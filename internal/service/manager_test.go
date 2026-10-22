package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/protocol"
	"github.com/MrZoidberg/sc200/internal/serial"
)

type fakeLock struct {
	mu     sync.Mutex
	closed bool
	verify func()
}

func (l *fakeLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.verify != nil {
		l.verify()
	}
	l.closed = true
	return nil
}

type device struct {
	mu            sync.Mutex
	reads         int
	speeds        []int
	closed        bool
	block         bool
	shutdownBlock bool
}

func (d *device) ReadMap(ctx context.Context) (protocol.Map, error) {
	if d.block {
		<-ctx.Done()
		return protocol.Map{}, ctx.Err()
	}
	d.mu.Lock()
	d.reads++
	d.mu.Unlock()
	e := protocol.Element{Index: 0, Installed: true, Status: 1}
	return protocol.Map{Temperatures: []protocol.Temperature{{Element: e, Available: true, Celsius: 25}}, Fans: []protocol.Fan{{Element: e, Available: true, RPM: 2680}}}, nil
}
func (d *device) SetSpeed(ctx context.Context, n int) error {
	d.mu.Lock()
	d.speeds = append(d.speeds, n)
	count := len(d.speeds)
	d.mu.Unlock()
	if d.shutdownBlock && n == 100 && count > 1 {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (d *device) Close() error { d.mu.Lock(); d.closed = true; d.mu.Unlock(); return nil }
func configuration() config.Config {
	c := config.Config{HTTP: config.HTTP{Listen: "127.0.0.1:9700"}, ShutdownTimeout: 100 * time.Millisecond}
	for _, id := range []string{"a", "b", "disabled"} {
		e := config.DefaultEnclosure()
		e.ID = id
		e.Port = "/dev/" + id
		e.Enabled = id != "disabled"
		e.CommandTimeout = 10 * time.Millisecond
		e.PollInterval = 20 * time.Millisecond
		e.StaleAfter = time.Second
		e.RecoverySamples = 1
		c.Enclosures = append(c.Enclosures, e)
	}
	return c
}
func options(devices map[string]*device, l *fakeLock) Options {
	return Options{
		Identity:    func(p string) (string, error) { return p, nil },
		AcquireLock: func() (io.Closer, error) { return l, nil },
		Open: func(_ context.Context, c config.Enclosure) (serial.Controller, error) {
			if d := devices[c.ID]; d != nil {
				return d, nil
			}
			return nil, errors.New("offline")
		},
	}
}
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestHealthyIsolationShutdown(t *testing.T) {
	devices := map[string]*device{"a": {}, "b": {}}
	l := &fakeLock{}
	l.verify = func() {
		for _, d := range devices {
			d.mu.Lock()
			if !d.closed || d.speeds[len(d.speeds)-1] != 100 {
				t.Error("lock released before safe write and close")
			}
			d.mu.Unlock()
		}
	}
	m, err := New(configuration(), options(devices, l))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { s := m.Snapshots(); return len(s) == 2 && s[0].Healthy && s[1].Healthy })
	s := m.Snapshots()
	s[0].Telemetry.Temperatures[0].Celsius = 999
	if m.Snapshots()[0].Telemetry.Temperatures[0].Celsius == 999 {
		t.Fatal("snapshot aliases worker")
	}
	if err = m.Start(context.Background()); err == nil {
		t.Fatal("double start")
	}
	if err = m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestBlockedAndDisconnectedIsolation(t *testing.T) {
	for _, mode := range []string{"blocked", "disconnected", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			devices := map[string]*device{"b": {}}
			if mode != "disconnected" {
				devices["a"] = &device{block: mode == "blocked", shutdownBlock: mode == "shutdown"}
			}
			m, _ := New(configuration(), options(devices, &fakeLock{}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { d := devices["b"]; d.mu.Lock(); defer d.mu.Unlock(); return d.reads >= 3 })
			cancel()
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() { _ = m.Shutdown(context.Background()) })
			}
			wg.Wait()
			d := devices["b"]
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.closed || d.speeds[len(d.speeds)-1] != 100 {
				t.Fatal("healthy enclosure shutdown blocked")
			}
		})
	}
}
func TestStartupValidationAndConflicts(t *testing.T) {
	c := configuration()
	c.Enclosures[1].ID = c.Enclosures[0].ID
	o := options(nil, &fakeLock{})
	o.AcquireLock = func() (io.Closer, error) { t.Fatal("invalid config touched hardware"); return nil, nil }
	if _, err := New(c, o); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	o = options(nil, &fakeLock{})
	o.Identity = func(string) (string, error) { return "same", nil }
	m, _ := New(configuration(), o)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("aliases accepted")
	}
	o = options(nil, &fakeLock{})
	o.AcquireLock = func() (io.Closer, error) { return nil, errors.New("busy") }
	m, _ = New(configuration(), o)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("lock conflict accepted")
	}
	m, _ = New(configuration(), options(nil, &fakeLock{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.Start(ctx) == nil {
		t.Fatal("cancelled start")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Start(context.Background()) == nil {
		t.Fatal("start after shutdown")
	}
}
func TestRuntimeAliasAndFailedOpenRelease(t *testing.T) {
	o := options(nil, &fakeLock{})
	o.Identity = func(string) (string, error) { return "same", nil }
	d := &device{}
	fail := true
	o.Open = func(context.Context, config.Enclosure) (serial.Controller, error) {
		if fail {
			return d, errors.New("open")
		}
		return d, nil
	}
	m, _ := New(configuration(), o)
	c := configuration().Enclosures[0]
	if _, err := m.open(context.Background(), c); err == nil {
		t.Fatal("failed open")
	}
	fail = false
	first, err := m.open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.open(context.Background(), c); err == nil {
		t.Fatal("runtime alias accepted")
	}
	_ = first.Close()
	_ = first.Close()
	if _, err = m.open(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsAndConfigurationOwnership(t *testing.T) {
	var o Options
	defaults(&o)
	if o.Open == nil || o.Identity == nil || o.AcquireLock == nil {
		t.Fatal("missing defaults")
	}
	if _, err := o.Open(context.Background(), config.Enclosure{Port: "/missing-sc200-test-device", Baud: 38400}); err == nil {
		t.Fatal("missing device opened")
	}
	if _, err := o.Identity("/missing-sc200-test-device"); err == nil {
		t.Fatal("missing identity")
	}
	c := configuration()
	c.Enclosures[0].RequiredSensors = []int{0}
	m, err := New(c, options(nil, &fakeLock{}))
	if err != nil {
		t.Fatal(err)
	}
	c.Enclosures[0].Enabled = false
	if !m.cfg.Enclosures[0].Enabled {
		t.Fatal("configuration aliases caller")
	}
	if m.ShutdownTimeout() != c.ShutdownTimeout {
		t.Fatal("shutdown timeout")
	}
	m.options.Identity = func(string) (string, error) { return "", errors.New("missing") }
	if _, err = m.open(context.Background(), configuration().Enclosures[0]); err == nil {
		t.Fatal("identity failure ignored")
	}
}
