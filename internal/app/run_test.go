package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/MrZoidberg/sc200/internal/serial"
	"github.com/MrZoidberg/sc200/internal/service"
)

func validConfig() config.Config {
	e := config.DefaultEnclosure()
	e.ID = "one"
	e.Port = "/dev/test"
	return config.Config{HTTP: config.HTTP{Listen: "127.0.0.1:9700"}, ShutdownTimeout: time.Second, Enclosures: []config.Enclosure{e}}
}
func invoke(ctx context.Context, args []string, o Options) (int, string, string) {
	var out, errout bytes.Buffer
	code := Run(ctx, args, &out, &errout, o)
	return code, out.String(), errout.String()
}
func TestCLI(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		code     int
		contains string
	}{{[]string{"--version"}, 0, "sc200"}, {[]string{"--help"}, 0, config.DefaultPath}, {[]string{"--bad"}, 2, "flag provided"}, {[]string{"other"}, 2, "Usage:"}, {[]string{"--config="}, 2, "Usage:"}, {[]string{"--version", "--check-config"}, 2, "Usage:"}} {
		code, out, errout := invoke(context.Background(), tc.args, Options{Load: func(string) (config.Config, error) { t.Fatal("unexpected load"); return config.Config{}, nil }})
		if code != tc.code || !strings.Contains(out+errout, tc.contains) {
			t.Fatalf("%v: %d %s %s", tc.args, code, out, errout)
		}
	}
}
func TestCheckConfigPaths(t *testing.T) {
	for _, path := range []string{config.DefaultPath, "/custom/config.yaml"} {
		args := []string{"--check-config"}
		if path != config.DefaultPath {
			args = append(args, "--config", path)
		}
		code, out, errout := invoke(context.Background(), args, Options{Load: func(got string) (config.Config, error) {
			if got != path {
				t.Fatal(got)
			}
			return validConfig(), nil
		}, NewManager: func(config.Config) (Manager, error) { t.Fatal("check opened manager"); return nil, nil }})
		if code != 0 || !strings.Contains(out, path) {
			t.Fatalf("%d %s %s", code, out, errout)
		}
	}
}
func TestRealConfigLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("enclosures:\n  - id: one\n    port: /dev/test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, errout := invoke(context.Background(), []string{"--check-config", "--config", path}, Options{})
	if code != 0 {
		t.Fatal(errout)
	}
	code, _, errout = invoke(context.Background(), []string{"--check-config", "--config", path + "missing"}, Options{})
	if code != 1 || !strings.Contains(errout, path+"missing") {
		t.Fatal(code, errout)
	}
}

type fakeManager struct {
	startErr, stopErr error
	started, stopped  bool
	ctx               context.Context
}

func (m *fakeManager) Start(c context.Context) error  { m.started = true; m.ctx = c; return m.startErr }
func (m *fakeManager) Shutdown(context.Context) error { m.stopped = true; return m.stopErr }
func (m *fakeManager) ShutdownTimeout() time.Duration { return time.Second }
func (m *fakeManager) Snapshots() []control.Snapshot  { return nil }

type fakeHTTP struct {
	serveErr, stopErr error
	started           chan struct{}
	done              chan struct{}
	stopped           bool
}

