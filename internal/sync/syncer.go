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
	UpdateMonitor(ctx context.Context, m kuma.Monitor) error
	AddTags(ctx context.Context, monitorID int, tags []kuma.MonitorTag) error
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

	groupsByName := s.indexGroups(ctx, &monitors)
	s.consolidateDuplicateDevices(ctx, &monitors)

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

		var sourceGroupID string
		if len(devices) > 0 {
			sourceGroupID = devices[0].SourceGroupID
		}

		if err := s.syncGroup(ctx, groupName, displayName, sourceGroupID, devices, groupsByName, &monitors); err != nil {
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
	sourceName, displayName, sourceGroupID string,
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
		groupID, err = s.ensureGroup(ctx, displayName, sourceGroupID, groupsByName, monitors)
		if err != nil {
			return fmt.Errorf("find/create group %q: %w", displayName, err)
		}
	}

	for _, device := range devices {
		if err := s.syncDevice(ctx, device, groupID, displayName, groupsByName, monitors); err != nil {
			s.logger.ErrorContext(ctx, "failed to sync device",
				"device", device.Name,
				"group", sourceName,
				"error", err,
			)
		}
	}

	return nil
}

// indexGroups builds a name -> ID index of existing group monitors,
// consolidating any duplicates it finds along the way — multiple group
// monitors sharing the same name, a symptom of historical bugs or a race
// between separate process instances both creating the same group at once.
// Without consolidation, the index would key off whatever Go's (randomized)
// map iteration order over the underlying monitor list happened to produce,
// so which duplicate "wins" as the parent for new devices could change from
// cycle to cycle, scattering devices across both. The lowest ID is kept as
// canonical (the oldest, most likely to already have children); every other
// same-named group monitor, and anything parented to it, is removed — the
// normal sync loop then recreates anything still needed under the
// canonical group in this same cycle.
func (s *Syncer) indexGroups(ctx context.Context, monitors *[]kuma.Monitor) map[string]int {
	byName := make(map[string]int)
	byID := make(map[int]kuma.Monitor)
	duplicates := make(map[string][]int)

	for _, m := range *monitors {
		if m.Type != kuma.MonitorTypeGroup {
			continue
		}
		byID[m.ID] = m
		existing, ok := byName[m.Name]
		switch {
		case !ok:
			byName[m.Name] = m.ID
		case m.ID < existing:
			byName[m.Name] = m.ID
			duplicates[m.Name] = append(duplicates[m.Name], existing)
		default:
			duplicates[m.Name] = append(duplicates[m.Name], m.ID)
		}
	}

	for name, extraIDs := range duplicates {
		s.logger.WarnContext(ctx, "found duplicate Kuma groups with the same name",
			"name", name,
			"canonical_id", byName[name],
			"duplicate_ids", extraIDs,
		)
		for _, extraID := range extraIDs {
			// Only ever remove a duplicate we tagged ourselves — a group a
			// user happens to have created by hand with a matching name is
			// left alone rather than guessed at.
			if !kuma.IsManagedMonitor(byID[extraID]) {
				s.logger.WarnContext(ctx, "duplicate group is not managed by unifi-kuma, leaving it alone",
					"group_id", extraID,
					"name", name,
				)
				continue
			}
			s.consolidateGroup(ctx, extraID, monitors)
		}
	}

	return byName
}

// consolidateGroup removes a duplicate group monitor and everything
// parented to it.
func (s *Syncer) consolidateGroup(ctx context.Context, groupID int, monitors *[]kuma.Monitor) {
	if s.cfg.Sync.DryRun {
		s.logger.InfoContext(ctx, "would remove duplicate group and its monitors", "group_id", groupID)
		return
	}

	var children []int
	for _, m := range *monitors {
		if m.ParentID != nil && *m.ParentID == groupID {
			children = append(children, m.ID)
		}
	}

	for _, childID := range children {
		if err := s.kuma.DeleteMonitor(ctx, childID); err != nil {
			s.logger.ErrorContext(ctx, "failed to remove monitor under duplicate group",
				"monitor_id", childID,
				"group_id", groupID,
				"error", err,
			)
			continue
		}
		*monitors = removeMonitorByID(*monitors, childID)
	}

	if err := s.kuma.DeleteMonitor(ctx, groupID); err != nil {
		s.logger.ErrorContext(ctx, "failed to remove duplicate group", "group_id", groupID, "error", err)
		return
	}
	*monitors = removeMonitorByID(*monitors, groupID)
}

