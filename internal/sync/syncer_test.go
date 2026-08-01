package sync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/johntdyer/unifi-kuma/internal/config"
	"github.com/johntdyer/unifi-kuma/internal/kuma"
	"github.com/johntdyer/unifi-kuma/internal/unifi"
)

// --- Mocks ---

type mockUniFi struct {
	loginErr     error
	deviceGroups map[string][]unifi.MonitorableDevice
	devicesErr   error
}

func (m *mockUniFi) Login(_ context.Context, _, _ string) error {
	return m.loginErr
}

func (m *mockUniFi) MonitorableDevices(_ context.Context, _, _ string) (map[string][]unifi.MonitorableDevice, error) {
	return m.deviceGroups, m.devicesErr
}

type mockKuma struct {
	loginErr        error
	monitors        []kuma.Monitor
	monitorsErr     error
	createdMonitors []kuma.Monitor
	updatedMonitors []kuma.Monitor
	addTagsCalls    []int
	deletedIDs      []int
	nextID          int
}

func newMockKuma() *mockKuma {
	return &mockKuma{nextID: 100}
}

func (m *mockKuma) Login(_ context.Context, _, _ string) error {
	return m.loginErr
}

func (m *mockKuma) GetMonitors(_ context.Context) ([]kuma.Monitor, error) {
	return m.monitors, m.monitorsErr
}

func (m *mockKuma) CreateMonitor(_ context.Context, mon kuma.Monitor) (int, error) {
	id := m.nextID
	m.nextID++
	mon.ID = id
	m.monitors = append(m.monitors, mon)
	m.createdMonitors = append(m.createdMonitors, mon)
	return id, nil
}

func (m *mockKuma) UpdateMonitor(_ context.Context, mon kuma.Monitor) error {
	m.updatedMonitors = append(m.updatedMonitors, mon)
	for i, existing := range m.monitors {
		if existing.ID == mon.ID {
			// The real Kuma update API never touches tags — mirror that so
			// tests correctly exercise the tag-backfill path.
			mon.Tags = existing.Tags
			m.monitors[i] = mon
			break
		}
	}
	return nil
}

func (m *mockKuma) AddTags(_ context.Context, monitorID int, tags []kuma.MonitorTag) error {
	m.addTagsCalls = append(m.addTagsCalls, monitorID)
	for i, existing := range m.monitors {
		if existing.ID == monitorID {
			m.monitors[i].Tags = append(m.monitors[i].Tags, tags...)
			break
		}
	}
	return nil
}

func (m *mockKuma) DeleteMonitor(_ context.Context, id int) error {
	m.deletedIDs = append(m.deletedIDs, id)
	return nil
}

// createdGroupNames returns the names of monitor groups created this session.
func (m *mockKuma) createdGroupNames() []string {
	var names []string
	for _, mon := range m.createdMonitors {
		if mon.Type == kuma.MonitorTypeGroup {
			names = append(names, mon.Name)
		}
	}
	return names
}

// createdDeviceMonitors returns non-group monitors created this session.
func (m *mockKuma) createdDeviceMonitors() []kuma.Monitor {
	var devs []kuma.Monitor
	for _, mon := range m.createdMonitors {
		if mon.Type != kuma.MonitorTypeGroup {
			devs = append(devs, mon)
		}
	}
	return devs
}

// --- Helpers ---

func defaultCfg() *config.Config {
	return &config.Config{
		UniFi: config.UniFiConfig{Username: "admin", Password: "secret"},
		Kuma:  config.KumaConfig{Username: "kuma", Password: "kumasecret"},
		Sync: config.SyncConfig{
			Interval:           5 * time.Minute,
			MonitorGroup:       "monitor",
			GroupPrefix:        "kuma-group",
			HumanizeGroupNames: true,
			DryRun:             false,
			DeleteOrphan:       false,
		},
	}
}

func intPtr(i int) *int { return &i }

// --- Tests ---

