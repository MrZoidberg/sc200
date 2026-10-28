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

func TestDiscoverProbeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		found   bool
		details bool
	}{
		{"automatic", nil, true, false},
		{"verbose", []string{"--verbose"}, true, true},
		{"explicit", []string{"--port", "/dev/test"}, true, true},
		{"no matches", nil, false, false},
		{"no matches verbose", []string{"--verbose"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := config.DefaultEnclosure()
			e.ID, e.Port = "test", "/dev/test"
			opts := Options{Discover: func(context.Context, []string, int) (discovery.Result, error) {
				r := discovery.Result{Outcomes: []discovery.Outcome{{Port: "/dev/unused", Err: errors.New("input/output error")}}}
				if tc.found {
					r.Enclosures = []config.Enclosure{e}
				}
				return r, nil
			}, DiscoveryIdentity: func(p string) (string, error) { return p, nil }}
			var out, diag bytes.Buffer
			args := append([]string{"discover", "--config", filepath.Join(t.TempDir(), "config.yaml")}, tc.args...)
			code := Run(context.Background(), args, &out, &diag, opts)
			want := 3
			if tc.found {
				want = 0
			}
			if code != want {
				t.Fatalf("code %d: %s", code, diag.String())
			}
			if strings.Contains(diag.String(), "input/output error") != tc.details {
				t.Fatal(diag.String())
			}
			if !tc.details && !strings.Contains(diag.String(), "Skipped 1 ports") {
				t.Fatal(diag.String())
			}
			if tc.found {
				if !strings.Contains(diag.String(), "preview (not saved)") {
					t.Fatal(diag.String())
				}
				if _, err := config.Decode(bytes.NewReader(out.Bytes())); err != nil {
					t.Fatal(err)
				}
			} else if out.Len() != 0 {
				t.Fatal("unexpected config output")
			}
		})
	}
}

func TestDiscoverProfilesPreserveExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	e := config.DefaultEnclosure()
	e.ID, e.Port, e.Baud, e.RequiredSensors = "existing", "/dev/existing", 57600, []int{1, 3}
	opts := Options{Discover: func(context.Context, []string, int) (discovery.Result, error) {
		return discovery.Result{Enclosures: []config.Enclosure{e}}, nil
	}, DiscoveryIdentity: func(p string) (string, error) { return p, nil }}
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"discover", "--config", path, "--write"}, &out, &diag, opts); code != 0 {
		t.Fatal(code, diag.String())
	}
	out.Reset()
	opts.Discover = func(context.Context, []string, int) (discovery.Result, error) {
		added := e
		added.ID, added.Port = "new", "/dev/new"
		return discovery.Result{Enclosures: []config.Enclosure{e, added}}, nil
	}
	if code := Run(context.Background(), []string{"discover", "--config", path, "--profile", "quiet", "--write"}, &out, &diag, opts); code != 0 {
		t.Fatal(code, diag.String())
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Enclosures) != 2 || c.Enclosures[0].SafeSpeed != 100 || c.Enclosures[0].Curve[3].Speed != 75 {
		t.Fatalf("existing config changed: %+v", c)
	}
	q := c.Enclosures[1]
	if q.SafeSpeed != 15 || q.MinSpeed != 10 || q.Curve[3].Speed != 15 || q.Curve[4].Speed != 20 || q.Baud != 57600 || len(q.RequiredSensors) != 2 || q.RequiredSensors[1] != 3 {
		t.Fatalf("incorrect generated quiet config: %+v", q)
	}
	opts.Discover = func(context.Context, []string, int) (discovery.Result, error) {
		t.Fatal("invalid profile opened hardware")
		return discovery.Result{}, nil
	}
	if code := Run(context.Background(), []string{"discover", "--profile", "unknown"}, &out, &diag, opts); code != 2 {
		t.Fatal(code)
	}
}
