package unifi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testServer is a minimal UniFi mock that records requests.
type testServer struct {
	mux     *http.ServeMux
	server  *httptest.Server
	site    string
	isUDM   bool
	devices []Device
	clients []NetworkClient
	tags    []Tag
}

func newTestServer(t *testing.T, udm bool) *testServer {
	t.Helper()

	ts := &testServer{
		mux:   http.NewServeMux(),
		site:  "default",
		isUDM: udm,
	}
	ts.server = httptest.NewTLSServer(ts.mux)
	t.Cleanup(ts.server.Close)

	// Auth
	loginPath := "/api/auth/login"
	if !udm {
		loginPath = "/api/login"
	}
	ts.mux.HandleFunc(loginPath, func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username == "admin" && req.Password == "secret" {
			w.Header().Set("X-Csrf-Token", "test-csrf")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	})

	// Register 404 for the wrong auth endpoint so fallback works.
	if udm {
		ts.mux.HandleFunc("/api/login", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
	} else {
		ts.mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
	}

	ts.mux.HandleFunc("/v2/api/site/default/tag", ts.handleTags)
	ts.mux.HandleFunc("/proxy/network/v2/api/site/default/tag", ts.handleTags)

	ts.mux.HandleFunc("/api/s/default/stat/device", ts.handleDevices)
	ts.mux.HandleFunc("/proxy/network/api/s/default/stat/device", ts.handleDevices)

	ts.mux.HandleFunc("/api/s/default/rest/user", ts.handleClients)
	ts.mux.HandleFunc("/proxy/network/api/s/default/rest/user", ts.handleClients)

	return ts
}

func (ts *testServer) handleTags(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"data": ts.tags})
}

func (ts *testServer) handleDevices(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(apiResponse[Device]{Data: ts.devices})
}

func (ts *testServer) handleClients(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(apiResponse[NetworkClient]{Data: ts.clients})
}

func (ts *testServer) client(t *testing.T) *Client {
	t.Helper()
	c := &Client{
		baseURL: ts.server.URL,
		site:    ts.site,
		http:    ts.server.Client(),
		isUDM:   ts.isUDM,
		logger:  slog.Default(),
	}
	return c
}

// TestSetAPIKey verifies that setting an API key puts the client into UDM
// mode and that Login becomes a no-op, skipping the HTTP call entirely.
func TestSetAPIKey(t *testing.T) {
	ts := newTestServer(t, true)
	c := ts.client(t)
	c.isUDM = false

	c.SetAPIKey("test-key")
	assert.True(t, c.isUDM)

	err := c.Login(context.Background(), "", "")
	require.NoError(t, err)
	assert.Empty(t, c.csrf, "Login should skip the HTTP call and never set csrf when using an API key")
}

// TestLogin_UDM verifies that UDM-style login sets the CSRF token.
func TestLogin_UDM(t *testing.T) {
	ts := newTestServer(t, true)
	c := ts.client(t)
	c.isUDM = false // reset so Login probes

	err := c.Login(context.Background(), "admin", "secret")
	require.NoError(t, err)
	assert.Equal(t, "test-csrf", c.csrf)
	assert.True(t, c.isUDM)
}

// TestLogin_Classic verifies fallback to the classic login endpoint.
func TestLogin_Classic(t *testing.T) {
	ts := newTestServer(t, false)
	c := ts.client(t)
	c.isUDM = false

	err := c.Login(context.Background(), "admin", "secret")
	require.NoError(t, err)
	assert.False(t, c.isUDM)
}

// TestLogin_BadCredentials verifies that wrong credentials return an error.
func TestLogin_BadCredentials(t *testing.T) {
	ts := newTestServer(t, true)
	c := ts.client(t)
	c.isUDM = false

	err := c.Login(context.Background(), "admin", "wrong")
	require.Error(t, err)
}

// TestGetTags returns only tags from the API.
func TestGetTags(t *testing.T) {
	ts := newTestServer(t, true)
	ts.tags = []Tag{
		{ID: "1", Name: "kuma-servers", MemberTable: "networkdevice", MemberIDs: []string{"AABBCCDDEEFF"}},
		{ID: "2", Name: "kuma-clients", MemberTable: "user", MemberIDs: []string{"111111111111"}},
	}
	c := ts.client(t)

	tags, err := c.GetTags(context.Background())
	require.NoError(t, err)
	assert.Len(t, tags, 2)
	assert.Equal(t, "kuma-servers", tags[0].Name)
}

