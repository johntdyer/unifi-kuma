package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetupLogger_Levels(t *testing.T) {
	defer slog.SetDefault(slog.Default())

	tests := []struct {
		name  string
		level string
		want  slog.Level
	}{
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"unknown defaults to info", "bogus", slog.LevelInfo},
		{"empty defaults to info", "", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupLogger(tt.level, false)

			logger := slog.Default()
			assert.True(t, logger.Enabled(context.Background(), tt.want))
			if tt.want > slog.LevelDebug {
				assert.False(t, logger.Enabled(context.Background(), tt.want-1))
			}
		})
	}
}

func TestSetupLogger_HandlerType(t *testing.T) {
	defer slog.SetDefault(slog.Default())

	setupLogger("info", true)
	_, isJSON := slog.Default().Handler().(*slog.JSONHandler)
	assert.True(t, isJSON, "expected JSONHandler when jsonOutput is true")

	setupLogger("info", false)
	_, isText := slog.Default().Handler().(*slog.TextHandler)
	assert.True(t, isText, "expected TextHandler when jsonOutput is false")
}
