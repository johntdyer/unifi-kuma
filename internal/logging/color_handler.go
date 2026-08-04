// Package logging provides the slog.Handler behind unifi-kuma's
// "color" log format — "text" and "json" both use slog's own
// TextHandler/JSONHandler directly and need nothing extra.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ANSI color codes, minimal palette matching common log-level conventions.
const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorBold   = "\033[1m"
)

func levelColor(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return colorCyan
	case level < slog.LevelWarn:
		return colorGreen
	case level < slog.LevelError:
		return colorYellow
	default:
		return colorBold + colorRed
	}
}

// ColorHandler is a slog.Handler that writes human-readable, ANSI-colored
// log lines: timestamp dimmed, level colored by severity, attribute keys
// in cyan — otherwise the same "key=value" shape as slog.TextHandler.
// Colors are unconditional: choosing this handler (via -log-format=color)
// is itself an explicit signal the output is going to an interactive
// terminal, not something scraping logs.
type ColorHandler struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

// NewColorHandler creates a ColorHandler writing to w. If opts or
// opts.Level is nil, the minimum enabled level defaults to slog.LevelInfo,
// matching slog's own handlers. AddSource and ReplaceAttr aren't
// supported.
func NewColorHandler(w io.Writer, opts *slog.HandlerOptions) *ColorHandler {
	var level slog.Leveler = slog.LevelInfo
	if opts != nil && opts.Level != nil {
		level = opts.Level
	}
	return &ColorHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *ColorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *ColorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *ColorHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	b.WriteString(colorGray)
	b.WriteString(r.Time.Format("2006-01-02T15:04:05.000Z07:00"))
	b.WriteString(colorReset)
	b.WriteByte(' ')

	b.WriteString(levelColor(r.Level))
	b.WriteString(r.Level.String())
	b.WriteString(colorReset)
	b.WriteByte(' ')

	b.WriteString(r.Message)

	prefix := groupPrefix(h.groups)
	for _, a := range h.attrs {
		writeAttr(&b, prefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, prefix, a)
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	b.WriteString(colorCyan)
	b.WriteString(prefix)
	b.WriteString(a.Key)
	b.WriteString(colorReset)
	b.WriteByte('=')
	b.WriteString(formatValue(a.Value))
}

func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return quoteIfNeeded(v.String())
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(v.Bool())
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	default:
		return quoteIfNeeded(fmt.Sprint(v.Any()))
	}
}

// quoteIfNeeded mirrors slog.TextHandler's own rule of thumb: an empty
// value or one containing whitespace/quote/equals characters gets quoted
// so the key=value stream stays unambiguous to parse.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"=") {
		return strconv.Quote(s)
	}
	return s
}
