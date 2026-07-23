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
}

// UniFiConfig holds UniFi controller connection settings.
type UniFiConfig struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Site     string `yaml:"site"`
	Insecure bool   `yaml:"insecure"`
}

// KumaConfig holds Uptime Kuma connection settings.
type KumaConfig struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// SyncConfig holds sync behavior settings.
type SyncConfig struct {
	Interval     time.Duration `yaml:"-"`
	IntervalSecs int           `yaml:"interval_seconds"`
	TagPrefix    string        `yaml:"tag_prefix"`
	DryRun       bool          `yaml:"dry_run"`
	DeleteOrphan bool          `yaml:"delete_orphan"`
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
			IntervalSecs: 300,
			TagPrefix:    "kuma",
		},
	}

	if yamlFile != "" {
		if err := loadYAML(yamlFile, cfg); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)

	cfg.Sync.Interval = time.Duration(cfg.Sync.IntervalSecs) * time.Second

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
	if v := os.Getenv("SYNC_TAG_PREFIX"); v != "" {
		cfg.Sync.TagPrefix = v
	}
	if v := os.Getenv("SYNC_DRY_RUN"); v != "" {
		cfg.Sync.DryRun = parseBool(v)
	}
	if v := os.Getenv("SYNC_DELETE_ORPHAN"); v != "" {
		cfg.Sync.DeleteOrphan = parseBool(v)
	}
}

// Validate returns an error if required fields are missing.
func (c *Config) Validate() error {
	var missing []string

	if c.UniFi.URL == "" {
		missing = append(missing, "UNIFI_URL")
	}
	if c.UniFi.Username == "" {
		missing = append(missing, "UNIFI_USERNAME")
	}
	if c.UniFi.Password == "" {
		missing = append(missing, "UNIFI_PASSWORD")
	}
	if c.Kuma.URL == "" {
		missing = append(missing, "KUMA_URL")
	}
	if c.Kuma.Username == "" {
		missing = append(missing, "KUMA_USERNAME")
	}
	if c.Kuma.Password == "" {
		missing = append(missing, "KUMA_PASSWORD")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	return nil
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes"
}
