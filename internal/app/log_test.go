package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConsoleHandler(t *testing.T) {
	var out bytes.Buffer
	h := newConsoleHandler(&out)
	r := slog.NewRecord(time.Date(2026, time.October, 19, 18, 40, 58, 0, time.FixedZone("local", 3*60*60)), slog.LevelInfo, "fan speed changed", 0)
	r.Add("speed_percent", 15, "reason", "healthy")
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Oct 19 18:40:58 INFO fan speed changed speed_percent=15 reason=healthy\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	out.Reset()
	derived := h.WithAttrs([]slog.Attr{slog.String("enclosure", "sc200")}).WithGroup("control").WithAttrs([]slog.Attr{slog.String("mode", "quiet")})
	r = slog.NewRecord(r.Time, slog.LevelError, "read failed\nretry", 0)
	r.Add("error", errors.New("no telemetry"))
	if err := derived.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want := "Oct 19 18:40:58 ERROR read failed\\nretry enclosure=sc200 control.mode=quiet control.error=\"no telemetry\"\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
	if h.Enabled(context.Background(), slog.LevelDebug) || !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("incorrect level filtering")
	}
	if err := newConsoleHandler(failOutput{}).Handle(context.Background(), r); err == nil {
		t.Fatal("write error lost")
	}
}

func TestConsoleHandlerConcurrent(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(newConsoleHandler(&out))
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() { logger.With("enclosure", i).Info("poll complete") })
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 20 {
		t.Fatal("lost log lines", out.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, " INFO poll complete enclosure=") {
			t.Fatal("interleaved log line", line)
		}
	}
}
