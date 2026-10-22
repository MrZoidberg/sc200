package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	e := config.DefaultEnclosure()
	e.ID = "sc200-test"
	e.Port = "/dev/test"
	e.RequiredSensors = []int{0}
	var seenPorts []string
	var seenBaud int
	opts := Options{Discover: func(_ context.Context, p []string, b int) (discovery.Result, error) {
		seenPorts = p
		seenBaud = b
		return discovery.Result{Enclosures: []config.Enclosure{e}, Outcomes: []discovery.Outcome{{Port: e.Port}, {Port: "/dev/busy", Err: errors.New("busy")}}}, nil
	}, DiscoveryIdentity: func(p string) (string, error) { return p, nil }, NewManager: func(config.Config) (Manager, error) { t.Fatal("discovery started manager"); return nil, nil }}
	var out, diag bytes.Buffer
	code := Run(context.Background(), []string{"discover", "--config", path, "--port", "/dev/test", "--baud", "57600"}, &out, &diag, opts)
	if code != 0 || seenBaud != 57600 || len(seenPorts) != 1 || !strings.Contains(diag.String(), "busy") {
		t.Fatalf("%d %s", code, diag.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote config")
	}
	if _, err := config.Decode(bytes.NewReader(out.Bytes())); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diag.Reset()
	if code = Run(context.Background(), []string{"discover", "--config", path, "--write"}, &out, &diag, opts); code != 0 {
		t.Fatal(code, diag.String())
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diag.String(), "restart") {
		t.Fatal(diag.String())
	}
}
func TestDiscoverCLIErrorsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--bad"}, {"--port", "relative"}, {"extra"}, {"--config", ""}} {
		var out bytes.Buffer
		if code := Run(context.Background(), append([]string{"discover"}, args...), &out, &out, Options{}); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
	var out bytes.Buffer
	if code := Run(context.Background(), []string{"discover", "--help"}, &out, &out, Options{}); code != 0 || !strings.Contains(out.String(), config.DefaultPath) {
		t.Fatalf("%d %s", code, out.String())
	}
	for _, fail := range []bool{false, true} {
		out.Reset()
		opts := Options{Discover: func(context.Context, []string, int) (discovery.Result, error) {
			if fail {
				return discovery.Result{}, errors.New("lock busy")
			}
			return discovery.Result{}, nil
		}}
		want := 3
		if fail {
			want = 1
		}
		if code := Run(context.Background(), []string{"discover", "--write"}, &out, &out, opts); code != want {
			t.Fatalf("%d %s", code, out.String())
		}
	}
	path := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(path, []byte("bad: config"), 0600); err != nil {
		t.Fatal(err)
	}
	e := config.DefaultEnclosure()
	e.ID = "new"
	e.Port = "/dev/a"
	opts := Options{Discover: func(context.Context, []string, int) (discovery.Result, error) {
		return discovery.Result{Enclosures: []config.Enclosure{e}}, nil
	}, DiscoveryIdentity: func(p string) (string, error) { return p, nil }}
	if code := Run(context.Background(), []string{"discover", "--config", path, "--write"}, &out, &out, opts); code != 1 {
		t.Fatal(code)
	}
}

func TestDiscoveryOutputFailure(t *testing.T) {
	e := config.DefaultEnclosure()
	e.ID, e.Port = "test", "/dev/test"
	opts := Options{Discover: func(context.Context, []string, int) (discovery.Result, error) {
		return discovery.Result{Enclosures: []config.Enclosure{e}}, nil
	}, DiscoveryIdentity: func(p string) (string, error) { return p, nil }}
	var diag bytes.Buffer
	if code := Run(context.Background(), []string{"discover", "--config", filepath.Join(t.TempDir(), "config.yaml")}, failOutput{}, &diag, opts); code != 1 || !strings.Contains(diag.String(), "write discovery output") {
		t.Fatalf("code %d: %s", code, diag.String())
	}
}

type failOutput struct{}

func (failOutput) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
