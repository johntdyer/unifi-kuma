package kuma

// MonitorType enumerates supported Uptime Kuma monitor types.
type MonitorType string

const (
	MonitorTypeHTTP  MonitorType = "http"
	MonitorTypePing  MonitorType = "ping"
	MonitorTypePort  MonitorType = "port"
	MonitorTypeGroup MonitorType = "group"
)

// Monitor represents an Uptime Kuma monitor or monitor group.
type Monitor struct {
	ID             int
	Name           string
	Type           MonitorType
	Hostname       string
	Interval       int
	RetryInterval  int
	ResendInterval int
	MaxRetries     int
	ParentID       *int
	Active         bool
	Description    string
	Tags           []MonitorTag
}

// MonitorTag is a label attached to a Kuma monitor, used to track
// which monitors were created by unifi-kuma.
type MonitorTag struct {
	Name  string
	Value string
	Color string
}