func TestSyncOnce_CreatesGroupAndMonitor(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1", MAC: "aa:bb:cc:dd:ee:ff"},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.createdGroupNames(), "Servers")
	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1)
	assert.Equal(t, "gateway", devs[0].Name)
	assert.Equal(t, "192.168.1.1", devs[0].Hostname)
	assert.Equal(t, kuma.MonitorTypePing, devs[0].Type)
}

// TestSyncOnce_GroupMonitorHasValidInterval guards against a real bug: Kuma
// rejects any monitor, including groups, with interval 0 ("Interval cannot
// be less than 1 seconds").
func TestSyncOnce_GroupMonitorHasValidInterval(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	require.Len(t, k.createdMonitors, 2) // group + device
	group := k.createdMonitors[0]
	require.Equal(t, kuma.MonitorTypeGroup, group.Type)
	assert.Positive(t, group.Interval, "group monitor must have a positive interval")
	assert.True(t, kuma.IsManagedMonitor(group), "group monitor should be tagged as managed by unifi-kuma")
}

func TestSyncOnce_ReconcilesExistingMonitor(t *testing.T) {
	existingGroup := kuma.Monitor{ID: 50, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	existingMonitor := kuma.Monitor{
		ID: 51, Name: "gateway", Type: kuma.MonitorTypePing,
		Hostname: "192.168.1.1", ParentID: intPtr(50), Active: true,
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{existingGroup, existingMonitor}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	// Neither group nor monitor should be recreated — but the existing
	// monitor is still reconciled (updated in place) even when nothing
	// actually changed, since there's no cheap way to detect "unchanged".
	assert.Empty(t, k.createdGroupNames())
	assert.Empty(t, k.createdDeviceMonitors())
	require.Len(t, k.updatedMonitors, 1)
	assert.Equal(t, 51, k.updatedMonitors[0].ID)
}

// TestSyncOnce_UpdatesHostnameOnDrift verifies that when UniFi reports a new
// IP for a device that already has a monitor (e.g. after a DHCP lease
// renewal), the existing monitor's hostname is updated to match rather than
// staying frozen at whatever it was when first created.
func TestSyncOnce_UpdatesHostnameOnDrift(t *testing.T) {
	existingGroup := kuma.Monitor{ID: 50, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	staleMonitor := kuma.Monitor{
		ID: 51, Name: "sonos-garage", Type: kuma.MonitorTypePing,
		Hostname: "10.0.0.5", ParentID: intPtr(50), Active: true,
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "sonos-garage", Hostname: "10.0.0.42"}, // new IP
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{existingGroup, staleMonitor}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	require.Len(t, k.updatedMonitors, 1)
	assert.Equal(t, 51, k.updatedMonitors[0].ID)
	assert.Equal(t, "10.0.0.42", k.updatedMonitors[0].Hostname)
	assert.Empty(t, k.createdDeviceMonitors(), "should update the existing monitor, not create a new one")
}

func TestSyncOnce_DryRun(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "switch", Hostname: "192.168.1.2"},
			},
		},
	}
	k := newMockKuma()
	cfg := defaultCfg()
	cfg.Sync.DryRun = true

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.createdMonitors) // nothing created in dry-run
}

func TestSyncOnce_SkipsDeviceWithoutIP(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "no-ip-device", Hostname: ""},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.createdDeviceMonitors())
}

