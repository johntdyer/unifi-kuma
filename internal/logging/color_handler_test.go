package logging

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain strips ANSI escapes so assertions can check the actual log content
// (e.g. "key=value") without needing to know exactly which segments are
// colorized.
func plain(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

func TestColorHandler_Handle_BasicLine(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h)

	logger.Info("sync cycle complete", "groups", 3, "duration", 2*time.Second)

	out := buf.String()
	assert.Contains(t, plain(out), "sync cycle complete")
	assert.Contains(t, plain(out), "groups=3")
	assert.Contains(t, plain(out), "duration=2s")
	assert.Contains(t, plain(out), "INFO")
	// Colorized: the raw output should carry ANSI escapes the stripped
	// version doesn't.
	assert.Contains(t, out, "\033[")
	assert.NotContains(t, plain(out), "\033[")
	assert.True(t, strings.HasSuffix(out, "\n"))
}

func TestColorHandler_Enabled_RespectsLevel(t *testing.T) {
	h := NewColorHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})

	assert.False(t, h.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, h.Enabled(context.Background(), slog.LevelWarn))
	assert.True(t, h.Enabled(context.Background(), slog.LevelError))
}

func TestColorHandler_Enabled_DefaultsToInfo(t *testing.T) {
	h := NewColorHandler(&bytes.Buffer{}, nil)

	assert.False(t, h.Enabled(context.Background(), slog.LevelDebug))
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}

func TestColorHandler_WithAttrs_PersistsAcrossCalls(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h).With("component", "syncer")

	logger.Info("starting sync cycle")

	assert.Contains(t, plain(buf.String()), "component=syncer")
}

func TestColorHandler_WithGroup_PrefixesKeys(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h).WithGroup("http")

	logger.Info("request", "status", 200)

	assert.Contains(t, plain(buf.String()), "http.status=200")
}

func TestColorHandler_QuotesValuesWithSpaces(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h)

	logger.Info("event", "name", "service-homeassistant ")

	assert.Contains(t, plain(buf.String()), `name="service-homeassistant "`)
}

func TestColorHandler_EmptyStringValueIsQuoted(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h)

	logger.Info("event", "reason", "")

	assert.Contains(t, plain(buf.String()), `reason=""`)
}

func TestColorHandler_FormatsErrorValues(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h)

	logger.Error("sync failed", "error", assertError("controller unreachable"))

	assert.Contains(t, plain(buf.String()), "error=\"controller unreachable\"")
}

// TestColorHandler_ConcurrentWritesDontInterleave exercises Handle's own
// mutex — the underlying io.Writer (a plain bytes.Buffer here) is never
// touched concurrently precisely because the handler serializes access to
// it itself.
func TestColorHandler_ConcurrentWritesDontInterleave(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, nil)
	logger := slog.New(h)

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(n int) {
			logger.Info("concurrent", "n", n)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 20)
	for _, line := range lines {
		assert.Contains(t, line, "concurrent")
	}
}

// assertError is a minimal error type so the test doesn't need to import
// "errors" just to build one string-valued error.
type assertError string

func (e assertError) Error() string { return string(e) }
