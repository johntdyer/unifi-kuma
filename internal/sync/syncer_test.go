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
}

func TestSyncOnce_SkipsExistingMonitor(t *testing.T) {
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

	// Neither group nor monitor should be recreated.
	assert.Empty(t, k.createdGroupNames())
	assert.Empty(t, k.createdDeviceMonitors())
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
