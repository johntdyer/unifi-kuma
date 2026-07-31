// Package sync orchestrates the reconciliation loop between UniFi tags
// and Uptime Kuma monitors.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/johntdyer/unifi-kuma/internal/config"
	"github.com/johntdyer/unifi-kuma/internal/kuma"
	"github.com/johntdyer/unifi-kuma/internal/unifi"
)

// UniFiProvider is the subset of the UniFi client used by the syncer.
type UniFiProvider interface {
	Login(ctx context.Context, username, password string) error
	MonitorableDevices(ctx context.Context, monitorGroup, groupPrefix string) (map[string][]unifi.MonitorableDevice, error)
}

// KumaProvider is the subset of the Kuma client used by the syncer.
// Monitor lookup is done in-memory from the list returned by GetMonitors,
// so the provider only needs CRUD primitives.
type KumaProvider interface {
	Login(ctx context.Context, username, password string) error
	GetMonitors(ctx context.Context) ([]kuma.Monitor, error)
	CreateMonitor(ctx context.Context, m kuma.Monitor) (int, error)
	DeleteMonitor(ctx context.Context, id int) error
}

// Syncer reconciles UniFi tags with Uptime Kuma monitors on a schedule.
type Syncer struct {
	cfg    *config.Config
	unifi  UniFiProvider
	kuma   KumaProvider
	logger *slog.Logger
}

// New creates a Syncer with the given clients and configuration.
func New(cfg *config.Config, u UniFiProvider, k KumaProvider) *Syncer {
	return &Syncer{
		cfg:    cfg,
		unifi:  u,
		kuma:   k,
		logger: slog.Default().With("component", "syncer"),
	}
}

// Start logs in to both APIs and runs the sync loop until ctx is cancelled.
// The initial sync is treated as fatal: if it fails, Start returns the error
// so the process exits and the container restart policy can kick in.
func (s *Syncer) Start(ctx context.Context) error {
	if err := s.login(ctx); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "starting sync loop",
		"interval", s.cfg.Sync.Interval,
		"monitor_group", s.cfg.Sync.MonitorGroup,
		"group_prefix", s.cfg.Sync.GroupPrefix,
		"dry_run", s.cfg.Sync.DryRun,
	)

	if err := s.SyncOnce(ctx); err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}

	ticker := time.NewTicker(s.cfg.Sync.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.InfoContext(ctx, "sync loop stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := s.SyncOnce(ctx); err != nil {
				s.logger.ErrorContext(ctx, "sync failed", "error", err)
			}
		}
	}
}

// SyncOnce runs a single reconciliation cycle. It fetches all Kuma monitors
// exactly once and uses in-memory lookups for all subsequent group/monitor
// checks — no extra API calls per tag or per device.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting sync cycle")
	start := time.Now()

	deviceGroups, err := s.unifi.MonitorableDevices(ctx, s.cfg.Sync.MonitorGroup, s.cfg.Sync.GroupPrefix)
	if err != nil {
		return fmt.Errorf("fetching monitorable devices: %w", err)
	}

	// Fetch all Kuma monitors once; everything else works from this snapshot.
	monitors, err := s.kuma.GetMonitors(ctx)
	if err != nil {
		return fmt.Errorf("fetching existing monitors: %w", err)
	}

	// Index existing groups by name for O(1) lookup.
	groupsByName := make(map[string]int, len(monitors))
	for _, m := range monitors {
		if m.Type == kuma.MonitorTypeGroup {
			groupsByName[m.Name] = m.ID
		}
	}

	// Build desired state for orphan detection.
	desired := make(map[string]map[string]struct{}, len(deviceGroups))

	for groupName, devices := range deviceGroups {
		displayName := groupName
		if s.cfg.Sync.HumanizeGroupNames {
			displayName = displayGroupName(groupName)
		}

		desired[displayName] = make(map[string]struct{}, len(devices))
		for _, d := range devices {
			desired[displayName][d.Name] = struct{}{}
		}

		if err := s.syncGroup(ctx, groupName, displayName, devices, groupsByName, &monitors); err != nil {
			s.logger.ErrorContext(ctx, "failed to sync group",
				"group", groupName,
				"error", err,
			)
		}
	}

	if s.cfg.Sync.DeleteOrphan {
		if err := s.deleteOrphans(ctx, monitors, desired); err != nil {
			s.logger.ErrorContext(ctx, "failed to delete orphans", "error", err)
		}
	}

	s.logger.InfoContext(ctx, "sync cycle complete",
		"duration", time.Since(start),
		"groups", len(deviceGroups),
	)
	return nil
}

// syncGroup ensures a Kuma group exists for the UniFi group and that every
// device in it has a corresponding ping monitor inside that group. monitors
// is a pointer so newly created monitors are immediately visible to the
// duplicate checks in the rest of this sync cycle — without this, a device
// appearing twice (e.g. once per matching UniFi group) would pass the
// "does this monitor already exist" check twice against the same stale
// snapshot and create a duplicate.
func (s *Syncer) syncGroup(
	ctx context.Context,
	sourceName, displayName string,
	devices []unifi.MonitorableDevice,
	groupsByName map[string]int,
	monitors *[]kuma.Monitor,
) error {
	if len(devices) == 0 {
		s.logger.InfoContext(ctx, "skipping empty group", "group", sourceName)
		return nil
	}

	var groupID int
	if !s.cfg.Sync.DryRun {
		var err error
		groupID, err = s.ensureGroup(ctx, displayName, groupsByName, monitors)
		if err != nil {
			return fmt.Errorf("find/create group %q: %w", displayName, err)
		}
	}

	for _, device := range devices {
		if err := s.syncDevice(ctx, device, groupID, displayName, monitors); err != nil {
			s.logger.ErrorContext(ctx, "failed to sync device",
				"device", device.Name,
				"group", sourceName,
				"error", err,
			)
		}
	}

	return nil
}

