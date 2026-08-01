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
	"sort"
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
//
// UniFi API keys are not supported: they only work against UniFi's newer
// public Integrations API, which doesn't expose tags — the thing this tool
// is built around — so username+password (the same session-based auth the
// web UI itself uses) is the only viable auth for reading tags.
func (c *Client) Login(ctx context.Context, username, password string) error {
	body, err := json.Marshal(loginRequest{Username: username, Password: password, Remember: true})
	if err != nil {
		return fmt.Errorf("marshaling login request: %w", err)
	}

	// Try UniFi OS (UDM) endpoint first.
	url := c.baseURL + "/api/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building login request to %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending login request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Guard against proxy/captive portal intercepts that return 200 with non-API bodies.
		if len(resp.Cookies()) == 0 && resp.Header.Get("X-Csrf-Token") == "" {
			return fmt.Errorf("UniFi OS login to %s returned 200 but no session cookie or CSRF token — possible proxy intercept", url)
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
		return fmt.Errorf("UniFi OS login to %s returned status %d", url, resp.StatusCode)
	}
}

func (c *Client) loginClassic(ctx context.Context, username, password string) error {
	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		return fmt.Errorf("marshaling classic login request: %w", err)
	}

	url := c.baseURL + "/api/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building classic login request to %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending classic login request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("classic login to %s returned status %d", url, resp.StatusCode)
	}

	c.isUDM = false
	c.logger.InfoContext(ctx, "logged in (classic)")
	return nil
}

// GetGroups returns all "network members groups" (shown as "Groups" in the
// UI) from the UniFi site.
func (c *Client) GetGroups(ctx context.Context) ([]Group, error) {
	url := c.v2URL("/network-members-groups")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request to %s: %w", url, err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned status %d", url, resp.StatusCode)
	}

	var groups []Group
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("decoding groups response: %w", err)
	}

	return groups, nil
}

// GetDevices returns all adopted UniFi infrastructure devices (APs, switches, gateways).
func (c *Client) GetDevices(ctx context.Context) ([]Device, error) {
	return fetchV1List[Device](ctx, c, "/stat/device")
}

// GetClients returns all known network clients.
func (c *Client) GetClients(ctx context.Context) ([]NetworkClient, error) {
	return fetchV1List[NetworkClient](ctx, c, "/rest/user")
}

// MonitorableDevices returns a map of Kuma group name → devices to monitor.
// Membership in the group named monitorGroup marks a device/client as
// something to monitor. Which Kuma group its monitor lands in is decided
// only by membership in other UniFi groups named "{groupPrefix}-{name}" —
// arbitrary UniFi groups used for unrelated purposes (firewall rules, VLAN
// assignment, etc.) are ignored unless they carry that prefix. A member of
// monitorGroup with no matching group is placed under "Ungrouped". A member
// of more than one matching group gets an entry — and so a monitor — under
// each one.
func (c *Client) MonitorableDevices(ctx context.Context, monitorGroup, groupPrefix string) (map[string][]MonitorableDevice, error) {
	groups, err := c.GetGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching groups: %w", err)
	}

	prefixDash := groupPrefix + "-"

	var flagGroup *Group
	// Index: member MAC -> names of the Kuma-destination groups it belongs to.
	// Deduplicated per MAC so a stray duplicate entry in a UniFi group's
	// member list (or two prefixed groups resolving to the same name) can't
	// produce a duplicate Kuma monitor.
	memberGroups := make(map[string]map[string]struct{})
	// Index: Kuma-destination group name -> the UniFi group ID that produced
	// it, so the syncer can recognize the same group again after a rename.
	groupSourceIDs := make(map[string]string)
	// Index: member MAC -> names of every other (non-flag, non-prefixed)
	// UniFi group it belongs to — arbitrary groups reused for unrelated
	// purposes (firewall rules, VLANs, etc.), exposed as MonitorableDevice
	// data so the syncer can optionally surface them as Kuma tags.
	memberOtherGroups := make(map[string]map[string]struct{})
	for _, g := range groups {
		if strings.EqualFold(g.Name, monitorGroup) {
			gg := g
			flagGroup = &gg
			continue
		}
		if strings.HasPrefix(strings.ToLower(g.Name), strings.ToLower(prefixDash)) {
			kumaGroupName := g.Name[len(prefixDash):]
			groupSourceIDs[kumaGroupName] = g.ID
			for _, mac := range g.Members {
				key := normalizeMAC(mac)
				if memberGroups[key] == nil {
					memberGroups[key] = make(map[string]struct{})
				}
				memberGroups[key][kumaGroupName] = struct{}{}
			}
			continue
		}
		for _, mac := range g.Members {
			key := normalizeMAC(mac)
			if memberOtherGroups[key] == nil {
				memberOtherGroups[key] = make(map[string]struct{})
			}
			memberOtherGroups[key][g.Name] = struct{}{}
		}
	}

	if flagGroup == nil {
		c.logger.WarnContext(ctx, "monitor group not found", "group", monitorGroup)
		return map[string][]MonitorableDevice{}, nil
	}

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

	clientByMAC := make(map[string]NetworkClient, len(clients))
	for _, cl := range clients {
		clientByMAC[normalizeMAC(cl.MAC)] = cl
	}

	const ungrouped = "Ungrouped"

	result := make(map[string][]MonitorableDevice)
	seen := make(map[string]struct{}, len(flagGroup.Members))
	for _, mac := range flagGroup.Members {
		key := normalizeMAC(mac)
		if _, dup := seen[key]; dup {
			continue // duplicate entry in the group's own member list
		}
		seen[key] = struct{}{}

		name, hostname, ok := resolveMember(key, deviceByMAC, clientByMAC)
		if !ok {
			c.logger.WarnContext(ctx, "could not resolve group member",
				"group", monitorGroup,
				"mac", mac,
			)
			continue
		}

		groupNames := memberGroups[key]
		if len(groupNames) == 0 {
			groupNames = map[string]struct{}{ungrouped: {}}
		}

		var otherGroups []string
		for gn := range memberOtherGroups[key] {
			otherGroups = append(otherGroups, gn)
		}
		sort.Strings(otherGroups)

		for gn := range groupNames {
			result[gn] = append(result[gn], MonitorableDevice{
				GroupName:     gn,
				Name:          name,
				Hostname:      hostname,
				MAC:           mac,
				SourceGroupID: groupSourceIDs[gn],
				OtherGroups:   otherGroups,
			})
		}
	}

	for gn, devs := range result {
		c.logger.InfoContext(ctx, "resolved group members", "group", gn, "count", len(devs))
	}

	return result, nil
}

// resolveMember finds a device or client matching the given normalized MAC.
func resolveMember(
	mac string,
	deviceByMAC map[string]Device,
	clientByMAC map[string]NetworkClient,
) (name, hostname string, ok bool) {
	if d, found := deviceByMAC[mac]; found {
		return d.GetName(), d.GetIP(), d.GetIP() != ""
	}
	if cl, found := clientByMAC[mac]; found {
		return cl.GetName(), cl.GetIP(), cl.GetIP() != ""
	}
	return "", "", false
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
	url := c.v1URL(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request to %s: %w", url, err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s returned status %d: %s", url, resp.StatusCode, string(body))
	}

	var result apiResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response from %s: %w", url, err)
	}

	if result.Meta.RC != "" && result.Meta.RC != "ok" {
		return nil, fmt.Errorf("%s API error: %s", url, result.Meta.Message)
	}

	return result.Data, nil
}
