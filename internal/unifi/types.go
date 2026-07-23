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

// Tag represents a UniFi network tag.
type Tag struct {
	ID          string   `json:"_id"`
	Name        string   `json:"name"`
	SiteID      string   `json:"site_id"`
	MemberTable string   `json:"member_table"` // "networkdevice" or "user"
	MemberIDs   []string `json:"member_ids"`
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

// NetworkClient represents a UniFi network client (end-user device).
type NetworkClient struct {
	ID       string `json:"_id"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
}

// TaggedDevice combines tag context with the resolved host info needed
// to create a monitor.
type TaggedDevice struct {
	TagName  string
	Name     string
	Hostname string // IP or DNS name to monitor
	MAC      string
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

// GetIP returns the IP for a NetworkClient.
func (c NetworkClient) GetIP() string {
	return c.IP
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