// consolidateDuplicateDevices removes duplicate managed (non-group)
// monitors sharing the same name and parent group, keeping the lowest ID
// as canonical. Unlike duplicate groups, the current sync loop can no
// longer create these (see the *[]kuma.Monitor threading in syncDevice) —
// this exists purely to clean up pre-existing garbage from before that fix,
// or from a race between separate process instances. Only monitors tagged
// as managed by this tool are touched, so a monitor a user created by hand
// that happens to share a name is never at risk.
func (s *Syncer) consolidateDuplicateDevices(ctx context.Context, monitors *[]kuma.Monitor) {
	type key struct {
		parentID int
		name     string
	}

	canonical := make(map[key]int)
	var duplicates []int

	for _, m := range *monitors {
		if m.Type == kuma.MonitorTypeGroup || m.ParentID == nil || !kuma.IsManagedMonitor(m) {
			continue
		}
		k := key{parentID: *m.ParentID, name: m.Name}
		existing, ok := canonical[k]
		switch {
		case !ok:
			canonical[k] = m.ID
		case m.ID < existing:
			canonical[k] = m.ID
			duplicates = append(duplicates, existing)
		default:
			duplicates = append(duplicates, m.ID)
		}
	}

	for _, id := range duplicates {
		s.logger.WarnContext(ctx, "found duplicate monitor for the same device, removing", "monitor_id", id)

		if s.cfg.Sync.DryRun {
			continue
		}

		if err := s.kuma.DeleteMonitor(ctx, id); err != nil {
			s.logger.ErrorContext(ctx, "failed to remove duplicate monitor", "monitor_id", id, "error", err)
			continue
		}
		*monitors = removeMonitorByID(*monitors, id)
	}
}

