package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/MrZoidberg/sc200/internal/config"
)

func setupKey(t *testing.T, m setupModel, code rune) (setupModel, tea.Cmd) {
	t.Helper()
	key := tea.KeyPressMsg{Code: code}
	if code == ' ' {
		key.Text = " "
	}
	next, cmd := m.Update(key)
	return next.(setupModel), cmd
}

func TestSetupReviewRequiresEnabledDeviceAndExplicitStart(t *testing.T) {
	e := config.DefaultEnclosure()
	e.ID = "shelf"
	e.Port = "/dev/ttyS0"
	e.Enabled = false
	c := config.Default()
	c.Enclosures = []config.Enclosure{e}
	m := setupModel{phase: "devices", cfg: c}
	m, _ = setupKey(t, m, tea.KeyEnter)
	if m.phase != "devices" || !strings.Contains(m.message, "enabled") {
		t.Fatalf("disabled device accepted: %+v", m)
	}
	m, _ = setupKey(t, m, ' ')
	m, _ = setupKey(t, m, 'q')
	if m.cfg.Enclosures[0].SafeSpeed != 15 {
		t.Fatal("quiet profile not applied")
	}
	m, _ = setupKey(t, m, tea.KeyEnter)
	if m.phase != "review" || m.start || m.accepted {
		t.Fatalf("unexpected review: %+v", m)
	}
	m, _ = setupKey(t, m, 's')
	m, cmd := setupKey(t, m, tea.KeyEnter)
	if !m.start || !m.accepted || cmd == nil {
		t.Fatal("explicit save/start lost")
	}
}

func TestSetupCustomValidationAndCancel(t *testing.T) {
	e := config.DefaultEnclosure()
	e.ID = "shelf"
	e.Port = "/dev/ttyS0"
	c := config.Default()
	c.Enclosures = []config.Enclosure{e}
	m := setupModel{phase: "devices", cfg: c}
	m, _ = setupKey(t, m, 'c')
	values := []string{"10", "15", "45", "2", "25:10,30:10,35:12,40:15,41:20,45:20"}
	for _, v := range values {
		m.input = v
		m, _ = setupKey(t, m, tea.KeyEnter)
	}
	if m.phase != "devices" || m.cfg.Enclosures[0].MinSpeed != 10 || len(m.cfg.Enclosures[0].Curve) != 6 {
		t.Fatalf("custom not applied: %+v", m)
	}
	m, _ = setupKey(t, m, 'c')
	m.input = "0"
	m, _ = setupKey(t, m, tea.KeyEnter)
	for _, v := range values[1:] {
		m.input = v
		m, _ = setupKey(t, m, tea.KeyEnter)
	}
	if m.phase != "custom" || !strings.Contains(m.message, "speed bounds") {
		t.Fatal("invalid custom accepted")
	}
	m, _ = setupKey(t, m, tea.KeyEscape)
	if m.cfg.Enclosures[0].MinSpeed != 10 {
		t.Fatal("discard altered settings")
	}
	m, cmd := setupKey(t, m, tea.KeyEscape)
	if m.accepted || cmd == nil {
		t.Fatal("cancel saved settings")
	}
}

func TestSetupCustomParseErrors(t *testing.T) {
	for field, input := range []string{"bad", "bad", "bad", "bad", "bad"} {
		m := setupModel{field: field, input: input}
		if err := m.setCustom(); err == nil {
			t.Fatalf("field %d accepted invalid input", field)
		}
	}
	for _, input := range []string{"oops:20", "25:oops"} {
		m := setupModel{field: 4, input: input}
		if m.setCustom() == nil {
			t.Fatal("invalid curve accepted")
		}
	}
}