func (h *fakeHTTP) Serve(net.Listener) error {
	close(h.started)
	if h.serveErr != nil {
		return h.serveErr
	}
	<-h.done
	return nil
}
func (h *fakeHTTP) Shutdown(context.Context) error { h.stopped = true; close(h.done); return h.stopErr }
func options(m *fakeManager, h *fakeHTTP) Options {
	return Options{Load: func(string) (config.Config, error) { return validConfig(), nil }, NewManager: func(config.Config) (Manager, error) { return m, nil }, Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }, NewHTTP: func(string, Manager) HTTPServer { return h }}
}
func TestLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		serveErr, managerErr, httpErr error
		code                          int
	}{{name: "cancellation"}, {name: "HTTP fatal", serveErr: errors.New("serve failed"), code: 1}, {name: "shutdown cleanup failure", managerErr: errors.New("close failed"), code: 1}, {name: "HTTP shutdown failure", httpErr: errors.New("shutdown failed"), code: 1}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeManager{stopErr: tc.managerErr}
			h := &fakeHTTP{serveErr: tc.serveErr, stopErr: tc.httpErr, started: make(chan struct{}), done: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() { code, _, _ := invoke(ctx, nil, options(m, h)); done <- code }()
			select {
			case <-h.started:
			case <-time.After(time.Second):
				t.Fatal("start timed out")
			}
			if tc.serveErr == nil {
				cancel()
			}
			select {
			case code := <-done:
				if code != tc.code || !m.started || !m.stopped || !h.stopped || m.ctx.Err() == nil {
					t.Fatalf("code=%d m=%+v h=%+v", code, m, h)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stop timed out")
			}
		})
	}
}
func TestStartupErrors(t *testing.T) {
	for _, kind := range []string{"load", "invalid", "manager", "listen", "start"} {
		t.Run(kind, func(t *testing.T) {
			m := &fakeManager{}
			h := &fakeHTTP{}
			o := options(m, h)
			switch kind {
			case "load":
				o.Load = func(string) (config.Config, error) { return config.Config{}, os.ErrPermission }
			case "invalid":
				o.Load = func(string) (config.Config, error) { return config.Config{}, nil }
			case "manager":
				o.NewManager = func(config.Config) (Manager, error) { return nil, errors.New("manager failed") }
			case "listen":
				o.Listen = func(string, string) (net.Listener, error) { return nil, errors.New("address already in use") }
			case "start":
				m.startErr = errors.New("lock permission denied")
			}
			code, _, errout := invoke(context.Background(), nil, o)
			if code != 1 || !strings.Contains(errout, "service failed") {
				t.Fatal(code, errout)
			}
			if kind != "start" && m.started {
				t.Fatal("invalid startup touched devices")
			}
			if kind == "start" && !m.stopped {
				t.Fatal("startup not cleaned up")
			}
		})
	}
}
func TestOccupiedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	m := &fakeManager{}
	o := options(m, nil)
	o.Listen = nil
	o.Load = func(string) (config.Config, error) {
		c := validConfig()
		c.HTTP.Listen = listener.Addr().String()
		return c, nil
	}
	code, _, out := invoke(context.Background(), nil, o)
	if code != 1 || m.started || !strings.Contains(out, "listen") {
		t.Fatal(code, out)
	}
}
func TestCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := Options{Load: func(string) (config.Config, error) { return validConfig(), nil }, Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }}
	code, _, out := invoke(ctx, nil, o)
	if code != 0 {
		t.Fatal(code, out)
	}
}

func TestDefaultHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &fakeManager{}
	o := options(m, nil)
	o.NewHTTP = nil
	o.Listen = func(string, string) (net.Listener, error) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		cancel()
		return l, err
	}
	code, _, out := invoke(ctx, nil, o)
	if code != 0 || !m.stopped {
		t.Fatal(code, out)
	}
}

type closer struct{}

func (closer) Close() error { return nil }
func TestTransientDeviceFailureKeepsServing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan struct{}, 1)
	h := &fakeHTTP{started: make(chan struct{}), done: make(chan struct{})}
	o := options(nil, h)
	o.NewManager = func(c config.Config) (Manager, error) {
		return service.New(c, service.Options{Identity: func(s string) (string, error) { return s, nil }, AcquireLock: func() (io.Closer, error) { return closer{}, nil }, Open: func(context.Context, config.Enclosure) (serial.Controller, error) {
			select {
			case opened <- struct{}{}:
			default:
			}
			return nil, os.ErrPermission
		}})
	}
	done := make(chan int, 1)
	go func() { code, _, _ := invoke(ctx, nil, o); done <- code }()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("worker did not attempt device")
	}
	select {
	case <-h.started:
	case <-time.After(time.Second):
		t.Fatal("HTTP did not start")
	}
	select {
	case code := <-done:
		t.Fatalf("transient failure stopped service: %d", code)
	default:
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("disconnected cleanup failed: %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup hung")
	}
}

func TestCLIOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--check-config"}} {
		t.Run(args[0], func(t *testing.T) {
			var diag bytes.Buffer
			opts := Options{Load: func(string) (config.Config, error) { return validConfig(), nil }}
			if code := Run(context.Background(), args, failOutput{}, &diag, opts); code != 1 {
				t.Fatalf("output error returned %d", code)
			}
		})
	}
}