// ensureGroup returns the ID of an existing group or creates one.
// The groups map is updated in-place so subsequent calls within the same
// sync cycle do not create duplicates and require no extra API calls.
//
// A group found by name is still just a name match, which breaks the moment
// that name changes in UniFi — so sourceGroupID (embedded in the Kuma
// group's description, see groupDescription) is checked first: a match
// there identifies the same UniFi group regardless of its current display
// name, and reconcileGroup renames the Kuma group in place instead of a new
// one being created here and the old one left behind as an orphan.
func (s *Syncer) ensureGroup(ctx context.Context, name, sourceGroupID string, groups map[string]int, monitors *[]kuma.Monitor) (int, error) {
	if sourceGroupID != "" {
		if existing := findGroupBySourceID(*monitors, sourceGroupID); existing != nil {
			return s.reconcileGroup(ctx, existing, name, sourceGroupID, groups, monitors)
		}
	}

	if id, ok := groups[name]; ok {
		s.logger.InfoContext(ctx, "found existing group", "name", name, "id", id)
		if existing := findMonitorByIDInList(*monitors, id); existing != nil {
			return s.reconcileGroup(ctx, existing, name, sourceGroupID, groups, monitors)
		}
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
		Tags:          []kuma.MonitorTag{kuma.ManagedLabel()},
		Description:   groupDescription(sourceGroupID),
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

// reconcileGroup brings an already-found group monitor in line with the
// current desired name and source-group-ID description, renaming it via a
// single UpdateMonitor call when either has drifted, then backfills the
// managed-by tag. Used both for a group matched by sourceGroupID (which may
// need a rename) and one matched by name (which may still need its
// description backfilled with sourceGroupID for future rename detection).
func (s *Syncer) reconcileGroup(ctx context.Context, existing *kuma.Monitor, name, sourceGroupID string, groups map[string]int, monitors *[]kuma.Monitor) (int, error) {
	wantDescription := existing.Description
	if sourceGroupID != "" {
		wantDescription = groupDescription(sourceGroupID)
	}

	if existing.Name != name || existing.Description != wantDescription {
		updated := *existing
		updated.Name = name
		updated.Description = wantDescription
		if err := s.kuma.UpdateMonitor(ctx, updated); err != nil {
			return 0, fmt.Errorf("updating group %q (id %d): %w", name, existing.ID, err)
		}
		if existing.Name != name {
			s.logger.InfoContext(ctx, "renamed group to match UniFi",
				"old_name", existing.Name,
				"new_name", name,
				"id", existing.ID,
			)
		}
		existing.Name = name
		existing.Description = wantDescription
	}

	groups[name] = existing.ID
	s.backfillTags(ctx, existing.ID, []kuma.MonitorTag{kuma.ManagedLabel()}, monitors)
	return existing.ID, nil
}

// groupDescription formats the description embedded on a managed group
// monitor, carrying the source UniFi group's stable ID so ensureGroup can
// find it again even if the group is renamed in UniFi later. Returns the
// bare managed-by marker for sourceGroupID == "" (the synthetic "Ungrouped"
// bucket, which has no single UniFi group behind it).
func groupDescription(sourceGroupID string) string {
	if sourceGroupID == "" {
		return "managed by unifi-kuma"
	}
	return fmt.Sprintf("managed by unifi-kuma | group-id:%s", sourceGroupID)
}

// descriptionGroupID extracts the source group ID embedded by
// groupDescription, or "" if description isn't in that format.
func descriptionGroupID(description string) string {
	const marker = "group-id:"
	idx := strings.Index(description, marker)
	if idx == -1 {
		return ""
	}
	return strings.TrimSpace(description[idx+len(marker):])
}

// findGroupBySourceID returns the managed group monitor whose description
// carries the given source UniFi group ID, or nil if none matches.
func findGroupBySourceID(monitors []kuma.Monitor, sourceGroupID string) *kuma.Monitor {
	for i := range monitors {
		m := &monitors[i]
		if m.Type != kuma.MonitorTypeGroup || !kuma.IsManagedMonitor(*m) {
			continue
		}
		if descriptionGroupID(m.Description) == sourceGroupID {
			return m
		}
	}
	return nil
}

// findMonitorByIDInList returns a pointer to the monitor with the given ID,
// or nil if not found.
func findMonitorByIDInList(monitors []kuma.Monitor, id int) *kuma.Monitor {
	for i := range monitors {
		if monitors[i].ID == id {
			return &monitors[i]
		}
	}
	return nil
}

// backfillTags ensures the monitor found by ID in monitors carries every tag
// in desired, adding whichever ones are missing (matched by name) and
// leaving any others already on the monitor untouched — tags are only ever
// added here, never removed, so e.g. a tag from a UniFi group the device
// was later taken out of stays put rather than disappearing on its own.
// This exists because CreateMonitor is the only place tags were ever
// applied before — UpdateMonitor doesn't touch them (Kuma manages tags as
// separate associations) — so anything created by an earlier version of
// this tool, or reconciled before a new desired tag was introduced, would
// otherwise never pick it up and stay invisible to every managed-only
// mechanism (orphan deletion, duplicate consolidation).
func (s *Syncer) backfillTags(ctx context.Context, monitorID int, desired []kuma.MonitorTag, monitors *[]kuma.Monitor) {
	for i := range *monitors {
		m := &(*monitors)[i]
		if m.ID != monitorID {
			continue
		}

		have := make(map[string]struct{}, len(m.Tags))
		for _, t := range m.Tags {
			have[t.Name] = struct{}{}
		}

		var missing []kuma.MonitorTag
		for _, t := range desired {
			if _, ok := have[t.Name]; !ok {
				missing = append(missing, t)
			}
		}
		if len(missing) == 0 {
			return
		}

		if err := s.kuma.AddTags(ctx, monitorID, missing); err != nil {
			s.logger.ErrorContext(ctx, "failed to backfill tags",
				"monitor_id", monitorID,
				"name", m.Name,
				"error", err,
			)
			return
		}

		m.Tags = append(m.Tags, missing...)
		s.logger.InfoContext(ctx, "backfilled tags onto existing monitor",
			"monitor_id", monitorID,
			"name", m.Name,
			"count", len(missing),
		)
		return
	}
}

// ungroupedGroupName is the Kuma group devices land in when they're
// monitored but don't match any {groupPrefix}-{name} UniFi group. It's
// never affected by SYNC_HUMANIZE_GROUP_NAMES since it doesn't come from a
// UniFi group name in the first place.
const ungroupedGroupName = "Ungrouped"

// syncDevice creates a Kuma ping monitor for a device if one doesn't already
// exist. It uses the pre-fetched monitor list instead of making an
// additional API call. If the device now belongs to a real (non-Ungrouped)
// group, any stale monitor left over under "Ungrouped" from before it had a
// matching group is removed — this runs regardless of SYNC_DELETE_ORPHAN,
// since it's a narrow, obviously-safe cleanup of the same device's own
// previous placement rather than a general orphan sweep.
func (s *Syncer) syncDevice(
	ctx context.Context,
	device unifi.MonitorableDevice,
	groupID int,
	groupName string,
	groupsByName map[string]int,
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

	tags := []kuma.MonitorTag{kuma.ManagedLabel()}
	if s.cfg.Sync.TagOtherGroups {
		color := kuma.TagColorHex(s.cfg.Sync.OtherGroupsColor)
		for _, g := range device.OtherGroups {
			tags = append(tags, kuma.MonitorTag{Name: g, Color: color})
		}
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
		Description:   deviceDescription(device.MAC),
		Tags:          tags,
	}

	if existing := findDeviceMonitor(*monitors, device, groupID); existing != nil {
		// Reconcile the existing monitor to the current desired state every
		// cycle (hostname, interval, etc.) rather than leaving it frozen at
		// whatever it was set to on creation — otherwise an IP change in
		// UniFi (DHCP renewal, etc.) would never reach an already-synced
		// monitor. GetMonitors doesn't return per-type fields like hostname,
		// so there's no cheap way to check for drift before deciding to
		// update; this always re-applies the desired config instead.
		monitor.ID = existing.ID
		if err := s.kuma.UpdateMonitor(ctx, monitor); err != nil {
			return fmt.Errorf("updating monitor for %s: %w", device.Name, err)
		}

		// UpdateMonitor doesn't touch tags — Kuma manages them as separate
		// associations — so preserve the real current tags here rather than
		// assuming ours are already set, and backfill whichever desired
		// tags are missing separately (covers a monitor created before
		// device-monitor tagging existed, or before TagOtherGroups was
		// turned on).
		desiredTags := monitor.Tags
		monitor.Tags = existing.Tags
		*existing = monitor
		s.backfillTags(ctx, existing.ID, desiredTags, monitors)

		s.logger.InfoContext(ctx, "reconciled existing monitor",
			"device", device.Name,
			"monitor_id", monitor.ID,
			"hostname", device.Hostname,
		)
	} else {
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
	}

	if groupName != ungroupedGroupName {
		s.removeFromUngrouped(ctx, device.Name, groupsByName, monitors)
	}

	return nil
}

// removeFromUngrouped deletes a device's monitor under the "Ungrouped" Kuma
// group, if one exists. Called once a device has been placed in a real
// group, so it doesn't end up listed under both.
func (s *Syncer) removeFromUngrouped(ctx context.Context, deviceName string, groupsByName map[string]int, monitors *[]kuma.Monitor) {
	ungroupedID, ok := groupsByName[ungroupedGroupName]
	if !ok {
		return
	}

	stale := findMonitorInList(*monitors, deviceName, ungroupedID)
	if stale == nil {
		return
	}

	s.logger.InfoContext(ctx, "removing stale Ungrouped monitor now that device has a real group",
		"device", deviceName,
		"monitor_id", stale.ID,
	)

	if err := s.kuma.DeleteMonitor(ctx, stale.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to remove stale Ungrouped monitor",
			"device", deviceName,
			"monitor_id", stale.ID,
			"error", err,
		)
		return
	}

	*monitors = removeMonitorByID(*monitors, stale.ID)
}

// removeMonitorByID returns monitors with the entry matching id removed.
func removeMonitorByID(monitors []kuma.Monitor, id int) []kuma.Monitor {
	for i, m := range monitors {
		if m.ID == id {
			return append(monitors[:i], monitors[i+1:]...)
		}
	}
	return monitors
}

// deviceDescription formats the description embedded on a managed device
// monitor, carrying the device's UniFi MAC address so findDeviceMonitor can
// find it again even if the device is renamed in UniFi later.
func deviceDescription(mac string) string {
	return fmt.Sprintf("managed by unifi-kuma | mac:%s", mac)
}

// descriptionMAC extracts the MAC address embedded by deviceDescription, or
// "" if description isn't in that format (e.g. an unmanaged monitor).
func descriptionMAC(description string) string {
	const marker = "mac:"
	idx := strings.Index(description, marker)
	if idx == -1 {
		return ""
	}
	return strings.TrimSpace(description[idx+len(marker):])
}

// findDeviceMonitor locates the managed ping monitor for device, preferring
// a match on its UniFi MAC address (embedded in the monitor's description)
// over its current name and group. Matching on MAC means a client rename in
// UniFi updates the existing monitor in place instead of leaving a
// same-named orphan behind, and as a side effect a device that moves to a
// different UniFi group is relocated (via the full UpdateMonitor that
// follows) rather than duplicated under the new one. Falls back to a
// name+group match for monitors that predate MAC-based descriptions, or
// when device.MAC is unavailable.
func findDeviceMonitor(monitors []kuma.Monitor, device unifi.MonitorableDevice, parentID int) *kuma.Monitor {
	if device.MAC != "" {
		for i := range monitors {
			m := &monitors[i]
			if m.Type != kuma.MonitorTypePing || !kuma.IsManagedMonitor(*m) {
				continue
			}
			if mac := descriptionMAC(m.Description); mac != "" && strings.EqualFold(mac, device.MAC) {
				return m
			}
		}
	}
	return findMonitorInList(monitors, device.Name, parentID)
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
