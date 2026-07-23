// Package unifi provides a client for the UniFi Network API.
// It supports both UniFi OS (UDM/UNVR) and the self-hosted Network Application.
package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// Client is an authenticated UniFi API client.
type Client struct {
	baseURL string
	site    string
	http    *http.Client
	csrf    string
	isUDM   bool
	logger  *slog.Logger
}

// NewClient creates a new UniFi client. Set insecure=true to skip TLS
// certificate verification (useful for self-signed certs).
func NewClient(baseURL, site string, insecure bool) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		site:    site,
		http: &http.Client{
			Jar:       jar,
			Transport: transport,
			Timeout:   30 * time.Second,
		},
		logger: slog.Default().With("component", "unifi"),
	}, nil
}

// Login authenticates to the UniFi controller. It tries UniFi OS style first,
// then falls back to the classic Network Application login.
func (c *Client) Login(ctx context.Context, username, password string) error {
	body, err := json.Marshal(loginRequest{Username: username, Password: password, Remember: true})
	if err != nil {
		return fmt.Errorf("marshaling login request: %w", err)
	}

	// Try UniFi OS (UDM) endpoint first.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending login request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Guard against proxy/captive portal intercepts that return 200 with non-API bodies.
		if len(resp.Cookies()) == 0 && resp.Header.Get("X-Csrf-Token") == "" {
			return fmt.Errorf("UniFi OS login returned 200 but no session cookie or CSRF token — possible proxy intercept")
		}
		c.isUDM = true
		if token := resp.Header.Get("X-Csrf-Token"); token != "" {
			c.csrf = token
		}
		c.logger.InfoContext(ctx, "logged in (UniFi OS)")
		return nil
	case http.StatusNotFound:
		// Fall back to classic login.
		return c.loginClassic(ctx, username, password)
	default:
		return fmt.Errorf("UniFi OS login returned status %d", resp.StatusCode)
	}
}

func (c *Client) loginClassic(ctx context.Context, username, password string) error {
	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		return fmt.Errorf("marshaling classic login request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building classic login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending classic login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("classic login returned status %d", resp.StatusCode)
	}

	c.isUDM = false
	c.logger.InfoContext(ctx, "logged in (classic)")
	return nil
}

