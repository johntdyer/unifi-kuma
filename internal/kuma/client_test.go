package kuma

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kumamonitor "github.com/breml/go-uptime-kuma-client/monitor"
	kumatag "github.com/breml/go-uptime-kuma-client/tag"
)

// fakeConn is an in-memory stand-in for the Socket.IO connection to Uptime
// Kuma, letting Client's request/response translation logic be tested
// without a live server.
type fakeConn struct {
	monitors []kumamonitor.Base
	tags     []kumatag.Tag

	nextMonitorID int64
	nextTagID     int64

	createdMonitors []kumamonitor.Monitor
	updatedMonitors []kumamonitor.Monitor
	deletedMonitors []int64
	monitorTags     map[int64][]int64 // monitorID -> tagIDs added, in order

	getMonitorsErr   error
	createMonitorErr error
	updateMonitorErr error
	deleteMonitorErr error
	getTagsErr       error
	createTagErr     error
	addMonitorTagErr error

	disconnected bool
}

func (f *fakeConn) GetMonitors(context.Context) ([]kumamonitor.Base, error) {
	if f.getMonitorsErr != nil {
		return nil, f.getMonitorsErr
	}
	return f.monitors, nil
}

func (f *fakeConn) CreateMonitor(_ context.Context, mon kumamonitor.Monitor) (int64, error) {
	if f.createMonitorErr != nil {
		return 0, f.createMonitorErr
	}
	f.createdMonitors = append(f.createdMonitors, mon)
	f.nextMonitorID++
	return f.nextMonitorID, nil
}

func (f *fakeConn) UpdateMonitor(_ context.Context, mon kumamonitor.Monitor) error {
	if f.updateMonitorErr != nil {
		return f.updateMonitorErr
	}
	f.updatedMonitors = append(f.updatedMonitors, mon)
	return nil
}

func (f *fakeConn) DeleteMonitor(_ context.Context, id int64) error {
	if f.deleteMonitorErr != nil {
		return f.deleteMonitorErr
	}
	f.deletedMonitors = append(f.deletedMonitors, id)
	return nil
}

func (f *fakeConn) GetTags(context.Context) ([]kumatag.Tag, error) {
	if f.getTagsErr != nil {
		return nil, f.getTagsErr
	}
	return f.tags, nil
}

func (f *fakeConn) CreateTag(_ context.Context, t kumatag.Tag) (int64, error) {
	if f.createTagErr != nil {
		return 0, f.createTagErr
	}
	f.nextTagID++
	t.ID = f.nextTagID
	f.tags = append(f.tags, t)
	return t.ID, nil
}

func (f *fakeConn) AddMonitorTag(_ context.Context, tagID, monitorID int64, value string) (*kumatag.MonitorTag, error) {
	if f.addMonitorTagErr != nil {
		return nil, f.addMonitorTagErr
	}
	if f.monitorTags == nil {
		f.monitorTags = map[int64][]int64{}
	}
	f.monitorTags[monitorID] = append(f.monitorTags[monitorID], tagID)
	return &kumatag.MonitorTag{TagID: tagID, MonitorID: monitorID, Value: value}, nil
}

func (f *fakeConn) Disconnect() error {
	f.disconnected = true
	return nil
}

// mustBaseMonitor builds a kumamonitor.Base from raw JSON. Base's type
// discriminator is only populated by its custom UnmarshalJSON, so there's no
// direct constructor for test fixtures.
func mustBaseMonitor(t *testing.T, rawJSON string) kumamonitor.Base {
	t.Helper()
	var m kumamonitor.Base
	require.NoError(t, json.Unmarshal([]byte(rawJSON), &m))
	return m
}

func newTestClient(conn *fakeConn) *Client {
	return &Client{
		conn:     conn,
		logger:   slog.Default(),
		tagCache: make(map[string]int64),
	}
}

