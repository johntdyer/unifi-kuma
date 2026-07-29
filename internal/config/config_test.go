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
	assert.Equal(t, "kuma", cfg.Sync.TagPrefix) // default
	assert.Equal(t, 300, cfg.Sync.IntervalSecs) // default
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
	assert.Equal(t, "kuma", cfg.Sync.TagPrefix)
	assert.Equal(t, 300, cfg.Sync.IntervalSecs)
	assert.False(t, cfg.Sync.DryRun)
	assert.False(t, cfg.Sync.DeleteOrphan)
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
		"SYNC_TAG_PREFIX":       "monitor",
		"SYNC_DRY_RUN":          "true",
		"SYNC_DELETE_ORPHAN":    "1",
		"UNIFI_INSECURE":        "yes",
	})

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, 60, cfg.Sync.IntervalSecs)
	assert.Equal(t, "monitor", cfg.Sync.TagPrefix)
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

func TestLoad_APIKeyAuth(t *testing.T) {
	setEnv(t, map[string]string{
		"UNIFI_URL":     "https://unifi.example.com",
		"UNIFI_API_KEY": "unifi-key-abc123",
		"KUMA_URL":      "http://kuma.example.com:3001",
		"KUMA_API_KEY":  "kuma-key-xyz789",
	})

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "unifi-key-abc123", cfg.UniFi.APIKey)
	assert.Equal(t, "kuma-key-xyz789", cfg.Kuma.APIKey)
}

func TestLoad_APIKeyTakesPrecedenceOverMissingPassword(t *testing.T) {
	// API key alone is sufficient — no username/password needed.
	setEnv(t, map[string]string{
		"UNIFI_URL":     "https://unifi.example.com",
		"UNIFI_API_KEY": "unifi-key-abc123",
		"KUMA_URL":      "http://kuma.example.com:3001",
		"KUMA_API_KEY":  "kuma-key-xyz789",
	})

	_, err := Load("")
	require.NoError(t, err)
}

func TestLoad_MissingAuthForUniFi(t *testing.T) {
	// Neither API key nor username+password → validation error.
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
  tag_prefix: monitor
`)
	require.NoError(t, err)
	f.Close()

	cfg, err := Load(f.Name())
	require.NoError(t, err)

	assert.Equal(t, "https://yaml-unifi.example.com", cfg.UniFi.URL)
	assert.Equal(t, "yamlsite", cfg.UniFi.Site)
	assert.Equal(t, 120, cfg.Sync.IntervalSecs)
	assert.Equal(t, "monitor", cfg.Sync.TagPrefix)
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
		"UNIFI_URL", "UNIFI_USERNAME", "UNIFI_PASSWORD", "UNIFI_SITE", "UNIFI_INSECURE", "UNIFI_API_KEY",
		"KUMA_URL", "KUMA_USERNAME", "KUMA_PASSWORD", "KUMA_API_KEY",
		"SYNC_INTERVAL_SECONDS", "SYNC_TAG_PREFIX", "SYNC_DRY_RUN", "SYNC_DELETE_ORPHAN",
	}
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