// TestGetTagsWithPrefix filters by prefix.
func TestGetTagsWithPrefix(t *testing.T) {
	ts := newTestServer(t, true)
	ts.tags = []Tag{
		{ID: "1", Name: "kuma-servers", MemberTable: "networkdevice"},
		{ID: "2", Name: "kuma-clients", MemberTable: "user"},
		{ID: "3", Name: "other-tag", MemberTable: "networkdevice"},
	}
	c := ts.client(t)

	tags, err := c.GetTagsWithPrefix(context.Background(), "kuma")
	require.NoError(t, err)
	assert.Len(t, tags, 2)
	for _, tag := range tags {
		assert.True(t, len(tag.Name) > 0)
		assert.NotEqual(t, "other-tag", tag.Name)
	}
}

// TestGetTagsWithPrefix_ExactMatch allows exact prefix as a tag name.
func TestGetTagsWithPrefix_ExactMatch(t *testing.T) {
	ts := newTestServer(t, true)
	ts.tags = []Tag{
		{ID: "1", Name: "kuma", MemberTable: "networkdevice"},
	}
	c := ts.client(t)

	tags, err := c.GetTagsWithPrefix(context.Background(), "kuma")
	require.NoError(t, err)
	assert.Len(t, tags, 1)
}

// TestTaggedDevices_Devices verifies resolution of tagged infrastructure devices.
func TestTaggedDevices_Devices(t *testing.T) {
	ts := newTestServer(t, true)
	ts.devices = []Device{
		{ID: "d1", MAC: "aa:bb:cc:dd:ee:ff", InformIP: "192.168.1.1", Name: "Gateway"},
		{ID: "d2", MAC: "11:22:33:44:55:66", InformIP: "192.168.1.2", Name: "Switch"},
	}
	ts.tags = []Tag{
		{
			ID:          "t1",
			Name:        "kuma-infra",
			MemberTable: "networkdevice",
			MemberIDs:   []string{"AABBCCDDEEFF", "112233445566"},
		},
	}
	c := ts.client(t)

	result, err := c.TaggedDevices(context.Background(), "kuma")
	require.NoError(t, err)
	assert.Len(t, result, 1)

	devices := result["kuma-infra"]
	assert.Len(t, devices, 2)
	assert.Equal(t, "Gateway", devices[0].Name)
	assert.Equal(t, "192.168.1.1", devices[0].Hostname)
}

// TestTaggedDevices_Clients verifies resolution of tagged clients.
func TestTaggedDevices_Clients(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyLaptop"},
	}
	ts.tags = []Tag{
		{
			ID:          "t1",
			Name:        "kuma-clients",
			MemberTable: "user",
			MemberIDs:   []string{"c1"},
		},
	}
	c := ts.client(t)

	result, err := c.TaggedDevices(context.Background(), "kuma")
	require.NoError(t, err)

	devices := result["kuma-clients"]
	require.Len(t, devices, 1)
	assert.Equal(t, "MyLaptop", devices[0].Name)
	assert.Equal(t, "10.0.0.100", devices[0].Hostname)
}

// TestTaggedDevices_NoTags returns an empty map when no tags match.
func TestTaggedDevices_NoTags(t *testing.T) {
	ts := newTestServer(t, true)
	ts.tags = []Tag{{ID: "1", Name: "other-tag"}}
	c := ts.client(t)

	result, err := c.TaggedDevices(context.Background(), "kuma")
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestTaggedDevices_UnresolvableMember skips members with no matching device.
func TestTaggedDevices_UnresolvableMember(t *testing.T) {
	ts := newTestServer(t, true)
	ts.tags = []Tag{
		{ID: "t1", Name: "kuma-infra", MemberTable: "networkdevice", MemberIDs: []string{"DEADBEEF0000"}},
	}
	c := ts.client(t)

	result, err := c.TaggedDevices(context.Background(), "kuma")
	require.NoError(t, err)
	assert.Empty(t, result["kuma-infra"]) // unresolvable, so nothing added
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"aa:bb:cc:dd:ee:ff", "AABBCCDDEEFF"},
		{"AA:BB:CC:DD:EE:FF", "AABBCCDDEEFF"},
		{"aa-bb-cc-dd-ee-ff", "AABBCCDDEEFF"},
		{"AABBCCDDEEFF", "AABBCCDDEEFF"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeMAC(tt.input))
		})
	}
}

func TestDevice_GetName_Fallback(t *testing.T) {
	d := Device{MAC: "aa:bb:cc:dd:ee:ff"}
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", d.GetName())

	d.Name = "MyDevice"
	assert.Equal(t, "MyDevice", d.GetName())
}

func TestClient_GetName_Fallback(t *testing.T) {
	c := NetworkClient{MAC: "aa:bb:cc:dd:ee:ff"}
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", c.GetName())

	c.Hostname = "my-host"
	assert.Equal(t, "my-host", c.GetName())

	c.Name = "My Client"
	assert.Equal(t, "My Client", c.GetName())
}
