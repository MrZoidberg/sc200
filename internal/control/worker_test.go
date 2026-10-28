package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/protocol"
	"github.com/MrZoidberg/sc200/internal/serial"
)

type timerRequest struct {
	duration time.Duration
	ch       chan time.Time
}
type testClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan timerRequest
}

func newClock() *testClock {
	return &testClock{now: time.Unix(100, 0), timers: make(chan timerRequest, 50)}
}
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.timers <- timerRequest{d, ch}
	return ch
}
func (c *testClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *testClock) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case r := <-c.timers:
		c.advance(r.duration)
		r.ch <- c.Now()
		return r.duration
	case <-time.After(time.Second):
		t.Fatal("worker did not schedule")
		return 0
	}
}
func (c *testClock) paused(t *testing.T) timerRequest {
	t.Helper()
	select {
	case r := <-c.timers:
		return r
	case <-time.After(time.Second):
		t.Fatal("worker did not pause")
		return timerRequest{}
	}
}

type fakeController struct {
	mu       sync.Mutex
	speeds   []int
	reads    int
	closed   bool
	closeErr error
	read     func(context.Context, int) (protocol.Map, error)
	write    func(context.Context, int, int) error
}

func (f *fakeController) ReadMap(ctx context.Context) (protocol.Map, error) {
	f.mu.Lock()
	f.reads++
	n := f.reads
	f.mu.Unlock()
	if f.read != nil {
		return f.read(ctx, n)
	}
	return goodMap(), nil
}
func (f *fakeController) SetSpeed(ctx context.Context, n int) error {
	f.mu.Lock()
	f.speeds = append(f.speeds, n)
	i := len(f.speeds)
	f.mu.Unlock()
	if f.write != nil {
		return f.write(ctx, n, i)
	}
	return nil
}
func (f *fakeController) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return f.closeErr
}
func (f *fakeController) commands() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.speeds...)
}
func goodMap() protocol.Map {
	e := protocol.Element{Index: 0, Installed: true, Status: 1}
	return protocol.Map{Temperatures: []protocol.Temperature{{Element: e, Celsius: 25, Available: true}}, Fans: []protocol.Fan{{Element: e, RPM: 2680, Available: true}}}
}
func workerConfig() config.Enclosure {
	c := config.DefaultEnclosure()
	c.ID = "test"
	c.Cooldown = time.Nanosecond
	return c
}
func startWorker(c config.Enclosure, clock Clock, open OpenController) *Worker {
	w := NewWorker(c, open, clock)
	go w.Run(context.Background())
	return w
}
func stopWorker(t *testing.T, w *Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
func TestWorkerRecoveryRefreshSnapshotShutdown(t *testing.T) {
	c := workerConfig()
	clock := newClock()
	f := &fakeController{}
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil })
	defer stopWorker(t, w)
	r := clock.paused(t)
	s := w.Snapshot()
	if s.State != Starting || s.Healthy || s.CompletedCommand != 100 {
		t.Fatalf("startup %+v", s)
	}
	for i := 0; i < 3; i++ {
		clock.advance(r.duration)
		r.ch <- clock.Now()
		r = clock.paused(t)
	}
	s = w.Snapshot()
	if !s.Healthy || s.State != Healthy || s.Target != 24 || s.CompletedCommand != 24 {
		t.Fatalf("healthy %+v", s)
	}
	transition := s.TransitionTime
	s.Telemetry.Temperatures[0].Celsius = 100
	if w.Snapshot().Telemetry.Temperatures[0].Celsius != 25 {
		t.Fatal("snapshot aliases worker")
	}
	for i := 0; i < 3; i++ {
		clock.advance(r.duration)
		r.ch <- clock.Now()
		r = clock.paused(t)
	}
	if len(f.commands()) != 3 || !w.Snapshot().TransitionTime.Equal(transition) {
		t.Fatalf("refresh commands %v", f.commands())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if s = w.Snapshot(); s.State != Stopping || s.Target != 24 || s.CompletedCommand != 24 || s.Operations.Shutdown != 1 || len(f.commands()) != 3 {
		t.Fatalf("stop %+v", s)
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestWorkerReconnectPreservesRequiredSensorsAndControlHealth(t *testing.T) {
	clock := newClock()
	c := workerConfig()
	c.RecoverySamples = 1
	first := &fakeController{read: func(_ context.Context, n int) (protocol.Map, error) {
		m := goodMap()
		m.Temperatures = append(m.Temperatures, m.Temperatures[0])
		m.Temperatures[1].Index = 1
		return m, nil
	}, write: func(_ context.Context, _ int, n int) error {
		if n >= 2 {
			return errors.New("write failed")
		}
		return nil
	}}
	second := &fakeController{}
	opens := 0
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	})
	defer stopWorker(t, w)
	clock.next(t)        // cooldown passes before first lower speed request
	r := clock.paused(t) // disconnected backoff
	s := w.Snapshot()
	if s.State != Disconnected || s.Reason != "control_failed" || s.Target != 100 || s.CompletedCommand != 100 || s.Errors.Fallback != 1 {
		t.Fatalf("failed write %+v", s)
	}
	clock.advance(r.duration)
	r.ch <- clock.Now()
	_ = clock.paused(t)
	s = w.Snapshot()
	if s.State != Failsafe || s.Reason != "missing_sensor" || s.Healthy || s.Target != 100 {
		t.Fatalf("shrinking sensors %+v", s)
	}
	if len(w.policy.RequiredSensors) != 2 {
		t.Fatal("lost required sensors on reconnect")
	}
}
func TestWorkerBackoffAndDisconnectedShutdown(t *testing.T) {
	c := workerConfig()
	c.ReconnectMax = 4 * time.Second
	clock := newClock()
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) {
		return nil, errors.New("unplugged")
	})
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		r := clock.paused(t)
		if r.duration != want {
			t.Fatalf("backoff %s want %s", r.duration, want)
		}
		clock.advance(r.duration)
		r.ch <- clock.Now()
	}
	_ = clock.paused(t)
	if s := w.Snapshot(); s.Target != 100 || s.CompletedCommand != 0 || s.Errors.Open != 5 {
		t.Fatalf("disconnected %+v", s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Snapshot().Errors.Shutdown != 0 {
		t.Fatal("disconnected cleanup reported a failure")
	}
}
func TestWorkerStallFreshnessAndCancellation(t *testing.T) {
	c := workerConfig()
	c.RecoverySamples = 1
	clock := newClock()
	blocked := make(chan struct{})
	f := &fakeController{read: func(ctx context.Context, n int) (protocol.Map, error) {
		if n == 3 {
			close(blocked)
			<-ctx.Done()
			return protocol.Map{}, ctx.Err()
		}
		return goodMap(), nil
	}}
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil })
	clock.next(t)
	clock.next(t)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("read did not block")
	}
	before := w.Snapshot()
	clock.advance(c.StaleAfter)
	s := w.Snapshot()
	if s.Fresh || s.Healthy || s.Target != 100 || s.State != Failsafe || s.Reason != "stale_telemetry" || !s.TransitionTime.Equal(before.LastTelemetry.Add(c.StaleAfter)) {
		t.Fatalf("stale %+v", s)
	}
	commandsBefore := len(f.commands())
	stopWorker(t, w)
	if len(f.commands()) != commandsBefore {
		t.Fatal("shutdown changed fan speed")
	}
}
func TestWorkerTelemetryFailureFallbackAndReconnectRecovery(t *testing.T) {
	c := workerConfig()
	clock := newClock()
	first := &fakeController{read: func(context.Context, int) (protocol.Map, error) { return protocol.Map{}, errors.New("partial map") }}
	second := &fakeController{}
	opens := 0
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	})
	defer stopWorker(t, w)
	r := clock.paused(t)
	if s := w.Snapshot(); s.State != Disconnected || s.Errors.Read != 1 || s.Operations.Fallback != 2 {
		t.Fatalf("failed map %+v", s)
	}
	clock.advance(r.duration)
	r.ch <- clock.Now()
	r = clock.paused(t)
	if w.Snapshot().State != Starting {
		t.Fatal("reconnect bypassed recovery")
	}
	for i := 0; i < 2; i++ {
		clock.advance(r.duration)
		r.ch <- clock.Now()
		r = clock.paused(t)
	}
	if !w.Snapshot().Healthy {
		t.Fatal("did not recover")
	}
}
func TestWorkerStartupFailureAndShutdownWithoutWrite(t *testing.T) {
	for _, startup := range []bool{true, false} {
		t.Run(map[bool]string{true: "startup failure", false: "shutdown skips writes"}[startup], func(t *testing.T) {
			c := workerConfig()
			clock := newClock()
			f := &fakeController{write: func(_ context.Context, _ int, n int) error {
				if startup || n > 1 {
					return errors.New("bad control")
				}
				return nil
			}}
			w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil })
			_ = clock.paused(t)
			s := w.Snapshot()
			if startup && (s.CompletedCommand != 0 || s.Errors.Command != 2 || s.State != Disconnected) {
				t.Fatalf("startup failure %+v", s)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := w.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if w.Snapshot().CompletedCommand != s.CompletedCommand {
				t.Fatal("failed command changed completion")
			}
		})
	}
}
func TestWorkerInvalidSampleAndOpenCleanup(t *testing.T) {
	c := workerConfig()
	clock := newClock()
	f := &fakeController{read: func(context.Context, int) (protocol.Map, error) {
		m := goodMap()
		m.Temperatures[0].Available = false
		return m, nil
	}}
	w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil })
	_ = clock.paused(t)
	if s := w.Snapshot(); s.State != Failsafe || s.Reason != "invalid_sensor" || s.Target != 100 || !s.Fresh {
		t.Fatalf("invalid sample %+v", s)
	}
	stopWorker(t, w)
	clock = newClock()
	f = &fakeController{}
	w = startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) {
		return f, errors.New("init failed")
	})
	_ = clock.paused(t)
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if !closed {
		t.Fatal("failed open leaked controller")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = w.Shutdown(ctx)
}
func TestSystemClock(t *testing.T) {
	c := SystemClock{}
	if c.Now().IsZero() {
		t.Fatal("zero time")
	}
	select {
	case <-c.After(time.Nanosecond):
	case <-time.After(time.Second):
		t.Fatal("no timer")
	}
	w := NewWorker(workerConfig(), nil, nil)
	if w.clock == nil {
		t.Fatal("no default clock")
	}
}

