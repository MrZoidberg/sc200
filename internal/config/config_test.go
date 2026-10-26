package config

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

const minimal = "enclosures:\n  - id: first\n    port: /dev/ttyS0\n"

func TestDefaultsAndMultiple(t *testing.T) {
	c, err := Decode(strings.NewReader(minimal + "  - id: second\n    port: /dev/serial/by-id/other\n    required_sensors: [0, 3]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if DefaultPath != "/etc/sc200/config.yaml" || ShutdownSpeed != 100 {
		t.Fatal("shared paths/safety contract changed")
	}
	e := c.Enclosures[0]
	if c.HTTP.Listen != "127.0.0.1:9700" || c.ShutdownTimeout != 5*time.Second || !e.Enabled || e.Baud != 38400 || e.SafeSpeed != 100 || e.PollInterval != 10*time.Second || e.RequiredSensors != nil {
		t.Fatalf("incorrect defaults: %+v", c)
	}
	if c.Enclosures[1].RequiredSensors[1] != 3 {
		t.Fatal("sensor identity lost")
	}
	e.Curve[0].Speed = 99
	if DefaultEnclosure().Curve[0].Speed != 20 {
		t.Fatal("default curve aliases mutable state")
	}
}
func TestExample(t *testing.T) {
	b, err := os.ReadFile("../../configs/sc200.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(strings.NewReader(string(b))); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("disabled example must require commissioning: %v", err)
	}
	if _, err = Decode(strings.NewReader(strings.Replace(string(b), "enabled: false", "enabled: true", 1))); err != nil {
		t.Fatal(err)
	}
}
func TestStrictDecode(t *testing.T) {
	cases := []string{"", "[", "enclosures: []", "http: {typo: true}\n" + minimal, "shutdown_timeout: 0s\n" + minimal, "http: {listen: ':0'}\n" + minimal, "http: {listen: bad}\n" + minimal, "http: {listen: ':999999'}\n" + minimal, "http: {listen: ':9700', listen: ':9701'}\n" + minimal, minimal + "    typo: true\n", minimal + "    baud: 38400\n    baud: 9600\n", minimal + "---\n{}\n", minimal + "    enabled: false\n", minimal + "  - id: first\n    port: /dev/ttyS1\n", minimal + "  - id: second\n    port: /dev/../dev/ttyS0\n", minimal + "    command_timeout: 0s\n", minimal + "    poll_interval: .inf\n", minimal + "    cooldown: 9999999999999999999999h\n", minimal + "    hysteresis: .nan\n", minimal + "    critical_temperature: .inf\n", minimal + "    curve: [{temperature: .nan, speed: 100}]\n", minimal + "    required_sensors: []\n"}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(s)); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
	// YAML aliases must be resolved and validated just like their anchors.
	s := "enclosures:\n - &device {id: a, port: /dev/ttyS0}\n - *device\n"
	if _, err := Decode(strings.NewReader(s)); err == nil {
		t.Fatal("accepted aliased duplicate")
	}
}
func TestValidationBoundaries(t *testing.T) {
	cases := map[string]func(*Enclosure){
		"bad id": func(e *Enclosure) { e.ID = "../bad" }, "relative port": func(e *Enclosure) { e.Port = "ttyS0" }, "root port": func(e *Enclosure) { e.Port = "/" }, "baud": func(e *Enclosure) { e.Baud = 1 },
		"poll": func(e *Enclosure) { e.PollInterval = 0 }, "timeout": func(e *Enclosure) { e.CommandTimeout = -1 }, "stale": func(e *Enclosure) { e.StaleAfter = 0 }, "refresh": func(e *Enclosure) { e.RefreshInterval = 0 }, "cooldown": func(e *Enclosure) { e.Cooldown = 0 }, "reconnect min": func(e *Enclosure) { e.ReconnectMin = 0 }, "reconnect max": func(e *Enclosure) { e.ReconnectMax = 0 }, "reconnect order": func(e *Enclosure) { e.ReconnectMax = time.Millisecond }, "recovery": func(e *Enclosure) { e.RecoverySamples = 0 },
		"timeout relation": func(e *Enclosure) { e.CommandTimeout = e.PollInterval }, "stale relation": func(e *Enclosure) { e.StaleAfter = e.PollInterval }, "transaction budget": func(e *Enclosure) { e.StaleAfter = e.PollInterval + e.CommandTimeout },
		"zero min": func(e *Enclosure) { e.MinSpeed = 0 }, "max min": func(e *Enclosure) { e.MinSpeed = 101 }, "safe min": func(e *Enclosure) { e.SafeSpeed = 19 }, "safe max": func(e *Enclosure) { e.SafeSpeed = 101 }, "unsafe curve": func(e *Enclosure) { e.SafeSpeed = 99 },
		"hysteresis": func(e *Enclosure) { e.Hysteresis = -1 }, "nan hysteresis": func(e *Enclosure) { e.Hysteresis = math.NaN() }, "inf critical": func(e *Enclosure) { e.CriticalTemperature = math.Inf(1) }, "critical low": func(e *Enclosure) { e.CriticalTemperature = 25 }, "critical high": func(e *Enclosure) { e.CriticalTemperature = 46 },
		"short curve": func(e *Enclosure) { e.Curve = e.Curve[:1] }, "infinite point": func(e *Enclosure) { e.Curve[0].Temperature = math.Inf(-1) }, "curve low": func(e *Enclosure) { e.Curve[0].Speed = 19 }, "curve temp": func(e *Enclosure) { e.Curve[1].Temperature = 25 }, "curve speed": func(e *Enclosure) { e.Curve[2].Speed = 20 }, "negative sensor": func(e *Enclosure) { e.RequiredSensors = []int{-1} }, "duplicate sensor": func(e *Enclosure) { e.RequiredSensors = []int{1, 1} }, "empty sensors": func(e *Enclosure) { e.RequiredSensors = []int{} },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			e := DefaultEnclosure()
			e.ID = "one"
			e.Port = "/dev/ttyS0"
			modify(&e)
			c := Config{HTTP: HTTP{Listen: "127.0.0.1:9700"}, ShutdownTimeout: time.Second, Enclosures: []Enclosure{e}}
			if err := c.Validate(); err == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
	for _, baud := range []int{9600, 19200, 38400, 57600, 115200} {
		e := DefaultEnclosure()
		e.Baud = baud
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLoad(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path + ".missing"); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unknown: true"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatal(err)
	}
	if _, err := Load(""); err != nil && !strings.Contains(err.Error(), DefaultPath) {
		t.Fatal(err)
	}
}

// Keep the operator's copy/paste multi-enclosure example executable as documentation evolves.
func TestOperationsExample(t *testing.T) {
	b, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(b), "```yaml\n")
	if !found {
		t.Fatal("missing documented configuration")
	}
	example, _, found := strings.Cut(rest, "```")
	if !found {
		t.Fatal("unterminated documented configuration")
	}
	c, err := Decode(strings.NewReader(example))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Enclosures) != 2 || !c.Enclosures[0].Enabled || !c.Enclosures[1].Enabled || len(c.Enclosures[0].RequiredSensors) != 4 {
		t.Fatalf("documented multi-enclosure configuration changed: %+v", c)
	}
}

func TestDryRun(t *testing.T) {
	for _, value := range []string{"", "dry_run: false\n", "dry_run: true\n"} {
		c, err := Decode(strings.NewReader(value + minimal))
		if err != nil {
			t.Fatal(err)
		}
		if c.DryRun != strings.Contains(value, "true") {
			t.Fatalf("%q: %+v", value, c)
		}
	}
	if _, err := Decode(strings.NewReader("dry_run: invalid\n" + minimal)); err == nil {
		t.Fatal("accepted invalid dry_run")
	}
}
