package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_EnvVars(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":      "https://unifi.example.com",
		"UNIFI_USERNAME": "admin",
		"UNIFI_PASSWORD": "secret",
		"UNIFI_SITE":     "mysite",
		"KUMA_URL":       "http://kuma.example.com:3001",
		"KUMA_USERNAME":  "kuma",
		"KUMA_PASSWORD":  "kumasecret",
	})

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "https://unifi.example.com", cfg.UniFi.URL)
	assert.Equal(t, "admin", cfg.UniFi.Username)
	assert.Equal(t, "mysite", cfg.UniFi.Site)
	assert.Equal(t, "http://kuma.example.com:3001", cfg.Kuma.URL)
	assert.Equal(t, "monitor", cfg.Sync.MonitorGroup) // default
	assert.Equal(t, 300, cfg.Sync.IntervalSecs)       // default
}

func TestLoad_Defaults(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":      "https://unifi.example.com",
		"UNIFI_USERNAME": "admin",
		"UNIFI_PASSWORD": "secret",
		"KUMA_URL":       "http://kuma.example.com:3001",
		"KUMA_USERNAME":  "kuma",
		"KUMA_PASSWORD":  "kumasecret",
	})

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "default", cfg.UniFi.Site)
	assert.Equal(t, "monitor", cfg.Sync.MonitorGroup)
	assert.Equal(t, "kuma-group", cfg.Sync.GroupPrefix)
	assert.True(t, cfg.Sync.HumanizeGroupNames)
	assert.Equal(t, 300, cfg.Sync.IntervalSecs)
	assert.False(t, cfg.Sync.DryRun)
	assert.False(t, cfg.Sync.DeleteOrphan)
}

func TestLoad_GroupPrefixAndHumanizeOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":                 "https://unifi.example.com",
		"UNIFI_USERNAME":            "admin",
		"UNIFI_PASSWORD":            "secret",
		"KUMA_URL":                  "http://kuma.example.com:3001",
		"KUMA_USERNAME":             "kuma",
		"KUMA_PASSWORD":             "kumasecret",
		"SYNC_GROUP_PREFIX":         "monitor-group",
		"SYNC_HUMANIZE_GROUP_NAMES": "false",
	})

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "monitor-group", cfg.Sync.GroupPrefix)
	assert.False(t, cfg.Sync.HumanizeGroupNames)
}

func TestLoad_EnvOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":             "https://unifi.example.com",
		"UNIFI_USERNAME":        "admin",
		"UNIFI_PASSWORD":        "secret",
		"KUMA_URL":              "http://kuma.example.com:3001",
		"KUMA_USERNAME":         "kuma",
		"KUMA_PASSWORD":         "kumasecret",
		"SYNC_INTERVAL_SECONDS": "60",
		"SYNC_MONITOR_GROUP":    "watched",
		"SYNC_DRY_RUN":          "true",
		"SYNC_DELETE_ORPHAN":    "1",
		"UNIFI_INSECURE":        "yes",
	})

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, 60, cfg.Sync.IntervalSecs)
	assert.Equal(t, "watched", cfg.Sync.MonitorGroup)
	assert.True(t, cfg.Sync.DryRun)
	assert.True(t, cfg.Sync.DeleteOrphan)
	assert.True(t, cfg.UniFi.Insecure)
}

func TestLoad_MissingRequired(t *testing.T) {
	clearEnv(t)

	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIFI_URL")
}

func TestLoad_MissingAuthForUniFi(t *testing.T) {
	// UniFi has no API-key option — missing username+password → validation error.
	setEnv(t, map[string]string{
		"UNIFI_URL":     "https://unifi.example.com",
		"KUMA_URL":      "http://kuma.example.com:3001",
		"KUMA_USERNAME": "kuma",
		"KUMA_PASSWORD": "secret",
	})

	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UniFi")
}

func TestLoad_MissingAuthForKuma(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":      "https://unifi.example.com",
		"UNIFI_USERNAME": "admin",
		"UNIFI_PASSWORD": "secret",
		"KUMA_URL":       "http://kuma.example.com:3001",
	})

	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Kuma")
}

func TestLoad_KumaDisableAuth(t *testing.T) {
	// No Kuma username/password, but KUMA_DISABLE_AUTH=true → valid.
	setEnv(t, map[string]string{
		"UNIFI_URL":         "https://unifi.example.com",
		"UNIFI_USERNAME":    "admin",
		"UNIFI_PASSWORD":    "secret",
		"KUMA_URL":          "http://kuma.example.com:3001",
		"KUMA_DISABLE_AUTH": "true",
	})

	cfg, err := Load("")
	require.NoError(t, err)
	assert.True(t, cfg.Kuma.DisableAuth)
}

func TestLoad_YAMLFile(t *testing.T) {
	clearEnv(t)

	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)

	_, err = f.WriteString(`
unifi:
  url: https://yaml-unifi.example.com
  username: yamladmin
  password: yamlsecret
  site: yamlsite
kuma:
  url: http://yaml-kuma.example.com:3001
  username: yamlkuma
  password: yamlkumasecret
sync:
  interval_seconds: 120
  monitor_group: watched
`)
	require.NoError(t, err)
	f.Close()

	cfg, err := Load(f.Name())
	require.NoError(t, err)

	assert.Equal(t, "https://yaml-unifi.example.com", cfg.UniFi.URL)
	assert.Equal(t, "yamlsite", cfg.UniFi.Site)
	assert.Equal(t, 120, cfg.Sync.IntervalSecs)
	assert.Equal(t, "watched", cfg.Sync.MonitorGroup)
}

func TestLoad_YAMLOverriddenByEnv(t *testing.T) {
	clearEnv(t)

	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)

	_, err = f.WriteString(`
unifi:
  url: https://yaml-unifi.example.com
  username: yamladmin
  password: yamlsecret
kuma:
  url: http://yaml-kuma.example.com:3001
  username: yamlkuma
  password: yamlkumasecret
`)
	require.NoError(t, err)
	f.Close()

	t.Setenv("UNIFI_URL", "https://env-override.example.com")

	cfg, err := Load(f.Name())
	require.NoError(t, err)

	assert.Equal(t, "https://env-override.example.com", cfg.UniFi.URL)
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"Yes", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseBool(tt.input))
		})
	}
}

// setEnv sets env vars and registers cleanup to remove them.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	clearEnv(t)
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

// clearEnv removes all config-related env vars.
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"UNIFI_URL", "UNIFI_USERNAME", "UNIFI_PASSWORD", "UNIFI_SITE", "UNIFI_INSECURE",
		"KUMA_URL", "KUMA_USERNAME", "KUMA_PASSWORD", "KUMA_DISABLE_AUTH",
		"SYNC_INTERVAL_SECONDS", "SYNC_MONITOR_GROUP", "SYNC_GROUP_PREFIX", "SYNC_HUMANIZE_GROUP_NAMES",
		"SYNC_DRY_RUN", "SYNC_DELETE_ORPHAN",
	}
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