func TestSyncOnce_DeleteOrphans(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup}
	managed := kuma.Monitor{
		ID: 2, Name: "old-device", Type: kuma.MonitorTypePing,
		Hostname: "192.168.1.99", ParentID: intPtr(1), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	unmanaged := kuma.Monitor{
		ID: 3, Name: "manual-monitor", Type: kuma.MonitorTypePing,
		Hostname: "8.8.8.8", Active: true,
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "new-device", Hostname: "192.168.1.5"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{group, managed, unmanaged}

	cfg := defaultCfg()
	cfg.Sync.DeleteOrphan = true

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	// Only the managed orphan should be deleted; unmanaged left alone.
	assert.Contains(t, k.deletedIDs, 2)
	assert.NotContains(t, k.deletedIDs, 3)
}

func TestSyncOnce_DeleteOrphans_ContinuesOnError(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup}
	orphan1 := kuma.Monitor{ID: 2, Name: "gone1", Type: kuma.MonitorTypePing,
		ParentID: intPtr(1), Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}}}
	orphan2 := kuma.Monitor{ID: 3, Name: "gone2", Type: kuma.MonitorTypePing,
		ParentID: intPtr(1), Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}}}

	u := &mockUniFi{deviceGroups: map[string][]unifi.MonitorableDevice{}}

	// Custom mock that fails on the first delete.
	k := &errorOnFirstDeleteKuma{
		mockKuma: mockKuma{
			monitors: []kuma.Monitor{group, orphan1, orphan2},
			nextID:   100,
		},
	}

	cfg := defaultCfg()
	cfg.Sync.DeleteOrphan = true

	s := New(cfg, u, k)
	_ = s.SyncOnce(context.Background()) // error expected but should still process all

	// Both orphans should have been attempted.
	assert.Len(t, k.deletedIDs, 2)
}

// errorOnFirstDeleteKuma wraps mockKuma to fail on the first DeleteMonitor call.
type errorOnFirstDeleteKuma struct {
	mockKuma
	calls int
}

func (m *errorOnFirstDeleteKuma) DeleteMonitor(ctx context.Context, id int) error {
	m.calls++
	m.deletedIDs = append(m.deletedIDs, id)
	if m.calls == 1 {
		return assert.AnError
	}
	return nil
}

func TestSyncOnce_MultipleGroupsAndDevices(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{Name: "web1", Hostname: "10.0.0.1"},
				{Name: "web2", Hostname: "10.0.0.2"},
			},
			"network": {
				{Name: "router", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Len(t, k.createdGroupNames(), 2)
	assert.Len(t, k.createdDeviceMonitors(), 3)
}

func TestSyncOnce_OnlyOneFetchPerCycle(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{Name: "web1", Hostname: "10.0.0.1"},
				{Name: "web2", Hostname: "10.0.0.2"},
			},
			"network": {
				{Name: "router", Hostname: "192.168.1.1"},
			},
		},
	}
	k := &countingGetKuma{mockKuma: *newMockKuma()}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, k.getMonitorsCalls, "GetMonitors must be called exactly once per sync cycle")
}

// countingGetKuma tracks how many times GetMonitors is called.
type countingGetKuma struct {
	mockKuma
	getMonitorsCalls int
}

func (c *countingGetKuma) GetMonitors(ctx context.Context) ([]kuma.Monitor, error) {
	c.getMonitorsCalls++
	return c.mockKuma.GetMonitors(ctx)
}

func TestDisplayGroupName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"servers", "Servers"},
		{"home-network", "Home Network"},
		{"access-points", "Access Points"},
		{"unifi-cameras", "Unifi Cameras"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, displayGroupName(tt.name))
		})
	}
}

// TestSyncOnce_NoDuplicateMonitorForRepeatedDevice guards against a real
// bug: monitors created earlier in the same sync cycle must be visible to
// later duplicate checks in that same cycle, not just on the next cycle.
// Simulates an upstream duplicate (e.g. the same device listed twice for a
// group) by returning the same device twice in one group's device list —
// only one monitor should be created for it.
func TestSyncOnce_NoDuplicateMonitorForRepeatedDevice(t *testing.T) {
	dev := unifi.MonitorableDevice{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"}
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {dev, dev},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1, "the second identical device entry should be recognized as already synced, not duplicated")
}

