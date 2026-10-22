// Package service coordinates independent enclosure workers and device ownership.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/MrZoidberg/sc200/internal/serial"
)

// Options supplies hardware boundaries. Nil fields use platform defaults.
// Open must honor cancellation; returned controllers own their device until Close.
type Options struct {
	Open        control.OpenController
	AcquireLock func() (io.Closer, error)
	Identity    func(string) (string, error)
	Clock       control.Clock
}

type Manager struct {
	cfg         config.Config
	options     Options
	workers     []*control.Worker
	mu          sync.Mutex
	started     bool
	stopped     bool
	ownershipMu sync.Mutex
	owners      map[string]string
	lock        io.Closer
	cancel      context.CancelFunc
	stopOnce    sync.Once
	stopDone    chan struct{}
	stopErr     error
}

// New validates the entire configuration before any hardware or lock access.
func New(cfg config.Config, options Options) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Enclosures = append([]config.Enclosure(nil), cfg.Enclosures...)
	defaults(&options)
	m := &Manager{cfg: cfg, options: options, stopDone: make(chan struct{}), owners: make(map[string]string)}
	// Workers retain their own configuration slices, independent of caller edits.
	for _, c := range cfg.Enclosures {
		if !c.Enabled {
			continue
		}
		c.Curve = append([]config.Point(nil), c.Curve...)
		if c.RequiredSensors != nil {
			c.RequiredSensors = append([]int{}, c.RequiredSensors...)
		}
		m.workers = append(m.workers, control.NewWorker(c, m.open, options.Clock))
	}
	return m, nil
}

// Start acquires process ownership and rejects known device aliases before workers
// start. Missing devices remain worker-local failures and may reconnect later.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("manager already stopped")
	}
	if m.started {
		return errors.New("manager already started")
	}
	select {
	case <-m.stopDone:
		return errors.New("manager already stopped")
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identities := map[string]string{}
	for _, c := range m.cfg.Enclosures {
		if !c.Enabled {
			continue
		}
		id, err := m.options.Identity(c.Port)
		if err != nil {
			continue
		}
		if prior, ok := identities[id]; ok {
			return fmt.Errorf("device alias conflict: %s and %s", prior, c.ID)
		}
		identities[id] = c.ID
	}
	lock, err := m.options.AcquireLock()
	if err != nil {
		return fmt.Errorf("acquire compatibility lock: %w", err)
	}
	m.lock = lock
	run, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.started = true
	for _, w := range m.workers {
		go w.Run(run)
	}
	go func() {
		<-run.Done()
		_ = m.Shutdown(context.Background())
	}()
	return nil
}

// open checks aliases again on every connection, including devices absent at startup.
func (m *Manager) open(ctx context.Context, c config.Enclosure) (serial.Controller, error) {
	id, err := m.options.Identity(c.Port)
	if err != nil {
		return nil, err
	}
	m.ownershipMu.Lock()
	if prior, ok := m.owners[id]; ok {
		m.ownershipMu.Unlock()
		return nil, fmt.Errorf("device alias conflict: %s and %s", prior, c.ID)
	}
	m.owners[id] = c.ID
	m.ownershipMu.Unlock()
	release := func() { m.ownershipMu.Lock(); delete(m.owners, id); m.ownershipMu.Unlock() }
	controller, err := m.options.Open(ctx, c)
	if err != nil {
		if controller != nil {
			_ = controller.Close()
		}
		release()
		return nil, err
	}
	return &ownedController{Controller: controller, release: release}, nil
}

type ownedController struct {
	serial.Controller
	release func()
	once    sync.Once
	err     error
}

func (c *ownedController) Close() error {
	c.once.Do(func() { c.err = c.Controller.Close(); c.release() })
	return c.err
}

// Snapshots returns independent cached readings in configuration order; no I/O occurs.
func (m *Manager) Snapshots() []control.Snapshot {
	snapshots := make([]control.Snapshot, len(m.workers))
	for i, w := range m.workers {
		snapshots[i] = w.Snapshot()
	}
	return snapshots
}

// Shutdown concurrently requests 100% with one shared deadline, closes devices,
// then releases the compatibility lock. Cancellation invokes this same path.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.stopOnce.Do(func() {
		defer close(m.stopDone)
		m.mu.Lock()
		m.stopped = true
		started := m.started
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Unlock()
		if !started {
			return
		}
		shutdown, cancel := context.WithTimeout(ctx, m.cfg.ShutdownTimeout)
		defer cancel()
		errs := make(chan error, len(m.workers))
		for _, w := range m.workers {
			go func() { errs <- w.Shutdown(shutdown) }()
		}
		for range m.workers {
			m.stopErr = errors.Join(m.stopErr, <-errs)
		}
		m.stopErr = errors.Join(m.stopErr, m.lock.Close())
	})
	<-m.stopDone
	return m.stopErr
}

// ShutdownTimeout is the configured upper bound for process lifecycle cleanup.
func (m *Manager) ShutdownTimeout() time.Duration { return m.cfg.ShutdownTimeout }
