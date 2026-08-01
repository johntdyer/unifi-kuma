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
	groups  []Group
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

	ts.mux.HandleFunc("/v2/api/site/default/network-members-groups", ts.handleGroups)
	ts.mux.HandleFunc("/proxy/network/v2/api/site/default/network-members-groups", ts.handleGroups)

	ts.mux.HandleFunc("/api/s/default/stat/device", ts.handleDevices)
	ts.mux.HandleFunc("/proxy/network/api/s/default/stat/device", ts.handleDevices)

	ts.mux.HandleFunc("/api/s/default/rest/user", ts.handleClients)
	ts.mux.HandleFunc("/proxy/network/api/s/default/rest/user", ts.handleClients)

	return ts
}

// handleGroups returns a bare JSON array, matching the real
// network-members-groups API (no meta/data envelope).
func (ts *testServer) handleGroups(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(ts.groups)
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

// TestGetGroups returns all groups from the API.
func TestGetGroups(t *testing.T) {
	ts := newTestServer(t, true)
	ts.groups = []Group{
		{ID: "1", Name: "monitor", Type: "CLIENTS", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "2", Name: "servers", Type: "CLIENTS", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	groups, err := c.GetGroups(context.Background())
	require.NoError(t, err)
	assert.Len(t, groups, 2)
	assert.Equal(t, "monitor", groups[0].Name)
}

// TestMonitorableDevices_Devices verifies resolution against infrastructure devices.
func TestMonitorableDevices_Devices(t *testing.T) {
	ts := newTestServer(t, true)
	ts.devices = []Device{
		{ID: "d1", MAC: "aa:bb:cc:dd:ee:ff", InformIP: "192.168.1.1", Name: "Gateway"},
		{ID: "d2", MAC: "11:22:33:44:55:66", InformIP: "192.168.1.2", Name: "Switch"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	require.Len(t, result, 1)

	devices := result["servers"]
	require.Len(t, devices, 2)
	assert.Equal(t, "Gateway", devices[0].Name)
	assert.Equal(t, "192.168.1.1", devices[0].Hostname)
}

// TestMonitorableDevices_Clients verifies resolution against network clients.
func TestMonitorableDevices_Clients(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyLaptop"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-iot", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["iot"]
	require.Len(t, devices, 1)
	assert.Equal(t, "MyLaptop", devices[0].Name)
	assert.Equal(t, "10.0.0.100", devices[0].Hostname)
}

// TestMonitorableDevices_OfflineClient verifies a client with no live "ip"
// (only "last_ip", as UniFi reports for anything not currently connected)
// still resolves rather than being skipped.
func TestMonitorableDevices_OfflineClient(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "58:97:bd:8f:66:9a", LastIP: "10.222.222.11", Name: "server-proxmox2"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"58:97:bd:8f:66:9a"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"58:97:bd:8f:66:9a"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["servers"]
	require.Len(t, devices, 1)
	assert.Equal(t, "server-proxmox2", devices[0].Name)
	assert.Equal(t, "10.222.222.11", devices[0].Hostname)
}

// TestMonitorableDevices_Ungrouped puts members of the flag group with no
// matching Kuma-destination group under "Ungrouped".
func TestMonitorableDevices_Ungrouped(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "Loner"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["Ungrouped"]
	require.Len(t, devices, 1)
	assert.Equal(t, "Loner", devices[0].Name)
}

// TestMonitorableDevices_IgnoresUnprefixedGroups verifies that a UniFi group
// used for something unrelated (firewall rules, VLANs, etc.) that doesn't
// carry the configured prefix is not treated as a Kuma-destination group —
// its members land in "Ungrouped" instead.
func TestMonitorableDevices_IgnoresUnprefixedGroups(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyPhone"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "apple", Members: []string{"aa:bb:cc:dd:ee:ff"}}, // no "kuma-group-" prefix
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Empty(t, result["apple"])
	assert.Len(t, result["Ungrouped"], 1)
}

// TestMonitorableDevices_MultipleGroups fans a member out into every matching
// Kuma-destination group it belongs to.
func TestMonitorableDevices_MultipleGroups(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MultiHomed"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g3", Name: "kuma-group-iot", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Len(t, result["servers"], 1)
	assert.Len(t, result["iot"], 1)
}

// TestMonitorableDevices_SourceGroupID verifies each device carries the
// stable UniFi ID of the prefixed group that placed it in its destination
// Kuma group, so the syncer can recognize the same group again even if it's
// later renamed in UniFi.
func TestMonitorableDevices_SourceGroupID(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyPhone"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["servers"]
	require.Len(t, devices, 1)
	assert.Equal(t, "g2", devices[0].SourceGroupID)
}

// TestMonitorableDevices_Ungrouped_NoSourceGroupID verifies a device with no
// matching prefixed group (landing under "Ungrouped") has no SourceGroupID,
// since there's no single UniFi group behind that synthetic bucket.
func TestMonitorableDevices_Ungrouped_NoSourceGroupID(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyPhone"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["Ungrouped"]
	require.Len(t, devices, 1)
	assert.Equal(t, "", devices[0].SourceGroupID)
}

// TestMonitorableDevices_OtherGroups verifies that arbitrary UniFi groups
// (neither the flag group nor a prefixed destination group) are surfaced as
// OtherGroups, while the flag group and prefixed groups are excluded from
// it since they're already represented structurally.
func TestMonitorableDevices_OtherGroups(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyPhone"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-media", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g3", Name: "apple", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["media"]
	require.Len(t, devices, 1)
	assert.Equal(t, []string{"apple"}, devices[0].OtherGroups)
}

// TestMonitorableDevices_OtherGroups_MultipleSorted verifies OtherGroups is
// sorted for a deterministic tag order regardless of more than one match.
func TestMonitorableDevices_OtherGroups_MultipleSorted(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyPhone"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "zebra", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g3", Name: "apple", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)

	devices := result["Ungrouped"]
	require.Len(t, devices, 1)
	assert.Equal(t, []string{"apple", "zebra"}, devices[0].OtherGroups)
}

// TestMonitorableDevices_DedupesFlagGroupMembers verifies a MAC listed
// twice in the flag group's own member list only produces one entry —
// otherwise it would pass the "already exists" check twice against the
// same stale-within-cycle data and create a duplicate monitor.
func TestMonitorableDevices_DedupesFlagGroupMembers(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyLaptop"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	assert.Len(t, result["servers"], 1)
}

// TestMonitorableDevices_DedupesDestinationGroupMembers verifies a MAC
// listed twice in a single Kuma-destination group's member list doesn't
// produce two entries for that device under the same Kuma group.
func TestMonitorableDevices_DedupesDestinationGroupMembers(t *testing.T) {
	ts := newTestServer(t, true)
	ts.clients = []NetworkClient{
		{ID: "c1", MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.100", Name: "MyLaptop"},
	}
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"aa:bb:cc:dd:ee:ff"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	assert.Len(t, result["servers"], 1)
}

// TestMonitorableDevices_NoFlagGroup returns an empty map when the
// configured monitor group doesn't exist, rather than erroring.
func TestMonitorableDevices_NoFlagGroup(t *testing.T) {
	ts := newTestServer(t, true)
	ts.groups = []Group{{ID: "1", Name: "kuma-group-servers", Members: []string{"aa:bb:cc:dd:ee:ff"}}}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestMonitorableDevices_UnresolvableMember skips members with no matching device/client.
func TestMonitorableDevices_UnresolvableMember(t *testing.T) {
	ts := newTestServer(t, true)
	ts.groups = []Group{
		{ID: "g1", Name: "monitor", Members: []string{"de:ad:be:ef:00:00"}},
		{ID: "g2", Name: "kuma-group-servers", Members: []string{"de:ad:be:ef:00:00"}},
	}
	c := ts.client(t)

	result, err := c.MonitorableDevices(context.Background(), "monitor", "kuma-group")
	require.NoError(t, err)
	assert.Empty(t, result["servers"]) // unresolvable, so nothing added
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

func TestClient_GetIP_FallsBackToLastIP(t *testing.T) {
	c := NetworkClient{LastIP: "10.0.0.5"}
	assert.Equal(t, "10.0.0.5", c.GetIP(), "should fall back to last_ip when not currently connected")

	c.IP = "10.0.0.6"
	assert.Equal(t, "10.0.0.6", c.GetIP(), "live ip should take precedence over last_ip")
}

func TestClient_GetName_Fallback(t *testing.T) {
	c := NetworkClient{MAC: "aa:bb:cc:dd:ee:ff"}
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", c.GetName())

	c.Hostname = "my-host"
	assert.Equal(t, "my-host", c.GetName())

	c.Name = "My Client"
	assert.Equal(t, "My Client", c.GetName())
}
