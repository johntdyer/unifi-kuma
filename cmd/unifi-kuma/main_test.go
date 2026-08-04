package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/johntdyer/unifi-kuma/internal/config"
	"github.com/johntdyer/unifi-kuma/internal/kuma"
	"github.com/johntdyer/unifi-kuma/internal/sync"
	"github.com/johntdyer/unifi-kuma/internal/unifi"
)

func TestSetupLogger_Levels(t *testing.T) {
	defer slog.SetDefault(slog.Default())

	tests := []struct {
		name  string
		level string
		want  slog.Level
	}{
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"unknown defaults to info", "bogus", slog.LevelInfo},
		{"empty defaults to info", "", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupLogger(tt.level, false)

			logger := slog.Default()
			assert.True(t, logger.Enabled(context.Background(), tt.want))
			if tt.want > slog.LevelDebug {
				assert.False(t, logger.Enabled(context.Background(), tt.want-1))
			}
		})
	}
}

func TestSetupLogger_HandlerType(t *testing.T) {
	defer slog.SetDefault(slog.Default())

	setupLogger("info", true)
	_, isJSON := slog.Default().Handler().(*slog.JSONHandler)
	assert.True(t, isJSON, "expected JSONHandler when jsonOutput is true")

	setupLogger("info", false)
	_, isText := slog.Default().Handler().(*slog.TextHandler)
	assert.True(t, isText, "expected TextHandler when jsonOutput is false")
}

// setRequiredEnv sets the env vars config.Load requires to succeed, so
// runHealthcheck tests can focus on the HTTP_ADDR/healthz behavior.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("UNIFI_URL", "https://unifi.example.com")
	t.Setenv("UNIFI_USERNAME", "admin")
	t.Setenv("UNIFI_PASSWORD", "secret")
	t.Setenv("KUMA_URL", "http://kuma.example.com:3001")
	t.Setenv("KUMA_USERNAME", "kuma")
	t.Setenv("KUMA_PASSWORD", "kumasecret")
}

func TestRunHealthcheck_Healthy(t *testing.T) {
	setRequiredEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	t.Setenv("HTTP_ADDR", "127.0.0.1:"+port)

	assert.Equal(t, 0, runHealthcheck(""))
}

func TestRunHealthcheck_Unhealthy(t *testing.T) {
	setRequiredEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	t.Setenv("HTTP_ADDR", "127.0.0.1:"+port)

	assert.Equal(t, 1, runHealthcheck(""))
}

func TestRunHealthcheck_DisabledWhenAddrEmpty(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HTTP_ADDR", "")

	assert.Equal(t, 0, runHealthcheck(""), "no HTTP server to check should not be reported as unhealthy")
}

func TestRunHealthcheck_ConfigLoadError(t *testing.T) {
	// Deliberately don't set the required env vars.
	t.Setenv("UNIFI_URL", "")
	t.Setenv("UNIFI_USERNAME", "")
	t.Setenv("UNIFI_PASSWORD", "")
	t.Setenv("KUMA_URL", "")
	t.Setenv("KUMA_USERNAME", "")
	t.Setenv("KUMA_PASSWORD", "")

	assert.Equal(t, 1, runHealthcheck(""))
}

func TestRunHealthcheck_UnreachableServer(t *testing.T) {
	setRequiredEnv(t)
	// Nothing listening on this port.
	t.Setenv("HTTP_ADDR", "127.0.0.1:1")

	assert.Equal(t, 1, runHealthcheck(""))
}

func TestStartHTTPServer_DisabledWhenAddrEmpty(t *testing.T) {
	cfg := &config.Config{HTTP: config.HTTPConfig{Addr: ""}}
	syncer := sync.New(cfg, noopUniFi{}, noopKuma{})

	assert.Nil(t, startHTTPServer(cfg, syncer))
}

func TestStartHTTPServer_ServesWhenAddrSet(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{Addr: "127.0.0.1:0"},
		Sync: config.SyncConfig{Interval: time.Minute},
	}
	syncer := sync.New(cfg, noopUniFi{}, noopKuma{})

	srv := startHTTPServer(cfg, syncer)
	require.NotNil(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.NoError(t, srv.Shutdown(ctx))
}

// noopUniFi/noopKuma satisfy sync.UniFiProvider/KumaProvider minimally, for
// tests that only need a Syncer to exist (e.g. to call Metrics()) without
// actually running a sync cycle.
type noopUniFi struct{}

func (noopUniFi) Login(context.Context, string, string) error { return nil }
func (noopUniFi) MonitorableDevices(context.Context, string, string) (map[string][]unifi.MonitorableDevice, unifi.ResolutionStats, error) {
	return nil, unifi.ResolutionStats{}, nil
}

type noopKuma struct{}

func (noopKuma) Login(context.Context, string, string) error              { return nil }
func (noopKuma) GetMonitors(context.Context) ([]kuma.Monitor, error)      { return nil, nil }
func (noopKuma) CreateMonitor(context.Context, kuma.Monitor) (int, error) { return 0, nil }
func (noopKuma) UpdateMonitor(context.Context, kuma.Monitor) error        { return nil }
func (noopKuma) AddTags(context.Context, int, []kuma.MonitorTag) error    { return nil }
func (noopKuma) DeleteMonitor(context.Context, int) error                 { return nil }
