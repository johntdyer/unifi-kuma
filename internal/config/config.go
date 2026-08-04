package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	UniFi UniFiConfig `yaml:"unifi"`
	Kuma  KumaConfig  `yaml:"kuma"`
	Sync  SyncConfig  `yaml:"sync"`
	HTTP  HTTPConfig  `yaml:"http"`
}

// UniFiConfig holds UniFi controller connection settings. UniFi API keys are
// not supported: they only work against UniFi's newer public Integrations
// API, which doesn't expose tags — the thing this tool is built around — so
// username+password (the same session-based auth the web UI itself uses) is
// the only viable auth here.
type UniFiConfig struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Site     string `yaml:"site"`
	Insecure bool   `yaml:"insecure"`
}

// KumaConfig holds Uptime Kuma connection settings. Uptime Kuma has no
// API-key auth for the Socket.IO control plane this tool uses (API keys
// only cover its push/badge REST endpoints), so username+password or
// DisableAuth are the only supported options.
type KumaConfig struct {
	URL         string `yaml:"url"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	DisableAuth bool   `yaml:"disable_auth"`
}

// SyncConfig holds sync behavior settings.
type SyncConfig struct {
	Interval           time.Duration `yaml:"-"`
	IntervalSecs       int           `yaml:"interval_seconds"`
	MonitorGroup       string        `yaml:"monitor_group"`
	GroupPrefix        string        `yaml:"group_prefix"`
	HumanizeGroupNames bool          `yaml:"humanize_group_names"`
	TagOtherGroups     bool          `yaml:"tag_other_groups"`
	OtherGroupsColor   string        `yaml:"other_groups_tag_color"`
	DryRun             bool          `yaml:"dry_run"`
	DeleteOrphan       bool          `yaml:"delete_orphan"`
	StaleWarnDays      int           `yaml:"stale_warn_days"`
	StaleWarnAfter     time.Duration `yaml:"-"`
	// MaxOrphanDeletePercent caps what fraction of currently-managed device
	// monitors deleteOrphans is allowed to remove in a single sync cycle,
	// once there are enough of them for a percentage to be meaningful (see
	// minMonitorsForOrphanSafeguard in the sync package). This is a
	// circuit breaker against a misconfiguration or upstream data glitch
	// (e.g. UniFi briefly not reporting the monitor group, or a login
	// issue) silently wiping an entire fleet of monitors in one pass.
	// AllowBulkDelete bypasses it for an intentional mass cleanup.
	MaxOrphanDeletePercent int  `yaml:"max_orphan_delete_percent"`
	AllowBulkDelete        bool `yaml:"allow_bulk_delete"`
}

// HTTPConfig holds settings for the /healthz and /metrics HTTP server.
type HTTPConfig struct {
	// Addr is the listen address for /healthz and /metrics, e.g. ":9090".
	// Empty disables the HTTP server entirely.
	Addr string `yaml:"addr"`
}

// validOtherGroupsColors lists the accepted values for
// SYNC_OTHER_GROUPS_TAG_COLOR, matching the color names unifi-kuma exposes
// (a subset of Uptime Kuma's own tag color palette).
var validOtherGroupsColors = map[string]struct{}{
	"gray":   {},
	"red":    {},
	"orange": {},
	"blue":   {},
	"indigo": {},
	"purple": {},
	"pink":   {},
}

// Load builds a Config from environment variables, with defaults.
// An optional YAML file path can be provided for base configuration
// that env vars will override.
func Load(yamlFile string) (*Config, error) {
	cfg := &Config{
		UniFi: UniFiConfig{
			Site: "default",
		},
		Sync: SyncConfig{
			IntervalSecs:           300,
			MonitorGroup:           "monitor",
			GroupPrefix:            "kuma-group",
			HumanizeGroupNames:     true,
			MaxOrphanDeletePercent: 50,
		},
		HTTP: HTTPConfig{
			Addr: ":9090",
		},
	}

	if yamlFile != "" {
		if err := loadYAML(yamlFile, cfg); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)

	cfg.Sync.Interval = time.Duration(cfg.Sync.IntervalSecs) * time.Second
	cfg.Sync.StaleWarnAfter = time.Duration(cfg.Sync.StaleWarnDays) * 24 * time.Hour
	cfg.Sync.OtherGroupsColor = strings.ToLower(strings.TrimSpace(cfg.Sync.OtherGroupsColor))

	return cfg, cfg.Validate()
}

func loadYAML(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening config file: %w", err)
	}
	defer f.Close()

	if err := yaml.NewDecoder(f).Decode(cfg); err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("UNIFI_URL"); v != "" {
		cfg.UniFi.URL = v
	}
	if v := os.Getenv("UNIFI_USERNAME"); v != "" {
		cfg.UniFi.Username = v
	}
	if v := os.Getenv("UNIFI_PASSWORD"); v != "" {
		cfg.UniFi.Password = v
	}
	if v := os.Getenv("UNIFI_SITE"); v != "" {
		cfg.UniFi.Site = v
	}
	if v := os.Getenv("UNIFI_INSECURE"); v != "" {
		cfg.UniFi.Insecure = parseBool(v)
	}

	if v := os.Getenv("KUMA_URL"); v != "" {
		cfg.Kuma.URL = v
	}
	if v := os.Getenv("KUMA_USERNAME"); v != "" {
		cfg.Kuma.Username = v
	}
	if v := os.Getenv("KUMA_PASSWORD"); v != "" {
		cfg.Kuma.Password = v
	}
	if v := os.Getenv("KUMA_DISABLE_AUTH"); v != "" {
		cfg.Kuma.DisableAuth = parseBool(v)
	}

	if v := os.Getenv("SYNC_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Sync.IntervalSecs = n
		} else {
			slog.Warn("invalid SYNC_INTERVAL_SECONDS value, using default",
				"value", v,
				"default_seconds", cfg.Sync.IntervalSecs,
			)
		}
	}
	if v := os.Getenv("SYNC_MONITOR_GROUP"); v != "" {
		cfg.Sync.MonitorGroup = v
	}
	if v := os.Getenv("SYNC_GROUP_PREFIX"); v != "" {
		cfg.Sync.GroupPrefix = v
	}
	if v := os.Getenv("SYNC_HUMANIZE_GROUP_NAMES"); v != "" {
		cfg.Sync.HumanizeGroupNames = parseBool(v)
	}
	if v := os.Getenv("SYNC_TAG_OTHER_GROUPS"); v != "" {
		cfg.Sync.TagOtherGroups = parseBool(v)
	}
	if v := os.Getenv("SYNC_OTHER_GROUPS_TAG_COLOR"); v != "" {
		cfg.Sync.OtherGroupsColor = v
	}
	if v := os.Getenv("SYNC_DRY_RUN"); v != "" {
		cfg.Sync.DryRun = parseBool(v)
	}
	if v := os.Getenv("SYNC_DELETE_ORPHAN"); v != "" {
		cfg.Sync.DeleteOrphan = parseBool(v)
	}
	if v := os.Getenv("SYNC_STALE_WARN_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Sync.StaleWarnDays = n
		} else {
			slog.Warn("invalid SYNC_STALE_WARN_DAYS value, using default",
				"value", v,
				"default_days", cfg.Sync.StaleWarnDays,
			)
		}
	}
	if v := os.Getenv("SYNC_MAX_ORPHAN_DELETE_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100 {
			cfg.Sync.MaxOrphanDeletePercent = n
		} else {
			slog.Warn("invalid SYNC_MAX_ORPHAN_DELETE_PERCENT value, must be 1-100, using default",
				"value", v,
				"default_percent", cfg.Sync.MaxOrphanDeletePercent,
			)
		}
	}
	if v := os.Getenv("SYNC_ALLOW_BULK_DELETE"); v != "" {
		cfg.Sync.AllowBulkDelete = parseBool(v)
	}

	if v, ok := os.LookupEnv("HTTP_ADDR"); ok {
		cfg.HTTP.Addr = v
	}
}

// Validate returns an error if required fields are missing. Both UniFi and
// Kuma require a username+password pair; Kuma alone also accepts
// DisableAuth in place of credentials.
func (c *Config) Validate() error {
	var errs []string

	if c.UniFi.URL == "" {
		errs = append(errs, "UNIFI_URL")
	}
	if c.UniFi.Username == "" || c.UniFi.Password == "" {
		errs = append(errs, "UniFi requires both UNIFI_USERNAME and UNIFI_PASSWORD")
	}
	if c.Kuma.URL == "" {
		errs = append(errs, "KUMA_URL")
	}
	if !c.Kuma.DisableAuth && (c.Kuma.Username == "" || c.Kuma.Password == "") {
		errs = append(errs, "Kuma requires both KUMA_USERNAME and KUMA_PASSWORD, or KUMA_DISABLE_AUTH=true")
	}
	if c.Sync.OtherGroupsColor != "" {
		if _, ok := validOtherGroupsColors[c.Sync.OtherGroupsColor]; !ok {
			errs = append(errs, fmt.Sprintf(
				"SYNC_OTHER_GROUPS_TAG_COLOR must be one of gray, red, orange, blue, indigo, purple, pink (got %q)",
				c.Sync.OtherGroupsColor,
			))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(errs, "; "))
	}

	return nil
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes"
}
