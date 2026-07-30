package kuma

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKumaTestServer(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	monitors := []Monitor{
		{ID: 1, Name: "Servers", Type: MonitorTypeGroup, Active: true},
		{ID: 2, Name: "gateway", Type: MonitorTypePing, Hostname: "192.168.1.1", ParentID: intPtr(1), Active: true,
			Tags: []MonitorTag{{Name: managedByLabel}}},
		{ID: 3, Name: "unmanaged", Type: MonitorTypePing, Hostname: "8.8.8.8", Active: true},
	}

	nextID := 10

	mux.HandleFunc("/login/access-token", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username == "admin" && req.Password == "secret" {
			json.NewEncoder(w).Encode(loginResponse{TokenType: "Bearer", Token: "test-token"})
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(errResponse{OK: false, Msg: "invalid credentials"})
		}
	})

	mux.HandleFunc("/api/v1/monitors", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(listResponse{OK: true, Monitors: monitors})
		case http.MethodPost:
			var m Monitor
			_ = json.NewDecoder(r.Body).Decode(&m)
			m.ID = nextID
			nextID++
			monitors = append(monitors, m)
			json.NewEncoder(w).Encode(createResponse{OK: true, MonitorID: m.ID, Monitor: m})
		}
	})

	mux.HandleFunc("/api/v1/monitors/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	})

	c := NewClient(server.URL)
	c.token = "test-token"
	return server, c
}

func intPtr(i int) *int { return &i }

// TestSetAPIKey verifies that setting an API key makes Login a no-op that
// skips the HTTP call entirely.
func TestSetAPIKey(t *testing.T) {
	_, c := newKumaTestServer(t)
	c.token = ""

	c.SetAPIKey("test-key")
	assert.Equal(t, "test-key", c.token)

	err := c.Login(context.Background(), "", "")
	require.NoError(t, err)
	assert.Equal(t, "test-key", c.token, "Login should skip the HTTP call and leave the API key token untouched")
}

// TestSetNoAuth verifies that Login becomes a no-op and that requests are
// sent without an Authorization header when auth is disabled.
func TestSetNoAuth(t *testing.T) {
	_, c := newKumaTestServer(t)
	c.token = ""

	c.SetNoAuth()

	err := c.Login(context.Background(), "", "")
	require.NoError(t, err)
	assert.Empty(t, c.token, "no-auth mode should never populate a token")

	req, err := c.newRequest(context.Background(), http.MethodGet, "/api/v1/monitors", nil)
	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("Authorization"))
}

func TestLogin_Success(t *testing.T) {
	_, c := newKumaTestServer(t)
	c.token = ""

	err := c.Login(context.Background(), "admin", "secret")
	require.NoError(t, err)
	assert.Equal(t, "test-token", c.token)
}

func TestLogin_BadCredentials(t *testing.T) {
	_, c := newKumaTestServer(t)
	c.token = ""

	err := c.Login(context.Background(), "admin", "wrong")
	require.Error(t, err)
}

func TestGetMonitors(t *testing.T) {
	_, c := newKumaTestServer(t)

	monitors, err := c.GetMonitors(context.Background())
	require.NoError(t, err)
	assert.Len(t, monitors, 3)
}

func TestGetGroups(t *testing.T) {
	_, c := newKumaTestServer(t)

	groups, err := c.GetGroups(context.Background())
	require.NoError(t, err)
	assert.Len(t, groups, 1)
	assert.Equal(t, "Servers", groups[0].Name)
	assert.Equal(t, MonitorTypeGroup, groups[0].Type)
}

func TestCreateMonitor(t *testing.T) {
	_, c := newKumaTestServer(t)

	parentID := 1
	id, err := c.CreateMonitor(context.Background(), Monitor{
		Type:     MonitorTypePing,
		Name:     "new-device",
		Hostname: "10.0.0.1",
		ParentID: &parentID,
		Active:   true,
	})
	require.NoError(t, err)
	assert.Greater(t, id, 0)
}

func TestDeleteMonitor(t *testing.T) {
	_, c := newKumaTestServer(t)

	err := c.DeleteMonitor(context.Background(), 2)
	require.NoError(t, err)
}

func TestFindOrCreateGroup_Existing(t *testing.T) {
	_, c := newKumaTestServer(t)

	id, err := c.FindOrCreateGroup(context.Background(), "Servers")
	require.NoError(t, err)
	assert.Equal(t, 1, id) // should find existing
}

func TestFindOrCreateGroup_New(t *testing.T) {
	_, c := newKumaTestServer(t)

	id, err := c.FindOrCreateGroup(context.Background(), "New Group")
	require.NoError(t, err)
	assert.Greater(t, id, 0)
}

func TestFindMonitorByName_Found(t *testing.T) {
	_, c := newKumaTestServer(t)

	parentID := 1
	m, err := c.FindMonitorByName(context.Background(), "gateway", &parentID)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, 2, m.ID)
}

func TestFindMonitorByName_DeParentedManaged(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	// Monitor with no parent but managed-by tag.
	monitors := []Monitor{
		{ID: 5, Name: "orphaned", Type: MonitorTypePing,
			Tags: []MonitorTag{{Name: managedByLabel}}},
	}
	mux.HandleFunc("/api/v1/monitors", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(listResponse{OK: true, Monitors: monitors})
	})

	c := NewClient(server.URL)
	c.token = "test-token"

	parentID := 42
	m, err := c.FindMonitorByName(context.Background(), "orphaned", &parentID)
	require.NoError(t, err)
	require.NotNil(t, m, "de-parented managed monitor should be found to prevent duplicates")
	assert.Equal(t, 5, m.ID)
}

func TestFindMonitorByName_NotFound(t *testing.T) {
	_, c := newKumaTestServer(t)

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
