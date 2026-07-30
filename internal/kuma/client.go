// Package kuma provides a client for the Uptime Kuma REST API (v1.23+).
package kuma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const managedByLabel = "unifi-kuma"

// Client is an authenticated Uptime Kuma API client.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
	noAuth  bool
	logger  *slog.Logger
}

// NewClient creates a new Kuma client targeting the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: slog.Default().With("component", "kuma"),
	}
}

// SetAPIKey configures the client to authenticate with a static API key instead
// of username/password. Call this before Start; Login becomes a no-op when set.
func (c *Client) SetAPIKey(key string) {
	c.token = key
}

// SetNoAuth configures the client for an Uptime Kuma instance running with
// authentication disabled ("Disable Auth" in Settings). Login becomes a no-op
// and requests are sent without an Authorization header.
func (c *Client) SetNoAuth() {
	c.noAuth = true
}

// Login authenticates and stores the JWT token for subsequent requests.
// If an API key was set via SetAPIKey, or the client was configured via
// SetNoAuth, the HTTP call is skipped.
func (c *Client) Login(ctx context.Context, username, password string) error {
	if c.noAuth {
		c.logger.InfoContext(ctx, "Uptime Kuma auth is disabled, skipping login")
		return nil
	}
	if c.token != "" {
		c.logger.InfoContext(ctx, "using API key for Uptime Kuma")
		return nil
	}

	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		return fmt.Errorf("marshaling login: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login/access-token", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login returned status %d: %s", resp.StatusCode, string(body))
	}

	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return fmt.Errorf("decoding login response: %w", err)
	}

	if lr.Token == "" {
		return fmt.Errorf("login response contained no token")
	}

	c.token = lr.Token
	c.logger.InfoContext(ctx, "logged in to Uptime Kuma")
	return nil
}

// GetMonitors returns all monitors including groups.
func (c *Client) GetMonitors(ctx context.Context) ([]Monitor, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/monitors", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching monitors: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get monitors returned status %d", resp.StatusCode)
	}

	var result listResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding monitors: %w", err)
	}

	return result.Monitors, nil
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

// CreateMonitor creates a new monitor and returns its assigned ID.
func (c *Client) CreateMonitor(ctx context.Context, m Monitor) (int, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return 0, fmt.Errorf("marshaling monitor: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/monitors", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("creating monitor: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading create response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("create monitor returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result createResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("decoding create response: %w", err)
	}

	if !result.OK {
		return 0, fmt.Errorf("create monitor failed: %s", result.Msg)
	}

	c.logger.InfoContext(ctx, "created monitor", "name", m.Name, "id", result.MonitorID)
	return result.MonitorID, nil
}

// DeleteMonitor removes a monitor by ID.
func (c *Client) DeleteMonitor(ctx context.Context, id int) error {
	req, err := c.newRequest(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/monitors/%d", id), nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("deleting monitor %d: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete monitor %d returned status %d", id, resp.StatusCode)
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

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}
