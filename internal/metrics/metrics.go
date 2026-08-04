// Package metrics defines the Prometheus metrics unifi-kuma exposes on
// /metrics, plus the health state backing /healthz. It has no dependency on
// any other internal package, so unifi/kuma/sync can all report into it
// without import cycles.
package metrics

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const namespace = "unifi_kuma"

// Metrics holds every collector unifi-kuma exposes, registered against its
// own private registry (never the global default) so multiple Syncer
// instances — e.g. across test cases — never collide.
type Metrics struct {
	Registry *prometheus.Registry

	buildInfo             *prometheus.GaugeVec
	buildInfoVersion      string
	SyncsTotal            *prometheus.CounterVec
	SyncDurationSeconds   prometheus.Histogram
	LastSyncTimestamp     prometheus.Gauge
	LastSuccessTimestamp  prometheus.Gauge
	ManagedMonitors       prometheus.Gauge
	ManagedGroups         prometheus.Gauge
	MonitorsCreatedTotal  prometheus.Counter
	MonitorsUpdatedTotal  prometheus.Counter
	MonitorsDeletedTotal  prometheus.Counter
	OrphanedMonitors      prometheus.Gauge
	StaleClients          prometheus.Gauge
	CircuitBreakerTripped prometheus.Counter

	mu          sync.RWMutex
	lastSyncAt  time.Time
	lastSyncErr error
}

// New creates a fresh, independently-registered set of metrics. version is
// exposed as a build-info gauge label.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Static metric with value 1, labeled by version.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)
	reg.MustRegister(buildInfo)

	m := &Metrics{
		Registry:         reg,
		buildInfo:        buildInfo,
		buildInfoVersion: version,
		SyncsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "syncs_total",
			Help:      "Total number of sync cycles, by result (success or error).",
		}, []string{"result"}),
		SyncDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "sync_duration_seconds",
			Help:      "Duration of each sync cycle in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		LastSyncTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "last_sync_timestamp_seconds",
			Help:      "Unix timestamp of the most recent sync cycle, regardless of outcome.",
		}),
		LastSuccessTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "last_successful_sync_timestamp_seconds",
			Help:      "Unix timestamp of the most recent successful sync cycle.",
		}),
		ManagedMonitors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "managed_monitors",
			Help:      "Number of Kuma device monitors currently managed by unifi-kuma.",
		}),
		ManagedGroups: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "managed_groups",
			Help:      "Number of Kuma groups currently managed by unifi-kuma.",
		}),
		MonitorsCreatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "monitors_created_total",
			Help:      "Total number of Kuma monitors (device or group) created.",
		}),
		MonitorsUpdatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "monitors_updated_total",
			Help:      "Total number of existing Kuma monitors reconciled to match UniFi.",
		}),
		MonitorsDeletedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "monitors_deleted_total",
			Help:      "Total number of orphaned Kuma monitors deleted.",
		}),
		OrphanedMonitors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "orphaned_monitors",
			Help:      "Number of managed monitors with no matching UniFi device, found in the most recent sync cycle — set regardless of whether SYNC_DELETE_ORPHAN or the circuit breaker actually removed them.",
		}),
		StaleClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "stale_clients",
			Help:      "Number of monitored UniFi clients not seen within SYNC_STALE_WARN_DAYS, found in the most recent sync cycle. Informational only — never causes deletion.",
		}),
		CircuitBreakerTripped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "orphan_delete_circuit_breaker_tripped_total",
			Help:      "Total number of times the SYNC_MAX_ORPHAN_DELETE_PERCENT safeguard refused to delete orphaned monitors in a cycle.",
		}),
	}

	reg.MustRegister(
		m.SyncsTotal,
		m.SyncDurationSeconds,
		m.LastSyncTimestamp,
		m.LastSuccessTimestamp,
		m.ManagedMonitors,
		m.ManagedGroups,
		m.MonitorsCreatedTotal,
		m.MonitorsUpdatedTotal,
		m.MonitorsDeletedTotal,
		m.OrphanedMonitors,
		m.StaleClients,
		m.CircuitBreakerTripped,
	)

	return m
}

// SetVersion updates the build_info gauge's version label, removing the
// previous one. Lets a caller that built its Metrics before knowing the
// real version (e.g. Syncer.New, constructed before main has its -ldflags
// version string handy) correct it after the fact.
func (m *Metrics) SetVersion(version string) {
	if version == m.buildInfoVersion {
		return
	}
	m.buildInfo.DeleteLabelValues(m.buildInfoVersion)
	m.buildInfo.WithLabelValues(version).Set(1)
	m.buildInfoVersion = version
}

// RecordSync updates the sync-outcome metrics and the state backing
// Healthy. It should be called exactly once per sync cycle, regardless of
// outcome.
func (m *Metrics) RecordSync(duration time.Duration, err error) {
	m.mu.Lock()
	m.lastSyncAt = time.Now()
	m.lastSyncErr = err
	m.mu.Unlock()

	result := "success"
	if err != nil {
		result = "error"
	}
	m.SyncsTotal.WithLabelValues(result).Inc()
	m.SyncDurationSeconds.Observe(duration.Seconds())
	m.LastSyncTimestamp.SetToCurrentTime()
	if err == nil {
		m.LastSuccessTimestamp.SetToCurrentTime()
	}
}

// Healthy reports whether the most recent sync succeeded and, if maxAge is
// positive, happened within maxAge of now — catching a process that's
// still running but stuck (e.g. wedged on a network call) rather than only
// a crashed one. ok is false before the first sync ever completes.
func (m *Metrics) Healthy(maxAge time.Duration) (ok bool, detail string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.lastSyncAt.IsZero() {
		return false, "no sync has completed yet"
	}
	if m.lastSyncErr != nil {
		return false, fmt.Sprintf("last sync failed: %s", m.lastSyncErr)
	}
	if age := time.Since(m.lastSyncAt); maxAge > 0 && age > maxAge {
		return false, fmt.Sprintf("last successful sync was %s ago, exceeding the %s limit", age.Round(time.Second), maxAge)
	}
	return true, "ok"
}