// TestSyncOnce_ConsolidatesDuplicateGroups verifies that when two group
// monitors share the same name (e.g. from a historical bug or a race
// between process instances), the lower-ID one is kept as canonical, the
// higher-ID duplicate and everything parented to it are removed, and the
// device ends up with exactly one monitor under the surviving group.
func TestSyncOnce_ConsolidatesDuplicateGroups(t *testing.T) {
	canonicalGroup := kuma.Monitor{
		ID: 2595, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	duplicateGroup := kuma.Monitor{
		ID: 2597, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	staleUnderDuplicate := kuma.Monitor{
		ID: 2598, Name: "sonos-one", Type: kuma.MonitorTypePing,
		Hostname: "10.0.0.50", ParentID: intPtr(2597), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"sonos-speakers": {
				{GroupName: "sonos-speakers", Name: "sonos-one", Hostname: "10.0.0.50"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{canonicalGroup, duplicateGroup, staleUnderDuplicate}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.deletedIDs, 2597, "duplicate group monitor should be removed")
	assert.Contains(t, k.deletedIDs, 2598, "monitor parented to the duplicate group should be removed")

	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1, "device should be recreated exactly once, under the canonical group")
	require.NotNil(t, devs[0].ParentID)
	assert.Equal(t, 2595, *devs[0].ParentID)
}

// TestSyncOnce_ConsolidatesDuplicateGroups_DryRun verifies dry-run mode
// doesn't actually delete anything while consolidating.
func TestSyncOnce_ConsolidatesDuplicateGroups_DryRun(t *testing.T) {
	canonicalGroup := kuma.Monitor{
		ID: 2595, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	duplicateGroup := kuma.Monitor{
		ID: 2597, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"sonos-speakers": {
				{GroupName: "sonos-speakers", Name: "sonos-one", Hostname: "10.0.0.50"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{canonicalGroup, duplicateGroup}
	cfg := defaultCfg()
	cfg.Sync.DryRun = true

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.deletedIDs)
}

// TestSyncOnce_BackfillsTagOnExistingGroup verifies a group created before
// group tagging existed gets the managed-by tag backfilled the next time
// it's synced, rather than staying untagged forever.
func TestSyncOnce_BackfillsTagOnExistingGroup(t *testing.T) {
	untaggedGroup := kuma.Monitor{ID: 1, Name: "Iot", Type: kuma.MonitorTypeGroup, Active: true} // no Tags

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"iot": {
				{GroupName: "iot", Name: "thermostat", Hostname: "10.0.1.50"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{untaggedGroup}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.addTagsCalls, 1)
	assert.True(t, kuma.IsManagedMonitor(k.monitors[0]), "group should now carry the managed tag")
	assert.Empty(t, k.createdGroupNames(), "existing group should be reused, not recreated")
}

// TestSyncOnce_BackfillsTagOnExistingDevice verifies a device monitor
// created before device-monitor tagging existed gets the managed-by tag
// backfilled the next time it's reconciled.
func TestSyncOnce_BackfillsTagOnExistingDevice(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	untaggedDevice := kuma.Monitor{
		ID: 2, Name: "gateway", Type: kuma.MonitorTypePing,
		Hostname: "192.168.1.1", ParentID: intPtr(1), Active: true,
		// No Tags: created before device-monitor tagging existed.
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{group, untaggedDevice}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.addTagsCalls, 2)
	assert.True(t, kuma.IsManagedMonitor(k.monitors[1]), "device monitor should now carry the managed tag")
}

// TestSyncOnce_TagOtherGroups verifies that when enabled, a device's other
// UniFi group memberships (e.g. "apple") are added as Kuma tags alongside
// the managed-by tag, matching the exact scenario reported: a client with
// "monitor", "kuma-group-media", and "apple" should end up tagged "apple"
// and "unifi-kuma" in Kuma.
func TestSyncOnce_TagOtherGroups(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"media": {
				{GroupName: "media", Name: "mac-mini", Hostname: "10.0.0.40", OtherGroups: []string{"apple"}},
			},
		},
	}
	k := newMockKuma()
	cfg := defaultCfg()
	cfg.Sync.TagOtherGroups = true

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1)

	var tagNames []string
	for _, t := range devs[0].Tags {
		tagNames = append(tagNames, t.Name)
	}
	assert.ElementsMatch(t, []string{"unifi-kuma", "apple"}, tagNames)
}

// TestSyncOnce_TagOtherGroupsColor verifies SYNC_OTHER_GROUPS_TAG_COLOR is
// applied to other-group tags, but not to the unifi-kuma managed-by tag.
func TestSyncOnce_TagOtherGroupsColor(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"media": {
				{GroupName: "media", Name: "mac-mini", Hostname: "10.0.0.40", OtherGroups: []string{"apple"}},
			},
		},
	}
	k := newMockKuma()
	cfg := defaultCfg()
	cfg.Sync.TagOtherGroups = true
	cfg.Sync.OtherGroupsColor = "purple"

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1)

	tagsByName := make(map[string]string)
	for _, tag := range devs[0].Tags {
		tagsByName[tag.Name] = tag.Color
	}
	assert.Equal(t, "#7C3AED", tagsByName["apple"])
	assert.Equal(t, kuma.ManagedLabel().Color, tagsByName["unifi-kuma"])
}

// TestSyncOnce_TagOtherGroupsDisabled verifies OtherGroups is ignored by
// default — only the managed-by tag is applied.
func TestSyncOnce_TagOtherGroupsDisabled(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"media": {
				{GroupName: "media", Name: "mac-mini", Hostname: "10.0.0.40", OtherGroups: []string{"apple"}},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1)
	require.Len(t, devs[0].Tags, 1)
	assert.Equal(t, "unifi-kuma", devs[0].Tags[0].Name)
}

// TestSyncOnce_LeavesUnmanagedDuplicateGroupAlone verifies a group monitor
// without the managed-by tag — e.g. one a user created by hand that happens
// to collide by name — is never deleted, even though it looks like a
// duplicate of a managed group.
func TestSyncOnce_LeavesUnmanagedDuplicateGroupAlone(t *testing.T) {
	canonicalGroup := kuma.Monitor{
		ID: 2595, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	unmanagedGroup := kuma.Monitor{
		ID: 2597, Name: "Sonos Speakers", Type: kuma.MonitorTypeGroup, Active: true,
		// No managed-by tag: a user made this one by hand.
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"sonos-speakers": {
				{GroupName: "sonos-speakers", Name: "sonos-one", Hostname: "10.0.0.50"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{canonicalGroup, unmanagedGroup}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.deletedIDs, "unmanaged group must never be deleted, even as an apparent duplicate")
}

// TestSyncOnce_ConsolidatesDuplicateDevices verifies pre-existing duplicate
// monitors for the same device under the same group (garbage left over from
// before the within-cycle duplicate fix, or a race between process
// instances) get consolidated down to one, keeping the lowest ID.
func TestSyncOnce_ConsolidatesDuplicateDevices(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	canonicalDevice := kuma.Monitor{
		ID: 2586, Name: "server-proxmox2", Type: kuma.MonitorTypePing,
		Hostname: "10.222.222.11", ParentID: intPtr(1), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	duplicateDevice := kuma.Monitor{
		ID: 2589, Name: "server-proxmox2", Type: kuma.MonitorTypePing,
		Hostname: "10.222.222.11", ParentID: intPtr(1), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "server-proxmox2", Hostname: "10.222.222.11"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{group, canonicalDevice, duplicateDevice}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.deletedIDs, 2589, "the higher-ID duplicate should be removed")
	assert.NotContains(t, k.deletedIDs, 2586, "the canonical (lowest-ID) monitor should be kept")
	assert.Empty(t, k.createdDeviceMonitors(), "the surviving monitor already matches, nothing new should be created")
}

// TestSyncOnce_DoesNotConsolidateUnmanagedDuplicates verifies a monitor a
// user created by hand is never touched, even if it happens to share a name
// and parent with a managed one.
func TestSyncOnce_DoesNotConsolidateUnmanagedDuplicates(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	managedDevice := kuma.Monitor{
		ID: 2586, Name: "server-proxmox2", Type: kuma.MonitorTypePing,
		Hostname: "10.222.222.11", ParentID: intPtr(1), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	manualDevice := kuma.Monitor{
		ID: 2589, Name: "server-proxmox2", Type: kuma.MonitorTypePing,
		Hostname: "10.222.222.11", ParentID: intPtr(1), Active: true,
		// No managed-by tag: this one was created by hand.
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "server-proxmox2", Hostname: "10.222.222.11"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{group, managedDevice, manualDevice}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.deletedIDs, "unmanaged monitor must never be deleted, even as a duplicate")
}

// TestSyncOnce_RemovesStaleUngroupedMonitor verifies that once a device
// gains a real (non-Ungrouped) group, its old monitor under "Ungrouped" —
// left over from before it had any kuma-group-* membership — is removed,
// even though SYNC_DELETE_ORPHAN is off (the default).
func TestSyncOnce_RemovesStaleUngroupedMonitor(t *testing.T) {
	ungroupedGroup := kuma.Monitor{ID: 1, Name: ungroupedGroupName, Type: kuma.MonitorTypeGroup, Active: true}
	staleMonitor := kuma.Monitor{
		ID: 2, Name: "gateway", Type: kuma.MonitorTypePing,
		Hostname: "192.168.1.1", ParentID: intPtr(1), Active: true,
		Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{ungroupedGroup, staleMonitor}
	cfg := defaultCfg()
	cfg.Sync.DeleteOrphan = false // explicit: this cleanup must not depend on it

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.deletedIDs, 2, "stale Ungrouped monitor should be removed")
	devs := k.createdDeviceMonitors()
	require.Len(t, devs, 1)
	assert.Equal(t, "gateway", devs[0].Name)
}

// TestSyncOnce_NoUngroupedCleanupWithoutStaleMonitor verifies syncing a
// device into a real group doesn't error or call DeleteMonitor when there's
// no pre-existing "Ungrouped" group at all.
func TestSyncOnce_NoUngroupedCleanupWithoutStaleMonitor(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "gateway", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.deletedIDs)
}

// TestSyncOnce_UngroupedDeviceNotSelfDeleted verifies a device that
// genuinely lands under "Ungrouped" this cycle doesn't trigger its own
// cleanup.
func TestSyncOnce_UngroupedDeviceNotSelfDeleted(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			ungroupedGroupName: {
				{GroupName: ungroupedGroupName, Name: "loner", Hostname: "192.168.1.9"},
			},
		},
	}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.deletedIDs)
	assert.Contains(t, k.createdGroupNames(), ungroupedGroupName)
}

func TestSyncOnce_HumanizeGroupNamesDisabled(t *testing.T) {
	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"home-network": {
				{GroupName: "home-network", Name: "router", Hostname: "192.168.1.1"},
			},
		},
	}
	k := newMockKuma()
	cfg := defaultCfg()
	cfg.Sync.HumanizeGroupNames = false

	s := New(cfg, u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, k.createdGroupNames(), "home-network")
	assert.NotContains(t, k.createdGroupNames(), "Home Network")
}

