package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// consoleHandler uses a compact prefix and the standard text encoding for fields.
// Derived handlers share a mutex so concurrent enclosure logs remain whole lines.
type consoleHandler struct {
	out      io.Writer
	mu       *sync.Mutex
	decorate func(slog.Handler) slog.Handler
}

func newConsoleHandler(out io.Writer) slog.Handler {
	return &consoleHandler{out: out, mu: &sync.Mutex{}, decorate: func(h slog.Handler) slog.Handler { return h }}
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *consoleHandler) Handle(ctx context.Context, r slog.Record) error {
	var fields bytes.Buffer
	text := slog.NewTextHandler(&fields, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey) {
				return slog.Attr{}
			}
			return a
		},
	})
	if err := h.decorate(text).Handle(ctx, r); err != nil {
		return err
	}
	// Keep messages on one line, matching the field encoder's newline escaping.
	message := strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(r.Message)
	line := fmt.Sprintf("%s %s %s", r.Time.Format("Jan 02 15:04:05"), r.Level, message)
	if attrs := strings.TrimSuffix(fields.String(), "\n"); attrs != "" {
		line += " " + attrs
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, line+"\n")
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	attrs = append([]slog.Attr(nil), attrs...)
	copy.decorate = func(base slog.Handler) slog.Handler { return h.decorate(base).WithAttrs(attrs) }
	return &copy
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	copy := *h
	copy.decorate = func(base slog.Handler) slog.Handler { return h.decorate(base).WithGroup(name) }
	return &copy
}
