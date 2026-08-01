package unifi

import "strings"

// loginRequest is sent to the UniFi auth endpoint.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// apiResponse is the standard envelope for UniFi API v1 responses.
type apiResponse[T any] struct {
	Meta struct {
		RC      string `json:"rc"`
		Message string `json:"msg"`
	} `json:"meta"`
	Data []T `json:"data"`
}

// Group represents a UniFi "network members group" — shown as "Groups" in
// the UI — a named, user-managed collection of devices/clients identified
// by MAC address.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // "CLIENTS" or "DEVICES"
	Members []string `json:"members"`
}

// Device represents a UniFi infrastructure device (AP, switch, gateway).
type Device struct {
	ID       string `json:"_id"`
	MAC      string `json:"mac"`
	InformIP string `json:"inform_ip"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Type     string `json:"type"`
	Adopted  bool   `json:"adopted"`
	Version  string `json:"version"`
}

// NetworkClient represents a UniFi network client (end-user device). UniFi
// only populates ip while the client is actively connected; last_ip is
// always present (the most recently observed address), so a client that
// isn't live right now still resolves to a usable ping target.
type NetworkClient struct {
	ID       string `json:"_id"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	LastIP   string `json:"last_ip"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
}

// MonitorableDevice combines the Kuma group it should land in with the
// resolved host info needed to create a monitor.
type MonitorableDevice struct {
	GroupName string
	Name      string
	Hostname  string // IP or DNS name to monitor
	MAC       string
	// SourceGroupID is the stable UniFi ID of the "{groupPrefix}-{name}"
	// group that produced GroupName, or "" for the synthetic "Ungrouped"
	// bucket. Lets the syncer recognize the same Kuma group across a rename
	// in UniFi instead of matching on the (now stale) name alone.
	SourceGroupID string
	// OtherGroups lists every other UniFi group this device belongs to,
	// besides the monitor flag group and any group matching groupPrefix
	// (those are already represented structurally via GroupName). Sorted
	// for a deterministic tag order.
	OtherGroups []string
}

// normalizeMAC strips colons/dashes and uppercases a MAC address so that
// different formatting styles can be compared reliably.
func normalizeMAC(mac string) string {
	mac = strings.ReplaceAll(mac, ":", "")
	mac = strings.ReplaceAll(mac, "-", "")
	return strings.ToUpper(mac)
}

// GetIP returns the best available IP for a Device.
func (d Device) GetIP() string {
	return d.InformIP
}

// GetName returns a display name for the Device, falling back to MAC.
func (d Device) GetName() string {
	if d.Name != "" {
		return d.Name
	}
	return d.MAC
}

// GetIP returns the best available IP for a NetworkClient, preferring the
// live ip and falling back to the last known address if it isn't currently
// connected.
func (c NetworkClient) GetIP() string {
	if c.IP != "" {
		return c.IP
	}
	return c.LastIP
}

// GetName returns a display name for the NetworkClient, falling back to hostname then MAC.
func (c NetworkClient) GetName() string {
	if c.Name != "" {
		return c.Name
	}
	if c.Hostname != "" {
		return c.Hostname
	}
	return c.MAC
}