func TestFindMonitorInList_Found(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "router", ParentID: intPtr(10), Type: kuma.MonitorTypePing},
	}
	m := findMonitorInList(monitors, "router", 10)
	require.NotNil(t, m)
	assert.Equal(t, 1, m.ID)
}

func TestFindMonitorInList_WrongParent(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "router", ParentID: intPtr(99), Type: kuma.MonitorTypePing},
	}
	m := findMonitorInList(monitors, "router", 10)
	assert.Nil(t, m)
}

func TestFindMonitorInList_DeParentedManaged(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "router", ParentID: nil, Type: kuma.MonitorTypePing,
			Tags: []kuma.MonitorTag{{Name: "unifi-kuma"}}},
	}
	// De-parented managed monitor should be found to prevent duplicate creation.
	m := findMonitorInList(monitors, "router", 10)
	require.NotNil(t, m)
}

func TestFindMonitorInList_DeParentedUnmanaged(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "router", ParentID: nil, Type: kuma.MonitorTypePing},
	}
	// Unmanaged monitor with no parent should NOT block creation.
	m := findMonitorInList(monitors, "router", 10)
	assert.Nil(t, m)
}

func TestFindDeviceMonitor_MatchesByMAC(t *testing.T) {
	monitors := []kuma.Monitor{
		{
			ID: 1, Name: "old-name", ParentID: intPtr(10), Type: kuma.MonitorTypePing,
			Description: deviceDescription("aa:bb:cc:dd:ee:ff"),
			Tags:        []kuma.MonitorTag{{Name: "unifi-kuma"}},
		},
	}
	device := unifi.MonitorableDevice{Name: "new-name", MAC: "aa:bb:cc:dd:ee:ff"}

	// Wrong parent (99) would defeat name-based matching, but MAC still finds it.
	m := findDeviceMonitor(monitors, device, 99)
	require.NotNil(t, m)
	assert.Equal(t, 1, m.ID)
}