func TestWorkerParentCancellationAndShutdownBudget(t *testing.T) {
	clock := newClock()
	c := workerConfig()
	entered := make(chan struct{})
	release := make(chan struct{})
	// Ownership must remain held while a cancelled opener unwinds; shutdown
	// must not send concurrent commands or skip eventual cleanup.
	w := NewWorker(c, func(ctx context.Context, _ config.Enclosure) (serial.Controller, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}, clock)
	parent, cancelParent := context.WithCancel(context.Background())
	go w.Run(parent)
	<-entered
	cancelParent()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- w.Shutdown(ctx) }()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned before opener released ownership: %v", err)
	default:
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("shutdown budget %v", err)
	}
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation not propagated")
	}
}

func TestWorkerExpiredShutdownStillClosesController(t *testing.T) {
	clock := newClock()
	f := &fakeController{write: func(ctx context.Context, _, _ int) error { return ctx.Err() }}
	w := NewWorker(workerConfig(), func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil }, clock)
	go w.Run(context.Background())
	_ = clock.paused(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		t.Fatal("expired cleanup left device open")
	}
}

func TestWorkerSlowSuccessfulOperationsCannotHideExpiry(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			c := workerConfig()
			c.PollInterval = 10 * time.Second
			c.CommandTimeout = 2 * time.Second
			c.StaleAfter = 13 * time.Second
			c.RecoverySamples = 1
			if operation == "write" {
				c.RefreshInterval = time.Second
			}
			clock := newClock()
			f := &fakeController{}
			lowWrites := 0
			f.write = func(_ context.Context, speed, _ int) error {
				if speed != c.SafeSpeed {
					lowWrites++
					if operation == "write" && lowWrites == 2 {
						clock.advance(14 * time.Second)
					} else {
						clock.advance(2 * time.Second)
					}
				}
				return nil
			}
			f.read = func(_ context.Context, n int) (protocol.Map, error) {
				if n == 3 && operation == "read" {
					clock.advance(4 * time.Second)
				}

				return goodMap(), nil
			}
			w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil })
			clock.next(t)
			clock.next(t)
			_ = clock.paused(t)
			s := w.Snapshot()
			if s.State != Disconnected || s.Reason != "stale_telemetry" || s.CompletedCommand != c.SafeSpeed || w.policy.Recovery != 0 || w.policy.Healthy {
				t.Fatalf("expired telemetry accepted: %+v", s)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = w.Shutdown(ctx)
		})
	}
}