func TestSetupEndToEnd(t *testing.T) {
	for _, mode := range []string{"save", "start", "cancel", "scan-error", "ui-error", "start-error", "stale", "no-devices"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			e := config.DefaultEnclosure()
			e.ID = "shelf"
			e.Port = "/dev/a"
			var out, diag bytes.Buffer
			var serviceArgs []string
			o := Options{
				SetupInput:        strings.NewReader(""),
				DiscoveryIdentity: func(p string) (string, error) { return p, nil },
				Discover: func(context.Context, []string, int) (discovery.Result, error) {
					if mode == "scan-error" {
						return discovery.Result{}, errors.New("locked")
					}
					if mode == "no-devices" {
						return discovery.Result{}, nil
					}
					return discovery.Result{Enclosures: []config.Enclosure{e}}, nil
				},
				Systemctl: func(_ context.Context, args ...string) error {
					serviceArgs = args
					if mode == "start-error" {
						return errors.New("start failed")
					}
					return nil
				},
				SetupUI: func(model tea.Model) (tea.Model, error) {
					if mode == "ui-error" {
						return model, errors.New("terminal failed")
					}
					m := model.(setupModel)
					if !strings.Contains(m.View().Content, "Discovering") {
						t.Fatal("missing progress")
					}
					loaded, _ := m.Update(m.Init()())
					m = loaded.(setupModel)
					if m.err != nil {
						return m, nil
					}
					if m.cfg.Enclosures[0].Enabled {
						t.Fatal("new device enabled without consent")
					}
					if !strings.Contains(m.View().Content, "/dev/a") {
						t.Fatal("missing device")
					}
					if mode == "cancel" {
						return m, nil
					}
					m, _ = setupKey(t, m, ' ')
					m, _ = setupKey(t, m, tea.KeyEnter)
					if !strings.Contains(m.View().Content, "Enable and start service: no") {
						t.Fatal("missing review")
					}
					if mode == "start" || mode == "start-error" {
						m, _ = setupKey(t, m, 's')
					}
					m, _ = setupKey(t, m, tea.KeyEnter)
					if mode == "stale" {
						if err := os.WriteFile(path, []byte("external"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return m, nil
				},
			}
			code := Run(context.Background(), []string{"--setup", "--config", path}, &out, &diag, o)
			want := 0
			switch mode {
			case "scan-error", "ui-error", "start-error", "stale", "no-devices":
				want = 1
			}
			if code != want {
				t.Fatalf("code %d, want %d: %s", code, want, diag.String())
			}
			if mode == "save" || mode == "start" || mode == "start-error" {
				cfg, err := config.Load(path)
				if err != nil || !cfg.Enclosures[0].Enabled {
					t.Fatalf("not saved: %v", err)
				}
			} else if mode != "stale" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("wrote config on failure/cancel")
				}
			}
			if mode == "start" || mode == "start-error" {
				if !reflect.DeepEqual(serviceArgs, []string{"enable", "--now", "sc200.service"}) {
					t.Fatal(serviceArgs)
				}
			} else if serviceArgs != nil {
				t.Fatal("started without consent")
			}
		})
	}
}

func TestSetupRetainsExistingDisabledAndGlobalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := config.DefaultEnclosure()
	old.ID = "existing"
	old.Port = "/dev/a"
	old.MinSpeed = 25
	old.Curve[0].Speed = 25
	identity := func(p string) (string, error) { return p, nil }
	if _, err := config.MergeFile(path, []config.Enclosure{old}, true, identity); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opts := Options{SetupInput: strings.NewReader(""), DiscoveryIdentity: identity, Discover: func(context.Context, []string, int) (discovery.Result, error) {
		return discovery.Result{Enclosures: []config.Enclosure{old}}, nil
	}}
	opts.SetupUI = func(model tea.Model) (tea.Model, error) {
		m := model.(setupModel)
		loaded, _ := m.Update(m.Init()())
		m = loaded.(setupModel)
		if m.err != nil {
			t.Fatal(m.err)
		}
		if !m.cfg.Enclosures[0].Enabled || m.cfg.Enclosures[0].MinSpeed != 25 {
			t.Fatal("reset existing configuration")
		}
		m.accepted = true
		return m, nil
	}
	if code := setup(context.Background(), path, &out, &out, opts); code != 0 {
		t.Fatal(out.String())
	}
}
