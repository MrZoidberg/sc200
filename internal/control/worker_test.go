package control

import (
	"context"
	"errors"
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
	mu     sync.Mutex
	speeds []int
	reads  int
	closed bool
	read   func(context.Context, int) (protocol.Map, error)
	write  func(context.Context, int, int) error
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
func (f *fakeController) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
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
	if s = w.Snapshot(); s.State != Stopping || s.Target != 100 || s.CompletedCommand != 100 || s.Operations.Shutdown != 1 {
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
	if w.Shutdown(ctx) == nil {
		t.Fatal("unavailable shutdown reported success")
	}
	if w.Snapshot().Errors.Shutdown != 1 {
		t.Fatal("missing shutdown failure")
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
	stopWorker(t, w)
	if last := f.commands(); last[len(last)-1] != 100 {
		t.Fatalf("shutdown command %v", last)
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
func TestWorkerStartupFailureAndShutdownFailure(t *testing.T) {
	for _, startup := range []bool{true, false} {
		t.Run(map[bool]string{true: "startup", false: "shutdown"}[startup], func(t *testing.T) {
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
			if w.Shutdown(ctx) == nil {
				t.Fatal("shutdown failure hidden")
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
	// Opener observes cancellation. A short cleanup context must not block waiting
	// for an opener still unwinding, and must not send concurrent commands.
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
	if err := w.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown budget %v", err)
	}
	close(release)
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation not propagated")
	}
}
