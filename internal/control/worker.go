package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/serial"
)

// Clock permits deterministic scheduling tests. All times share its monotonic clock.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// OpenController must obey cancellation and return a freshly synchronized session.
// Its controller owns device exclusivity until Close. The manager owns the legacy lock.
type OpenController func(context.Context, config.Enclosure) (serial.Controller, error)
type Worker struct {
	cfg              config.Enclosure
	open             OpenController
	clock            Clock
	mu               sync.RWMutex
	snapshot         Snapshot
	cancel           context.CancelFunc
	runContext       context.Context
	done             chan struct{}
	controller       serial.Controller
	policy           PolicyState
	runOnce          sync.Once
	stopOnce         sync.Once
	stopErr          error
	lastFailure      string
	sessionTelemetry bool
}

func NewWorker(c config.Enclosure, open OpenController, clock Clock) *Worker {
	if clock == nil {
		clock = SystemClock{}
	}
	runContext, cancel := context.WithCancel(context.Background())
	return &Worker{cfg: c, open: open, clock: clock, runContext: runContext, cancel: cancel, done: make(chan struct{}), snapshot: Snapshot{ID: c.ID, State: Starting, Reason: "starting", Target: c.SafeSpeed, TransitionTime: clock.Now()}}
}

// Snapshot never performs I/O. Expired readings remain available with Fresh=false;
// consumers must omit their values in current-reading metrics and JSON fields.
func (w *Worker) Snapshot() Snapshot {
	w.mu.RLock()
	s := cloneSnapshot(w.snapshot)
	w.mu.RUnlock()
	s.Fresh = !s.LastTelemetry.IsZero() && w.clock.Now().Sub(s.LastTelemetry) < w.cfg.StaleAfter
	if !s.Fresh && !s.LastTelemetry.IsZero() && (s.State == Healthy || s.State == Starting) {
		s.State, s.Reason, s.Healthy, s.Target = Failsafe, "stale_telemetry", false, w.cfg.SafeSpeed
		s.TransitionTime = s.LastTelemetry.Add(w.cfg.StaleAfter)
	}
	return s
}
func (w *Worker) update(fn func(*Snapshot)) { w.mu.Lock(); defer w.mu.Unlock(); fn(&w.snapshot) }
func (w *Worker) transition(state State, reason string, target int, healthy bool) {
	w.update(func(s *Snapshot) {
		if s.State != state || s.Reason != reason {
			s.TransitionTime = w.clock.Now()
		}
		s.State, s.Reason, s.Target, s.Healthy = state, reason, target, healthy
	})
}
func (w *Worker) wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-w.clock.After(d):
		return true
	}
}
func (w *Worker) command(ctx context.Context, target int, kind string) error {
	w.update(func(s *Snapshot) {
		s.Operations.Command++
		if kind == "fallback" {
			s.Operations.Fallback++
		}
		if kind == "shutdown" {
			s.Operations.Shutdown++
		}
	})
	budget := w.cfg.CommandTimeout
	if kind == "" {
		budget = min(budget, w.freshnessBudget())
	}
	c, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	err := w.controller.SetSpeed(c, target)
	w.update(func(s *Snapshot) {
		if err != nil {
			s.Errors.Command++
			if kind == "fallback" {
				s.Errors.Fallback++
			}
			if kind == "shutdown" {
				s.Errors.Shutdown++
			}
		} else {
			s.CompletedCommand = target
			s.LastCommand = w.clock.Now()
		}
	})
	return err
}
func (w *Worker) freshnessBudget() time.Duration {
	s := w.Snapshot()
	if !w.sessionTelemetry || s.LastTelemetry.IsZero() {
		return w.cfg.CommandTimeout
	}
	return s.LastTelemetry.Add(w.cfg.StaleAfter).Sub(w.clock.Now())
}
func (w *Worker) logFailure(reason string, err error) {
	message := reason + ": " + err.Error()
	if message != w.lastFailure {
		slog.Error("enclosure operation failed", "enclosure", w.cfg.ID, "reason", reason, "error", err)
		w.lastFailure = message
	}
}

// resetRecovery discards stability inferred before a transport interruption while
// retaining sensor identities so reconnect cannot silently drop required sensors.
func (w *Worker) resetRecovery() {
	w.policy.Healthy = false
	w.policy.Recovery = 0
	w.policy.Target = w.cfg.SafeSpeed
	w.policy.ReductionPending = false
	w.policy.ReductionTarget = 0
	w.policy.ReductionSince = time.Time{}
}
func (w *Worker) failure(ctx context.Context, reason string, cause error) {
	w.resetRecovery()
	w.transition(Failsafe, reason, w.cfg.SafeSpeed, false)
	// Exactly one bounded fallback attempt; ambiguous sessions will reject it.
	fallbackErr := w.command(ctx, w.cfg.SafeSpeed, "fallback")
	w.logFailure(reason, errors.Join(cause, fallbackErr))
	_ = w.controller.Close()
	w.controller = nil
	w.transition(Disconnected, reason, w.cfg.SafeSpeed, false)
}