func TestWorkerFailureLogsUnderlyingFallbackErrorsOnce(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	w := NewWorker(workerConfig(), nil, newClock())
	for i := 0; i < 2; i++ {
		w.controller = &fakeController{write: func(context.Context, int, int) error { return errors.New("fallback serial timeout") }}
		w.failure(context.Background(), "telemetry_failed", errors.New("device unplugged"))
	}
	logs := output.String()
	if strings.Count(logs, "enclosure operation failed") != 1 || !strings.Contains(logs, "device unplugged") || !strings.Contains(logs, "fallback serial timeout") || !strings.Contains(logs, "enclosure=test") {
		t.Fatal(logs)
	}
}

func TestWorkerReconnectRestartsReductionCooldown(t *testing.T) {
	for _, reason := range []string{"telemetry_failed", "stale_telemetry"} {
		t.Run(reason, func(t *testing.T) {
			c := workerConfig()
			c.Cooldown = 30 * time.Second
			clock := newClock()
			first := &fakeController{read: func(_ context.Context, n int) (protocol.Map, error) {
				if n == 4 {
					if reason == "stale_telemetry" {
						clock.advance(c.StaleAfter)
						return goodMap(), nil
					}
					return protocol.Map{}, errors.New("disconnected")
				}
				return goodMap(), nil
			}}
			second := &fakeController{}
			opens := 0
			w := startWorker(c, clock, func(context.Context, config.Enclosure) (serial.Controller, error) {
				opens++
				if opens == 1 {
					return first, nil
				}
				return second, nil
			})
			defer stopWorker(t, w)
			clock.next(t)
			clock.next(t)
			r := clock.paused(t)
			if !w.policy.ReductionPending {
				t.Fatal("no pending reduction before outage")
			}
			clock.advance(r.duration)
			r.ch <- clock.Now()
			r = clock.paused(t)
			if s := w.Snapshot(); s.State != Disconnected || s.Reason != reason {
				t.Fatalf("failure: %+v", s)
			}
			if w.policy.ReductionPending || w.policy.ReductionTarget != 0 || !w.policy.ReductionSince.IsZero() {
				t.Fatal("outage retained reduction stability")
			}
			clock.advance(2 * c.Cooldown)
			r.ch <- clock.Now()
			r = clock.paused(t)
			for i := 0; i < 2; i++ {
				clock.advance(r.duration)
				r.ch <- clock.Now()
				r = clock.paused(t)
			}
			if s := w.Snapshot(); !s.Healthy || s.Target != c.SafeSpeed || s.CompletedCommand != c.SafeSpeed {
				t.Fatalf("recovery bypassed cooldown: %+v", s)
			}
			if len(w.policy.RequiredSensors) != 1 {
				t.Fatal("required sensors lost")
			}
			for i := 0; i < 3; i++ {
				clock.advance(r.duration)
				r.ch <- clock.Now()
				r = clock.paused(t)
				want := c.SafeSpeed
				if i == 2 {
					want = 24
				}
				if s := w.Snapshot(); s.Target != want || s.CompletedCommand != want {
					t.Fatalf("cooldown step %d: %+v", i, s)
				}
			}
		})
	}
}