// GetTags returns all tags from the UniFi site. Requires UniFi OS 3+ or
// UniFi Network Application 8+.
func (c *Client) GetTags(ctx context.Context) ([]Tag, error) {
	url := c.v2URL("/tag")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building tags request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tags API returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []Tag `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding tags response: %w", err)
	}

	return result.Data, nil
}

// GetTagsWithPrefix returns tags whose names start with prefix followed by "-".
func (c *Client) GetTagsWithPrefix(ctx context.Context, prefix string) ([]Tag, error) {
	tags, err := c.GetTags(ctx)
	if err != nil {
		return nil, err
	}

	pattern := prefix + "-"
	var matched []Tag
	for _, t := range tags {
		if strings.HasPrefix(t.Name, pattern) || t.Name == prefix {
			matched = append(matched, t)
		}
	}

	c.logger.InfoContext(ctx, "tags matched prefix",
		"prefix", prefix,
		"total", len(tags),
		"matched", len(matched),
	)
	return matched, nil
}

// GetDevices returns all adopted UniFi infrastructure devices (APs, switches, gateways).
func (c *Client) GetDevices(ctx context.Context) ([]Device, error) {
	return fetchV1List[Device](ctx, c, "/stat/device")
}

// GetClients returns all known network clients.
func (c *Client) GetClients(ctx context.Context) ([]NetworkClient, error) {
	return fetchV1List[NetworkClient](ctx, c, "/rest/user")
}

// TaggedDevices returns a map of tag name → slice of devices to monitor,
// filtered by tags that match the given prefix.
func (c *Client) TaggedDevices(ctx context.Context, prefix string) (map[string][]TaggedDevice, error) {
	tags, err := c.GetTagsWithPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("fetching tags: %w", err)
	}

	if len(tags) == 0 {
		return map[string][]TaggedDevice{}, nil
	}

	// Build lookup indices for fast member resolution.
	devices, err := c.GetDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching devices: %w", err)
	}

	clients, err := c.GetClients(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching clients: %w", err)
	}

	deviceByMAC := make(map[string]Device, len(devices))
	for _, d := range devices {
		deviceByMAC[normalizeMAC(d.MAC)] = d
	}

	clientByID := make(map[string]NetworkClient, len(clients))
	clientByMAC := make(map[string]NetworkClient, len(clients))
	for _, cl := range clients {
		clientByID[cl.ID] = cl
		clientByMAC[normalizeMAC(cl.MAC)] = cl
	}

	result := make(map[string][]TaggedDevice, len(tags))
	for _, tag := range tags {
		var tagged []TaggedDevice

		for _, memberID := range tag.MemberIDs {
			td, ok := resolveDevice(memberID, tag.MemberTable, deviceByMAC, clientByID, clientByMAC)
			if !ok {
				c.logger.WarnContext(ctx, "could not resolve tag member",
					"tag", tag.Name,
					"member_id", memberID,
				)
				continue
			}
			td.TagName = tag.Name
			tagged = append(tagged, td)
		}

		result[tag.Name] = tagged
		c.logger.InfoContext(ctx, "resolved tag members",
			"tag", tag.Name,
			"count", len(tagged),
		)
	}

	return result, nil
}

// resolveDevice finds a device or client matching the given member ID,
// trying MAC normalization and direct ID lookup.
func resolveDevice(
	memberID, memberTable string,
	deviceByMAC map[string]Device,
	clientByID map[string]NetworkClient,
	clientByMAC map[string]NetworkClient,
) (TaggedDevice, bool) {
	normID := normalizeMAC(memberID)

	switch memberTable {
	case "networkdevice":
		if d, ok := deviceByMAC[normID]; ok {
			return TaggedDevice{
				Name:     d.GetName(),
				Hostname: d.GetIP(),
				MAC:      d.MAC,
			}, d.GetIP() != ""
		}
	case "user":
		// Try by document ID first, then by MAC.
		if cl, ok := clientByID[memberID]; ok {
			return TaggedDevice{
				Name:     cl.GetName(),
				Hostname: cl.GetIP(),
				MAC:      cl.MAC,
			}, cl.GetIP() != ""
		}
		if cl, ok := clientByMAC[normID]; ok {
			return TaggedDevice{
				Name:     cl.GetName(),
				Hostname: cl.GetIP(),
				MAC:      cl.MAC,
			}, cl.GetIP() != ""
		}
	default:
		// Try both indices as a fallback.
		if d, ok := deviceByMAC[normID]; ok {
			return TaggedDevice{
				Name:     d.GetName(),
				Hostname: d.GetIP(),
				MAC:      d.MAC,
			}, d.GetIP() != ""
		}
	}

	return TaggedDevice{}, false
}

// v2URL builds an endpoint URL for the UniFi v2 API.
func (c *Client) v2URL(path string) string {
	if c.isUDM {
		return fmt.Sprintf("%s/proxy/network/v2/api/site/%s%s", c.baseURL, c.site, path)
	}
	return fmt.Sprintf("%s/v2/api/site/%s%s", c.baseURL, c.site, path)
}

// v1URL builds an endpoint URL for the UniFi v1 API.
func (c *Client) v1URL(path string) string {
	if c.isUDM {
		return fmt.Sprintf("%s/proxy/network/api/s/%s%s", c.baseURL, c.site, path)
	}
	return fmt.Sprintf("%s/api/s/%s%s", c.baseURL, c.site, path)
}

func (c *Client) setHeaders(req *http.Request) {
	if c.csrf != "" {
		req.Header.Set("X-Csrf-Token", c.csrf)
	}
}

// fetchV1List performs a GET against a v1 API path and decodes the data array.
func fetchV1List[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.v1URL(path), nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", path, err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s returned status %d: %s", path, resp.StatusCode, string(body))
	}

	var result apiResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response for %s: %w", path, err)
	}

	if result.Meta.RC != "" && result.Meta.RC != "ok" {
		return nil, fmt.Errorf("%s API error: %s", path, result.Meta.Message)
	}

	return result.Data, nil
}