func newFixtureConn(t *testing.T) *fakeConn {
	t.Helper()
	return &fakeConn{
		monitors: []kumamonitor.Base{
			mustBaseMonitor(t, `{"id":1,"type":"group","name":"Servers","active":true}`),
			mustBaseMonitor(t, `{"id":2,"type":"ping","name":"gateway","parent":1,"active":true,
				"tags":[{"id":1,"tag_id":100,"monitor_id":2,"name":"unifi-kuma","color":"#4a90d9"}]}`),
			mustBaseMonitor(t, `{"id":3,"type":"ping","name":"unmanaged","active":true}`),
		},
		nextMonitorID: 9,
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("http://kuma.example.com:3001/")
	assert.Equal(t, "http://kuma.example.com:3001", c.baseURL)
}

func TestSetNoAuth(t *testing.T) {
	c := NewClient("http://kuma.example.com:3001")
	assert.False(t, c.noAuth)
	c.SetNoAuth()
	assert.True(t, c.noAuth)
}

func TestClose(t *testing.T) {
	conn := &fakeConn{}
	c := newTestClient(conn)

	require.NoError(t, c.Close())
	assert.True(t, conn.disconnected)
}

func TestClose_NoConnection(t *testing.T) {
	c := &Client{}
	require.NoError(t, c.Close())
}

func TestGetMonitors(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	monitors, err := c.GetMonitors(context.Background())
	require.NoError(t, err)
	require.Len(t, monitors, 3)

	group := monitors[0]
	assert.Equal(t, 1, group.ID)
	assert.Equal(t, MonitorTypeGroup, group.Type)
	assert.Nil(t, group.ParentID)

	gateway := monitors[1]
	assert.Equal(t, 2, gateway.ID)
	assert.Equal(t, MonitorTypePing, gateway.Type)
	require.NotNil(t, gateway.ParentID)
	assert.Equal(t, 1, *gateway.ParentID)
	require.Len(t, gateway.Tags, 1)
	assert.Equal(t, managedByLabel, gateway.Tags[0].Name)
}

func TestGetMonitors_Error(t *testing.T) {
	conn := &fakeConn{getMonitorsErr: errors.New("boom")}
	c := newTestClient(conn)

	_, err := c.GetMonitors(context.Background())
	require.Error(t, err)
}

func TestGetGroups(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	groups, err := c.GetGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "Servers", groups[0].Name)
	assert.Equal(t, MonitorTypeGroup, groups[0].Type)
}

func TestCreateMonitor_Ping(t *testing.T) {
	conn := newFixtureConn(t)
	c := newTestClient(conn)

	parentID := 1
	id, err := c.CreateMonitor(context.Background(), Monitor{
		Type:     MonitorTypePing,
		Name:     "new-device",
		Hostname: "10.0.0.1",
		ParentID: &parentID,
		Active:   true,
		Tags:     []MonitorTag{ManagedLabel()},
	})
	require.NoError(t, err)
	assert.Equal(t, 10, id)

	require.Len(t, conn.createdMonitors, 1)
	ping, ok := conn.createdMonitors[0].(*kumamonitor.Ping)
	require.True(t, ok, "expected a *monitor.Ping to have been created")
	assert.Equal(t, "new-device", ping.Name)
	assert.Equal(t, "10.0.0.1", ping.Hostname)
	require.NotNil(t, ping.Parent)
	assert.Equal(t, int64(1), *ping.Parent)
	// Kuma's "timeout" DB column is NOT NULL even for ping monitors, though
	// the client library types it as optional — a nil pointer here breaks
	// monitor creation server-side.
	require.NotNil(t, ping.Timeout)
	assert.Equal(t, defaultPingTimeout, *ping.Timeout)

	// The managed-by tag should have been created and associated.
	require.Len(t, conn.tags, 1)
	assert.Equal(t, managedByLabel, conn.tags[0].Name)
	assert.Equal(t, []int64{conn.tags[0].ID}, conn.monitorTags[10])
}