func TestSpeedChangeLogs(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		previous, target       int
		initial, stale, failed bool
		kind                   string
		want                   string
	}{
		{name: "change", previous: 50, target: 30, want: "temperatures_celsius=0:25"},
		{name: "refresh", previous: 30, target: 30},
		{name: "failed", previous: 50, target: 30, failed: true},
		{name: "startup", target: 100, initial: true, kind: "fallback", want: "temperatures_celsius=unavailable"},
		{name: "stale fallback", previous: 30, target: 100, stale: true, kind: "fallback", want: "temperatures_celsius=unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			clock := newClock()
			w := NewWorker(workerConfig(), nil, clock, WorkerOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			controller := &fakeController{}
			if tc.failed {
				controller.write = func(context.Context, int, int) error { return errors.New("failed") }
			}
			w.controller = controller
			w.snapshot.CompletedCommand = tc.previous
			if !tc.initial {
				w.sessionTelemetry = true
				w.snapshot.LastCommand = clock.Now()
				w.snapshot.LastTelemetry = clock.Now()
				w.snapshot.Telemetry = goodMap()
			}
			if tc.stale {
				clock.advance(w.cfg.StaleAfter)
			}
			err := w.command(context.Background(), tc.target, tc.kind)
			if (err != nil) != tc.failed {
				t.Fatal(err)
			}
			if tc.want == "" {
				if logs.Len() != 0 {
					t.Fatal(logs.String())
				}
				return
			}
			for _, want := range []string{"level=INFO", `msg="fan speed changed"`, tc.want, "speed_percent="} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("missing %s: %s", want, logs.String())
				}
			}
			if !tc.initial && !strings.Contains(logs.String(), "previous_speed_percent=") {
				t.Fatal(logs.String())
			}
		})
	}
}