func TestFindDeviceMonitor_IgnoresUnmanagedMonitor(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "other", ParentID: intPtr(10), Type: kuma.MonitorTypePing,
			Description: deviceDescription("aa:bb:cc:dd:ee:ff")}, // no managed-by tag
	}
	device := unifi.MonitorableDevice{Name: "new-name", MAC: "aa:bb:cc:dd:ee:ff"}

	m := findDeviceMonitor(monitors, device, 10)
	assert.Nil(t, m)
}

func TestFindDeviceMonitor_FallsBackToNameWithoutMAC(t *testing.T) {
	monitors := []kuma.Monitor{
		{ID: 1, Name: "router", ParentID: intPtr(10), Type: kuma.MonitorTypePing},
	}
	device := unifi.MonitorableDevice{Name: "router"} // no MAC

	m := findDeviceMonitor(monitors, device, 10)
	require.NotNil(t, m)
	assert.Equal(t, 1, m.ID)
}

func TestDescriptionMAC(t *testing.T) {
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", descriptionMAC(deviceDescription("aa:bb:cc:dd:ee:ff")))
	assert.Equal(t, "", descriptionMAC("managed by unifi-kuma"))
	assert.Equal(t, "", descriptionMAC(""))
}

func TestDescriptionGroupID(t *testing.T) {
	assert.Equal(t, "grp-123", descriptionGroupID(groupDescription("grp-123")))
	assert.Equal(t, "", descriptionGroupID(groupDescription("")))
	assert.Equal(t, "", descriptionGroupID(""))
}

