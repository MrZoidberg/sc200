package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"github.com/charmbracelet/x/ansi"
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

// Unused UART failures must not displace the device controls on an SSH terminal.
func TestSetupLayoutWithUnusedUARTs(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 60, Height: 16}, {Width: 120, Height: 40}} {
		for _, phase := range []string{"devices", "review"} {
			t.Run(fmt.Sprintf("%s-%dx%d", phase, size.Width, size.Height), func(t *testing.T) {
				m := setupModel{phase: phase, cfg: config.Default(), message: "Settings applied."}
				for i := range 20 {
					e := config.DefaultEnclosure()
					e.ID = fmt.Sprintf("shelf-%02d", i)
					e.Port = fmt.Sprintf("/dev/serial/by-id/%s-%02d", strings.Repeat("long-device-name", 5), i)
					m.cfg.Enclosures = append(m.cfg.Enclosures, e)
				}
				for i := range 31 {
					m.outcomes = append(m.outcomes, discovery.Outcome{Port: fmt.Sprintf("/dev/ttyS%d", i), Err: errors.New("input/output error")})
				}
				next, _ := m.Update(size)
				m = next.(setupModel)
				for i := range 20 {
					content := m.View().Content
					if !strings.Contains(content, fmt.Sprintf("> [x] shelf-%02d", i)) {
						t.Fatalf("selected device hidden: %s", content)
					}
					if strings.Contains(content, "input/output error") {
						t.Fatal("individual probe failures clutter selection")
					}
					if phase == "devices" && !strings.Contains(content, "Skipped 31") {
						t.Fatal("missing failure summary")
					}
					if !strings.Contains(content, "Esc: cancel") {
						t.Fatal("cancel control hidden")
					}
					lines := strings.Split(content, "\n")
					if len(lines) > size.Height {
						t.Fatalf("%d lines exceed %d rows:\n%s", len(lines), size.Height, content)
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > size.Width {
							t.Fatalf("line wraps beyond %d columns: %s", size.Width, line)
						}
					}
					if i < 19 {
						m, _ = setupKey(t, m, tea.KeyDown)
					}
				}
				next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
				m = next.(setupModel)
				if !strings.Contains(m.View().Content, "> [x] shelf-19") {
					t.Fatal("resize lost selection")
				}
			})
		}
	}
}