// ensureGroup returns the ID of an existing group or creates one.
// The groups map is updated in-place so subsequent calls within the same
// sync cycle do not create duplicates and require no extra API calls.
func (s *Syncer) ensureGroup(ctx context.Context, name string, groups map[string]int, monitors *[]kuma.Monitor) (int, error) {
	if id, ok := groups[name]; ok {
		s.logger.InfoContext(ctx, "found existing group", "name", name, "id", id)
		return id, nil
	}

	group := kuma.Monitor{
		Type: kuma.MonitorTypeGroup,
		Name: name,
		// Uptime Kuma requires a positive interval even for group monitors,
		// which don't otherwise use it — match the device monitors' default.
		Interval:      60,
		RetryInterval: 60,
		Active:        true,
	}

	id, err := s.kuma.CreateMonitor(ctx, group)
	if err != nil {
		return 0, fmt.Errorf("creating group %q: %w", name, err)
	}

	groups[name] = id
	group.ID = id
	*monitors = append(*monitors, group)
	s.logger.InfoContext(ctx, "created group", "name", name, "id", id)
	return id, nil
}

// syncDevice creates a Kuma ping monitor for a device if one doesn't already exist.
// It uses the pre-fetched monitor list instead of making an additional API call.
func (s *Syncer) syncDevice(
	ctx context.Context,
	device unifi.MonitorableDevice,
	groupID int,
	groupName string,
	monitors *[]kuma.Monitor,
) error {
	if device.Hostname == "" {
		s.logger.WarnContext(ctx, "skipping device without IP",
			"device", device.Name,
			"mac", device.MAC,
		)
		return nil
	}

	s.logger.InfoContext(ctx, "syncing device",
		"device", device.Name,
		"hostname", device.Hostname,
		"group", groupName,
		"dry_run", s.cfg.Sync.DryRun,
	)

	if s.cfg.Sync.DryRun {
		return nil
	}

	if existing := findMonitorInList(*monitors, device.Name, groupID); existing != nil {
		s.logger.InfoContext(ctx, "monitor already exists, skipping",
			"device", device.Name,
			"monitor_id", existing.ID,
		)
		return nil
	}

	monitor := kuma.Monitor{
		Type:          kuma.MonitorTypePing,
		Name:          device.Name,
		Hostname:      device.Hostname,
		Interval:      60,
		RetryInterval: 60,
		MaxRetries:    3,
		ParentID:      &groupID,
		Active:        true,
		Description:   fmt.Sprintf("managed by unifi-kuma | mac:%s", device.MAC),
		Tags:          []kuma.MonitorTag{kuma.ManagedLabel()},
	}

	id, err := s.kuma.CreateMonitor(ctx, monitor)
	if err != nil {
		return fmt.Errorf("creating monitor for %s: %w", device.Name, err)
	}

	monitor.ID = id
	*monitors = append(*monitors, monitor)

	s.logger.InfoContext(ctx, "created monitor",
		"device", device.Name,
		"monitor_id", id,
		"group", groupName,
	)
	return nil
}

// findMonitorInList returns the first monitor in the list whose name and parent
// match the given criteria. A managed monitor with the same name but no parent
// (de-parented) is also returned to prevent duplicate creation.
func findMonitorInList(monitors []kuma.Monitor, name string, parentID int) *kuma.Monitor {
	for i := range monitors {
		m := &monitors[i]
		if m.Name != name {
			continue
		}
		if m.ParentID != nil && *m.ParentID == parentID {
			return m
		}
		if m.ParentID == nil && kuma.IsManagedMonitor(*m) {
			return m
		}
	}
	return nil
}

// deleteOrphans removes managed monitors that no longer correspond to a
// tagged device in UniFi. All candidates are processed even if some deletions
// fail; errors are collected and returned together.
func (s *Syncer) deleteOrphans(ctx context.Context, monitors []kuma.Monitor, desired map[string]map[string]struct{}) error {
	groupByID := make(map[int]string, len(monitors))
	for _, m := range monitors {
		if m.Type == kuma.MonitorTypeGroup {
			groupByID[m.ID] = m.Name
		}
	}

	var errs []error
	for _, m := range monitors {
		if m.Type == kuma.MonitorTypeGroup || !kuma.IsManagedMonitor(m) {
			continue
		}

		var parentName string
		if m.ParentID != nil {
			parentName = groupByID[*m.ParentID]
		}

		if deviceSet, ok := desired[parentName]; ok {
			if _, wanted := deviceSet[m.Name]; wanted {
				continue
			}
		}

		s.logger.InfoContext(ctx, "deleting orphaned monitor",
			"name", m.Name,
			"id", m.ID,
			"group", parentName,
		)

		if !s.cfg.Sync.DryRun {
			if err := s.kuma.DeleteMonitor(ctx, m.ID); err != nil {
				errs = append(errs, fmt.Errorf("deleting orphan %d (%s): %w", m.ID, m.Name, err))
			}
		}
	}

	return errors.Join(errs...)
}

// displayGroupName converts a UniFi group name like "unifi-cameras" into a
// readable Kuma group name like "Unifi Cameras", title-casing each
// hyphen-separated word.
func displayGroupName(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

// login authenticates both API clients.
func (s *Syncer) login(ctx context.Context) error {
	if err := s.unifi.Login(ctx, s.cfg.UniFi.Username, s.cfg.UniFi.Password); err != nil {
		return fmt.Errorf("UniFi login: %w", err)
	}
	if err := s.kuma.Login(ctx, s.cfg.Kuma.Username, s.cfg.Kuma.Password); err != nil {
		return fmt.Errorf("Kuma login: %w", err)
	}
	return nil
}