func TestFindGroupBySourceID(t *testing.T) {
	monitors := []kuma.Monitor{
		{
			ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup,
			Description: groupDescription("grp-123"),
			Tags:        []kuma.MonitorTag{{Name: "unifi-kuma"}},
		},
	}

	m := findGroupBySourceID(monitors, "grp-123")
	require.NotNil(t, m)
	assert.Equal(t, 1, m.ID)

	assert.Nil(t, findGroupBySourceID(monitors, "grp-456"))
}

// TestSyncOnce_RenamesDeviceMonitorOnUniFiRename verifies that renaming a
// client/device in UniFi renames its existing Kuma monitor (matched by MAC)
// instead of creating a new one and leaving the old one orphaned under its
// stale name.
func TestSyncOnce_RenamesDeviceMonitorOnUniFiRename(t *testing.T) {
	group := kuma.Monitor{ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true}
	staleMonitor := kuma.Monitor{
		ID: 2, Name: "old-name", Type: kuma.MonitorTypePing,
		Hostname: "192.168.1.1", ParentID: intPtr(1), Active: true,
		Description: deviceDescription("aa:bb:cc:dd:ee:ff"),
		Tags:        []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"servers": {
				{GroupName: "servers", Name: "new-name", Hostname: "192.168.1.1", MAC: "aa:bb:cc:dd:ee:ff"},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{group, staleMonitor}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.createdDeviceMonitors(), "should update the existing monitor found by MAC, not create a new one")
	require.Len(t, k.updatedMonitors, 1)
	assert.Equal(t, 2, k.updatedMonitors[0].ID)
	assert.Equal(t, "new-name", k.updatedMonitors[0].Name)
}

// TestSyncOnce_RenamesGroupOnUniFiRename verifies that renaming a
// "{groupPrefix}-{name}" group in UniFi renames the matching existing Kuma
// group (matched by the UniFi group's stable ID, embedded in the group
// monitor's description) instead of creating a new group and leaving the
// old one — and its devices — orphaned.
func TestSyncOnce_RenamesGroupOnUniFiRename(t *testing.T) {
	staleGroup := kuma.Monitor{
		ID: 1, Name: "Servers", Type: kuma.MonitorTypeGroup, Active: true,
		Description: groupDescription("grp-123"),
		Tags:        []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}
	existingDevice := kuma.Monitor{
		ID: 2, Name: "gateway", Type: kuma.MonitorTypePing, Hostname: "192.168.1.1",
		ParentID: intPtr(1), Active: true,
		Description: deviceDescription("aa:bb:cc:dd:ee:ff"),
		Tags:        []kuma.MonitorTag{{Name: "unifi-kuma"}},
	}

	u := &mockUniFi{
		deviceGroups: map[string][]unifi.MonitorableDevice{
			"backend": {
				{
					GroupName: "backend", Name: "gateway", Hostname: "192.168.1.1",
					MAC: "aa:bb:cc:dd:ee:ff", SourceGroupID: "grp-123",
				},
			},
		},
	}
	k := newMockKuma()
	k.monitors = []kuma.Monitor{staleGroup, existingDevice}

	s := New(defaultCfg(), u, k)
	err := s.SyncOnce(context.Background())
	require.NoError(t, err)

	assert.Empty(t, k.createdGroupNames(), "should rename the existing group found by source ID, not create a new one")

	var groupUpdate *kuma.Monitor
	for i := range k.updatedMonitors {
		if k.updatedMonitors[i].Type == kuma.MonitorTypeGroup {
			groupUpdate = &k.updatedMonitors[i]
		}
	}
	require.NotNil(t, groupUpdate, "expected the group monitor to be renamed via UpdateMonitor")
	assert.Equal(t, 1, groupUpdate.ID)
	assert.Equal(t, "Backend", groupUpdate.Name)
}

func TestStart_StopsOnContextCancel(t *testing.T) {
	u := &mockUniFi{deviceGroups: map[string][]unifi.MonitorableDevice{}}
	k := newMockKuma()

	cfg := defaultCfg()
	cfg.Sync.Interval = 10 * time.Millisecond

	s := New(cfg, u, k)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after context cancellation")
	}
}

func TestStart_InitialSyncFatalOnError(t *testing.T) {
	u := &mockUniFi{deviceGroups: map[string][]unifi.MonitorableDevice{}}
	k := newMockKuma()
	k.monitorsErr = assert.AnError // GetMonitors fails → SyncOnce fails

	s := New(defaultCfg(), u, k)
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial sync")
}

func TestLogin_UniFiError(t *testing.T) {
	u := &mockUniFi{loginErr: assert.AnError}
	k := newMockKuma()

	s := New(defaultCfg(), u, k)
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UniFi login")
}

func TestLogin_KumaError(t *testing.T) {
	u := &mockUniFi{}
	k := newMockKuma()
	k.loginErr = assert.AnError

	s := New(defaultCfg(), u, k)
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Kuma login")
}
