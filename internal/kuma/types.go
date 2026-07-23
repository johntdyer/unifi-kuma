package kuma

// loginRequest is the body sent to Uptime Kuma's auth endpoint.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse contains the JWT token returned on successful auth.
type loginResponse struct {
	TokenType string `json:"tokenType"`
	Token     string `json:"token"`
}

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
	ID             int          `json:"id,omitempty"`
	Name           string       `json:"name"`
	Type           MonitorType  `json:"type"`
	Hostname       string       `json:"hostname,omitempty"`
	URL            string       `json:"url,omitempty"`
	Port           int          `json:"port,omitempty"`
	Interval       int          `json:"interval,omitempty"`
	RetryInterval  int          `json:"retryInterval,omitempty"`
	ResendInterval int          `json:"resendInterval,omitempty"`
	MaxRetries     int          `json:"maxretries,omitempty"`
	ParentID       *int         `json:"parent,omitempty"`
	Active         bool         `json:"active"`
	Description    string       `json:"description,omitempty"`
	Tags           []MonitorTag `json:"tags,omitempty"`
}

// MonitorTag is a label attached to a Kuma monitor, used to track
// which monitors were created by unifi-kuma.
type MonitorTag struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Color string `json:"color,omitempty"`
}

// createResponse is returned when a monitor is successfully created.
type createResponse struct {
	OK        bool    `json:"ok"`
	Msg       string  `json:"msg"`
	MonitorID int     `json:"monitorID"`
	Monitor   Monitor `json:"monitor"`
}

// listResponse is returned by the list monitors endpoint.
type listResponse struct {
	OK       bool      `json:"ok"`
	Monitors []Monitor `json:"monitors"`
}

// errResponse wraps an error message from the Kuma API.
type errResponse struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg"`
}