func TestCreateMonitor_Group(t *testing.T) {
	conn := newFixtureConn(t)
	c := newTestClient(conn)

	id, err := c.CreateMonitor(context.Background(), Monitor{
		Type:   MonitorTypeGroup,
		Name:   "New Group",
		Active: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 10, id)

	require.Len(t, conn.createdMonitors, 1)
	_, ok := conn.createdMonitors[0].(*kumamonitor.Group)
	assert.True(t, ok, "expected a *monitor.Group to have been created")
}

func TestCreateMonitor_ReusesExistingTag(t *testing.T) {
	conn := newFixtureConn(t)
	conn.tags = []kumatag.Tag{{ID: 42, Name: managedByLabel, Color: "#4a90d9"}}
	c := newTestClient(conn)

	_, err := c.CreateMonitor(context.Background(), Monitor{
		Type: MonitorTypePing, Name: "a", Active: true, Tags: []MonitorTag{ManagedLabel()},
	})
	require.NoError(t, err)

	// No new tag should have been created — the existing one was reused.
	assert.Len(t, conn.tags, 1)
	assert.Equal(t, []int64{42}, conn.monitorTags[10])
}

func TestCreateMonitor_UnsupportedType(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	_, err := c.CreateMonitor(context.Background(), Monitor{Type: MonitorTypeHTTP, Name: "x"})
	require.Error(t, err)
}

func TestCreateMonitor_Error(t *testing.T) {
	conn := newFixtureConn(t)
	conn.createMonitorErr = errors.New("boom")
	c := newTestClient(conn)

	_, err := c.CreateMonitor(context.Background(), Monitor{Type: MonitorTypePing, Name: "x", Active: true})
	require.Error(t, err)
}

func TestUpdateMonitor(t *testing.T) {
	conn := newFixtureConn(t)
	c := newTestClient(conn)

	parentID := 1
	err := c.UpdateMonitor(context.Background(), Monitor{
		ID:       2,
		Type:     MonitorTypePing,
		Name:     "gateway",
		Hostname: "10.0.0.99", // updated IP
		ParentID: &parentID,
		Active:   true,
	})
	require.NoError(t, err)

	require.Len(t, conn.updatedMonitors, 1)
	ping, ok := conn.updatedMonitors[0].(*kumamonitor.Ping)
	require.True(t, ok, "expected a *monitor.Ping to have been updated")
	assert.Equal(t, int64(2), ping.ID)
	assert.Equal(t, "10.0.0.99", ping.Hostname)
}

func TestUpdateMonitor_Error(t *testing.T) {
	conn := newFixtureConn(t)
	conn.updateMonitorErr = errors.New("boom")
	c := newTestClient(conn)

	err := c.UpdateMonitor(context.Background(), Monitor{ID: 2, Type: MonitorTypePing, Name: "x", Active: true})
	require.Error(t, err)
}

func TestDeleteMonitor(t *testing.T) {
	conn := newFixtureConn(t)
	c := newTestClient(conn)

	err := c.DeleteMonitor(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, conn.deletedMonitors)
}

func TestFindOrCreateGroup_Existing(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	id, err := c.FindOrCreateGroup(context.Background(), "Servers")
	require.NoError(t, err)
	assert.Equal(t, 1, id) // should find existing
}

func TestFindOrCreateGroup_New(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	id, err := c.FindOrCreateGroup(context.Background(), "New Group")
	require.NoError(t, err)
	assert.Equal(t, 10, id)
}

func TestFindMonitorByName_Found(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	parentID := 1
	m, err := c.FindMonitorByName(context.Background(), "gateway", &parentID)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, 2, m.ID)
}

func TestFindMonitorByName_DeParentedManaged(t *testing.T) {
	conn := &fakeConn{
		monitors: []kumamonitor.Base{
			mustBaseMonitor(t, `{"id":5,"type":"ping","name":"orphaned",
				"tags":[{"id":1,"tag_id":100,"monitor_id":5,"name":"unifi-kuma","color":"#4a90d9"}]}`),
		},
	}
	c := newTestClient(conn)

	parentID := 42
	m, err := c.FindMonitorByName(context.Background(), "orphaned", &parentID)
	require.NoError(t, err)
	require.NotNil(t, m, "de-parented managed monitor should be found to prevent duplicates")
	assert.Equal(t, 5, m.ID)
}

func TestFindMonitorByName_NotFound(t *testing.T) {
	c := newTestClient(newFixtureConn(t))

	parentID := 1
	m, err := c.FindMonitorByName(context.Background(), "does-not-exist", &parentID)
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestIsManagedMonitor(t *testing.T) {
	managed := Monitor{Tags: []MonitorTag{{Name: managedByLabel}}}
	unmanaged := Monitor{Tags: []MonitorTag{{Name: "other"}}}
	noTags := Monitor{}

	assert.True(t, IsManagedMonitor(managed))
	assert.False(t, IsManagedMonitor(unmanaged))
	assert.False(t, IsManagedMonitor(noTags))
}

func TestManagedLabel(t *testing.T) {
	label := ManagedLabel()
	assert.Equal(t, managedByLabel, label.Name)
	assert.NotEmpty(t, label.Color)
}
