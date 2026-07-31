// Package kuma provides a client for Uptime Kuma. Uptime Kuma has no REST
// API for managing monitors — creating, editing, and deleting monitors (and
// everything else the web UI does) goes over its Socket.IO connection, so
// this wraps github.com/breml/go-uptime-kuma-client rather than talking
// plain HTTP.
package kuma

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	kumaclient "github.com/breml/go-uptime-kuma-client"
	kumamonitor "github.com/breml/go-uptime-kuma-client/monitor"
	kumatag "github.com/breml/go-uptime-kuma-client/tag"
)

const managedByLabel = "unifi-kuma"

const connectTimeout = 30 * time.Second

// defaultPingTimeout is used for every ping monitor's request timeout.
// Kuma's "timeout" column is NOT NULL regardless of monitor type, even
// though the client library types it as optional and the web UI doesn't
// expose it for ping monitors.
const defaultPingTimeout int64 = 48

// kumaConn is the subset of the underlying Socket.IO client's methods that
// Client relies on. Defined as an interface so tests can substitute a fake
// without needing a live Uptime Kuma server; *kumaclient.Client satisfies it.
type kumaConn interface {
	GetMonitors(ctx context.Context) ([]kumamonitor.Base, error)
	CreateMonitor(ctx context.Context, mon kumamonitor.Monitor) (int64, error)
	DeleteMonitor(ctx context.Context, monitorID int64) error
	GetTags(ctx context.Context) ([]kumatag.Tag, error)
	CreateTag(ctx context.Context, t kumatag.Tag) (int64, error)
	AddMonitorTag(ctx context.Context, tagID, monitorID int64, value string) (*kumatag.MonitorTag, error)
	Disconnect() error
}

// Client is an authenticated Uptime Kuma client. The Socket.IO connection is
// established by Login, mirroring the UniFi client's Login-after-construct
// shape even though Kuma's protocol combines connect and auth in one step.
type Client struct {
	baseURL string
	noAuth  bool
	logger  *slog.Logger

	conn kumaConn

	tagMu    sync.Mutex
	tagCache map[string]int64
}

// NewClient creates a new Kuma client targeting the given base URL. The
// connection itself is established lazily by Login.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		logger:   slog.Default().With("component", "kuma"),
		tagCache: make(map[string]int64),
	}
}

// SetNoAuth configures the client for an Uptime Kuma instance running with
// authentication disabled ("Disable Auth" in Settings). Login then connects
// without credentials. Uptime Kuma has no API-key auth for this control
// plane (API keys only cover its push/badge REST endpoints), so this and
// username+password are the only two options.
func (c *Client) SetNoAuth() {
	c.noAuth = true
}

// Login connects to the Uptime Kuma server over Socket.IO and authenticates.
func (c *Client) Login(ctx context.Context, username, password string) error {
	if c.noAuth {
		username, password = "", ""
		c.logger.InfoContext(ctx, "Uptime Kuma auth is disabled, connecting without credentials")
	}

	connection, err := kumaclient.New(ctx, c.baseURL, username, password, kumaclient.WithConnectTimeout(connectTimeout))
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", c.baseURL, err)
	}

	c.conn = connection
	c.logger.InfoContext(ctx, "connected to Uptime Kuma")
	return nil
}

// Close disconnects the underlying Socket.IO connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	if err := c.conn.Disconnect(); err != nil {
		return fmt.Errorf("disconnecting from Uptime Kuma: %w", err)
	}
	return nil
}

// GetMonitors returns all monitors including groups.
func (c *Client) GetMonitors(ctx context.Context) ([]Monitor, error) {
	baseMonitors, err := c.conn.GetMonitors(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching monitors: %w", err)
	}

	monitors := make([]Monitor, 0, len(baseMonitors))
	for _, m := range baseMonitors {
		monitors = append(monitors, toMonitor(m))
	}

	return monitors, nil
}

// GetGroups returns only monitors of type "group".
func (c *Client) GetGroups(ctx context.Context) ([]Monitor, error) {
	all, err := c.GetMonitors(ctx)
	if err != nil {
		return nil, err
	}

	var groups []Monitor
	for _, m := range all {
		if m.Type == MonitorTypeGroup {
			groups = append(groups, m)
		}
	}
	return groups, nil
}

// CreateMonitor creates a new monitor (or group) and returns its assigned ID.
// Tags are created if they don't already exist and associated after the
// monitor itself is created, since Uptime Kuma has no way to set tags as
// part of monitor creation.
func (c *Client) CreateMonitor(ctx context.Context, m Monitor) (int, error) {
	mon, err := fromMonitor(m)
	if err != nil {
		return 0, err
	}

	id64, err := c.conn.CreateMonitor(ctx, mon)
	if err != nil {
		return 0, fmt.Errorf("creating monitor %q: %w", m.Name, err)
	}
	id := int(id64)

	for _, t := range m.Tags {
		tagID, err := c.ensureTag(ctx, t.Name, t.Color)
		if err != nil {
			return id, fmt.Errorf("ensuring tag %q for monitor %q (id %d): %w", t.Name, m.Name, id, err)
		}
		if _, err := c.conn.AddMonitorTag(ctx, tagID, id64, t.Value); err != nil {
			return id, fmt.Errorf("tagging monitor %q (id %d) with %q: %w", m.Name, id, t.Name, err)
		}
	}

	c.logger.InfoContext(ctx, "created monitor", "name", m.Name, "id", id)
	return id, nil
}