// Run is single-use. Cancellation stops normal work; Shutdown performs final writes
// using a separate shared deadline supplied by the service manager.
func (w *Worker) Run(ctx context.Context) {
	w.runOnce.Do(func() {
		stop := context.AfterFunc(ctx, w.cancel)
		defer stop()
		ctx = w.runContext
		defer w.cancel()
		defer close(w.done)
		backoff := w.cfg.ReconnectMin
		for ctx.Err() == nil {
			w.update(func(s *Snapshot) { s.Operations.Open++ })
			c, cc := context.WithTimeout(ctx, w.cfg.CommandTimeout)
			controller, err := w.open(c, w.cfg)
			cc()
			if err != nil {
				if controller != nil {
					_ = controller.Close()
				}
				w.update(func(s *Snapshot) { s.Errors.Open++ })
				w.logFailure("open_failed", err)
				w.transition(Disconnected, "open_failed", w.cfg.SafeSpeed, false)
			} else {
				w.controller = controller
				w.sessionTelemetry = false
				w.resetRecovery()
				w.transition(Starting, "recovering", w.cfg.SafeSpeed, false)
				if err = w.command(ctx, w.cfg.SafeSpeed, "fallback"); err != nil {
					w.failure(ctx, "control_failed", err)
				} else {
					for ctx.Err() == nil {
						if w.freshnessBudget() <= 0 {
							w.failure(ctx, "stale_telemetry", errors.New("telemetry freshness deadline expired"))
							break
						}
						w.update(func(s *Snapshot) { s.Operations.Read++ })
						c, cc = context.WithTimeout(ctx, min(w.cfg.CommandTimeout, w.freshnessBudget()))
						m, readErr := w.controller.ReadMap(c)
						cc()
						if ctx.Err() != nil {
							break
						}
						if w.freshnessBudget() <= 0 {
							w.failure(ctx, "stale_telemetry", errors.Join(readErr, errors.New("telemetry freshness deadline expired")))
							break
						}
						if readErr != nil {
							w.update(func(s *Snapshot) { s.Errors.Read++ })
							w.failure(ctx, "telemetry_failed", readErr)
							break
						}
						now := w.clock.Now()
						w.sessionTelemetry = true
						w.update(func(s *Snapshot) {
							s.Telemetry = cloneSnapshot(Snapshot{Telemetry: m}).Telemetry
							s.LastTelemetry = now
						})
						var d PolicyDecision
						w.policy, d = EvaluatePolicy(w.cfg, w.policy, PolicyInput{Now: now, SampleTime: now, Map: m, NewSample: true, ControlOK: true})
						state := Failsafe
						if d.Healthy {
							state = Healthy
							backoff = w.cfg.ReconnectMin
						} else if d.Reason == "recovering" {
							state = Starting
						}
						w.transition(state, d.Reason, d.Target, d.Healthy)
						s := w.Snapshot()
						if s.CompletedCommand != d.Target || now.Sub(s.LastCommand) >= w.cfg.RefreshInterval {
							if err = w.command(ctx, d.Target, ""); err != nil {
								w.failure(ctx, "control_failed", err)
								break
							}
						}
						if d.Healthy {
							w.lastFailure = ""
						}
						if w.freshnessBudget() <= 0 {
							w.failure(ctx, "stale_telemetry", errors.New("telemetry freshness deadline expired"))
							break
						}
						// Never sleep beyond the absolute telemetry expiry.
						delay := min(w.cfg.PollInterval, w.cfg.RefreshInterval-w.clock.Now().Sub(w.Snapshot().LastCommand))
						if delay <= 0 {
							delay = w.cfg.PollInterval
						}
						delay = min(delay, w.freshnessBudget())
						if !w.wait(ctx, delay) {
							break
						}
					}
				}
			}
			if ctx.Err() != nil {
				break
			}
			if !w.wait(ctx, backoff) {
				break
			}
			backoff += min(backoff, w.cfg.ReconnectMax-backoff)
		}
	})
}

// Shutdown is idempotent and must follow starting Run. A failed final transaction
// leaves CompletedCommand unchanged; the target only records the shutdown request.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.stopOnce.Do(func() {
		w.mu.RLock()
		cancel := w.cancel
		w.mu.RUnlock()
		cancel()
		// Run's I/O receives cancellation above and must finish before its controller
		// can be closed. An already expired shutdown deadline must not skip device
		// cleanup and allow the manager to release ownership while a port is open.
		<-w.done
		w.transition(Stopping, "shutdown", config.ShutdownSpeed, false)
		if w.controller != nil {
			w.stopErr = w.command(ctx, config.ShutdownSpeed, "shutdown")
			w.stopErr = errors.Join(w.stopErr, w.controller.Close())
			w.controller = nil
		} else {
			w.update(func(s *Snapshot) { s.Operations.Shutdown++; s.Errors.Shutdown++ })
			w.stopErr = errors.Join(ctx.Err(), errors.New("shutdown safe-speed unavailable: disconnected"))
		}
	})
	return w.stopErr
}