func TestDryRunPollingRefreshFallbackShutdown(t *testing.T) {
	for _, failedRead := range []bool{false, true} {
		t.Run(fmt.Sprint(failedRead), func(t *testing.T) {
			var logs bytes.Buffer
			clock := newClock()
			c := workerConfig()
			c.RecoverySamples = 1
			f := &fakeController{write: func(context.Context, int, int) error {
				t.Error("dry-run called SetSpeed")
				return errors.New("unexpected write")
			}}
			if failedRead {
				f.read = func(context.Context, int) (protocol.Map, error) { return protocol.Map{}, errors.New("read failed") }
			}
			w := NewWorker(c, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil }, clock, WorkerOptions{DryRun: true, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			go w.Run(context.Background())
			r := clock.paused(t)
			if !failedRead {
				for i := 0; i < 7; i++ {
					clock.advance(r.duration)
					r.ch <- clock.Now()
					r = clock.paused(t)
				}
				s := w.Snapshot()
				if !s.Healthy || !s.Fresh || s.SimulatedCommand != 24 {
					t.Fatalf("%+v", s)
				}
			} else if w.Snapshot().State != Disconnected {
				t.Fatal(w.Snapshot())
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := w.Shutdown(ctx)
			if !failedRead && err != nil {
				t.Fatal(err)
			}
			s := w.Snapshot()
			if len(f.commands()) != 0 || !s.LastCommand.IsZero() || s.CompletedCommand != 0 || s.Operations.Command != 0 {
				t.Fatalf("hardware commands recorded: %+v", s)
			}
			if !strings.Contains(logs.String(), "dry-run: fan speed command suppressed") || !strings.Contains(logs.String(), "dry_run=true") {
				t.Fatal(logs.String())
			}
			if !failedRead {
				for _, want := range []string{"temperatures_celsius=0:25", "changed=false"} {
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("missing %s: %s", want, logs.String())
					}
				}
			} else if strings.Count(logs.String(), "command=fallback") != 2 {
				t.Fatal(logs.String())
			}
		})
	}
}

func TestShutdownPreservesQuietSpeedAndReportsCloseError(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			c, err := config.EnclosureProfile("quiet")
			if err != nil {
				t.Fatal(err)
			}
			clock := newClock()
			closeErr := errors.New("close failed")
			f := &fakeController{closeErr: closeErr}
			w := NewWorker(c, func(context.Context, config.Enclosure) (serial.Controller, error) { return f, nil }, clock, WorkerOptions{DryRun: dryRun})
			go w.Run(context.Background())
			_ = clock.paused(t)
			before := w.Snapshot()
			if before.Target != 15 {
				t.Fatal("quiet startup fallback", before)
			}
			commands := len(f.commands())
			for range 2 {
				if err := w.Shutdown(context.Background()); !errors.Is(err, closeErr) {
					t.Fatal("close error lost", err)
				}
			}
			after := w.Snapshot()
			if !f.closed || len(f.commands()) != commands || after.CompletedCommand != before.CompletedCommand || after.SimulatedCommand != before.SimulatedCommand || after.Target != before.Target || after.Operations.Shutdown != 1 || after.Errors.Shutdown != 1 {
				t.Fatalf("shutdown changed commands or cleanup was not idempotent: %+v", after)
			}
		})
	}
}