// DeleteMonitor removes a monitor by ID.
func (c *Client) DeleteMonitor(ctx context.Context, id int) error {
	if err := c.conn.DeleteMonitor(ctx, int64(id)); err != nil {
		return fmt.Errorf("deleting monitor %d: %w", id, err)
	}

	c.logger.InfoContext(ctx, "deleted monitor", "id", id)
	return nil
}

// FindOrCreateGroup returns the ID of an existing group with the given name,
// creating it if it does not already exist.
func (c *Client) FindOrCreateGroup(ctx context.Context, name string) (int, error) {
	groups, err := c.GetGroups(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing groups: %w", err)
	}

	for _, g := range groups {
		if g.Name == name {
			c.logger.InfoContext(ctx, "found existing group", "name", name, "id", g.ID)
			return g.ID, nil
		}
	}

	id, err := c.CreateMonitor(ctx, Monitor{
		Type:   MonitorTypeGroup,
		Name:   name,
		Active: true,
	})
	if err != nil {
		return 0, fmt.Errorf("creating group %q: %w", name, err)
	}

	return id, nil
}

// FindMonitorByName returns the first monitor with a matching name and parent,
// or nil if not found.
func (c *Client) FindMonitorByName(ctx context.Context, name string, parentID *int) (*Monitor, error) {
	all, err := c.GetMonitors(ctx)
	if err != nil {
		return nil, err
	}

	for i := range all {
		m := &all[i]
		if m.Name != name {
			continue
		}
		if parentID == nil {
			return m, nil
		}
		if m.ParentID != nil && *m.ParentID == *parentID {
			return m, nil
		}
		// A managed monitor with the same name but no parent was likely de-parented.
		// Treat it as found to prevent creating a duplicate.
		if m.ParentID == nil && IsManagedMonitor(*m) {
			return m, nil
		}
	}

	return nil, nil
}

// ManagedLabel returns the tag used to mark monitors created by this tool.
func ManagedLabel() MonitorTag {
	return MonitorTag{Name: managedByLabel, Color: "#4a90d9"}
}

// IsManagedMonitor returns true if the monitor was created by unifi-kuma.
func IsManagedMonitor(m Monitor) bool {
	for _, tag := range m.Tags {
		if tag.Name == managedByLabel {
			return true
		}
	}
	return false
}

// ensureTag returns the ID of an existing tag with the given name, creating
// it if it does not already exist. Results are cached for the life of the
// client since the same managed-by tag is looked up on every monitor create.
func (c *Client) ensureTag(ctx context.Context, name, color string) (int64, error) {
	c.tagMu.Lock()
	defer c.tagMu.Unlock()

	if id, ok := c.tagCache[name]; ok {
		return id, nil
	}

	tags, err := c.conn.GetTags(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing tags: %w", err)
	}
	for _, t := range tags {
		if t.Name == name {
			c.tagCache[name] = t.ID
			return t.ID, nil
		}
	}

	id, err := c.conn.CreateTag(ctx, kumatag.Tag{Name: name, Color: color})
	if err != nil {
		return 0, fmt.Errorf("creating tag %q: %w", name, err)
	}

	c.tagCache[name] = id
	c.logger.InfoContext(ctx, "created tag", "name", name, "id", id)
	return id, nil
}

// toMonitor converts a monitor from the underlying Socket.IO client's
// generic representation into our own domain type.
func toMonitor(m kumamonitor.Base) Monitor {
	var parentID *int
	if m.Parent != nil {
		p := int(*m.Parent)
		parentID = &p
	}

	var description string
	if m.Description != nil {
		description = *m.Description
	}

	tags := make([]MonitorTag, 0, len(m.Tags))
	for _, t := range m.Tags {
		tags = append(tags, MonitorTag{Name: t.Name, Value: t.Value, Color: t.Color})
	}

	return Monitor{
		ID:             int(m.ID),
		Name:           m.Name,
		Type:           MonitorType(m.Type()),
		Interval:       int(m.Interval),
		RetryInterval:  int(m.RetryInterval),
		ResendInterval: int(m.ResendInterval),
		MaxRetries:     int(m.MaxRetries),
		ParentID:       parentID,
		Active:         m.IsActive,
		Description:    description,
		Tags:           tags,
	}
}

// fromMonitor converts our domain type into the concrete monitor type the
// underlying Socket.IO client expects for creation.
func fromMonitor(m Monitor) (kumamonitor.Monitor, error) {
	base := kumamonitor.Base{
		Name:           m.Name,
		Interval:       int64(m.Interval),
		RetryInterval:  int64(m.RetryInterval),
		ResendInterval: int64(m.ResendInterval),
		MaxRetries:     int64(m.MaxRetries),
		IsActive:       m.Active,
	}
	if m.ParentID != nil {
		p := int64(*m.ParentID)
		base.Parent = &p
	}
	if m.Description != "" {
		d := m.Description
		base.Description = &d
	}

	switch m.Type {
	case MonitorTypeGroup:
		return &kumamonitor.Group{Base: base}, nil
	case MonitorTypePing:
		// Timeout is typed as optional (*int64) in the client library, but
		// Kuma's own "timeout" DB column is NOT NULL for every monitor type
		// including ping — a nil pointer marshals to JSON null and the
		// insert is rejected server-side.
		timeout := defaultPingTimeout
		return &kumamonitor.Ping{
			Base:        base,
			PingDetails: kumamonitor.PingDetails{Hostname: m.Hostname, Timeout: &timeout},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported monitor type %q", m.Type)
	}
}
