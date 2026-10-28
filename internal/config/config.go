// Package config defines the shared daemon and discovery configuration contract.
package config

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"
)

const DefaultPath = "/etc/sc200/config.yaml"
const MaxSpeed = 100

type Config struct {
	DryRun          bool          `yaml:"dry_run"`
	HTTP            HTTP          `yaml:"http"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	Enclosures      []Enclosure   `yaml:"enclosures"`
}
type HTTP struct {
	Listen string `yaml:"listen"`
}
type Point struct {
	Temperature float64 `yaml:"temperature"`
	Speed       int     `yaml:"speed"`
}
type Enclosure struct {
	ID                  string        `yaml:"id"`
	Enabled             bool          `yaml:"enabled"`
	Port                string        `yaml:"port"`
	Baud                int           `yaml:"baud"`
	PollInterval        time.Duration `yaml:"poll_interval"`
	CommandTimeout      time.Duration `yaml:"command_timeout"`
	StaleAfter          time.Duration `yaml:"stale_after"`
	RefreshInterval     time.Duration `yaml:"refresh_interval"`
	Cooldown            time.Duration `yaml:"cooldown"`
	ReconnectMin        time.Duration `yaml:"reconnect_min"`
	ReconnectMax        time.Duration `yaml:"reconnect_max"`
	RecoverySamples     int           `yaml:"recovery_samples"`
	MinSpeed            int           `yaml:"min_speed"`
	SafeSpeed           int           `yaml:"safe_speed"`
	CriticalTemperature float64       `yaml:"critical_temperature"`
	Hysteresis          float64       `yaml:"hysteresis"`
	Curve               []Point       `yaml:"curve"`
	// nil selects every installed sensor from the first complete sample. An explicit empty list is invalid.
	RequiredSensors []int `yaml:"required_sensors,omitempty"`
}

func DefaultEnclosure() Enclosure {
	return Enclosure{Enabled: true, Baud: 38400, PollInterval: 10 * time.Second, CommandTimeout: 2 * time.Second, StaleAfter: 30 * time.Second, RefreshInterval: 30 * time.Second, Cooldown: 30 * time.Second, ReconnectMin: time.Second, ReconnectMax: 30 * time.Second, RecoverySamples: 3, MinSpeed: 20, SafeSpeed: 100, CriticalTemperature: 45, Hysteresis: 2, Curve: []Point{{25, 20}, {30, 30}, {35, 50}, {40, 75}, {45, 100}}}
}
func (e *Enclosure) UnmarshalYAML(n *yaml.Node) error {
	type plain Enclosure
	v := plain(DefaultEnclosure())
	data, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err = d.Decode(&v); err != nil {
		return err
	}
	*e = Enclosure(v)
	return nil
}

// Default returns the shared global defaults used by loading and discovery.
func Default() Config {
	return Config{HTTP: HTTP{Listen: "127.0.0.1:9700"}, ShutdownTimeout: 5 * time.Second}
}

// Decode rejects unknown fields, duplicate keys and multiple YAML documents.
func Decode(r io.Reader) (Config, error) {
	c := Default()
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("configuration must contain exactly one document")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultPath
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	c, err := Decode(f)
	if err != nil {
		return Config{}, fmt.Errorf("configuration %s: %w", path, err)
	}
	return c, nil
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.HTTP.Listen); err != nil {
		return fmt.Errorf("http.listen: %w", err)
	}
	_, port, _ := net.SplitHostPort(c.HTTP.Listen)
	if p, err := net.LookupPort("tcp", port); err != nil || p < 1 {
		return fmt.Errorf("http.listen requires a valid nonzero port")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown_timeout must be positive")
	}
	ids, ports := map[string]bool{}, map[string]bool{}
	enabled := 0
	for _, e := range c.Enclosures {
		if !idPattern.MatchString(e.ID) {
			return fmt.Errorf("invalid enclosure id %q", e.ID)
		}
		if ids[e.ID] {
			return fmt.Errorf("duplicate enclosure id %q", e.ID)
		}
		ids[e.ID] = true
		if !filepath.IsAbs(e.Port) || filepath.Clean(e.Port) == "/" {
			return fmt.Errorf("enclosure %s: port must be an absolute device path", e.ID)
		}
		port := filepath.Clean(e.Port)
		if ports[port] {
			return fmt.Errorf("duplicate port %q", port)
		}
		ports[port] = true
		if err := e.Validate(); err != nil {
			return fmt.Errorf("enclosure %s: %w", e.ID, err)
		}
		if e.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return fmt.Errorf("at least one enabled enclosure is required")
	}
	return nil
}
func (e Enclosure) Validate() error {
	switch e.Baud {
	case 9600, 19200, 38400, 57600, 115200:
	default:
		return fmt.Errorf("unsupported baud %d", e.Baud)
	}
	for name, d := range map[string]time.Duration{"poll_interval": e.PollInterval, "command_timeout": e.CommandTimeout, "stale_after": e.StaleAfter, "refresh_interval": e.RefreshInterval, "cooldown": e.Cooldown, "reconnect_min": e.ReconnectMin, "reconnect_max": e.ReconnectMax} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if e.CommandTimeout >= e.PollInterval || e.StaleAfter <= e.PollInterval || e.CommandTimeout >= e.StaleAfter-e.PollInterval {
		return fmt.Errorf("require command_timeout < poll_interval and poll_interval + command_timeout < stale_after")
	}
	if e.ReconnectMax < e.ReconnectMin {
		return fmt.Errorf("reconnect_max must be >= reconnect_min")
	}
	if e.RecoverySamples < 1 {
		return fmt.Errorf("recovery_samples must be positive")
	}
	if e.MinSpeed < 1 || e.MinSpeed > 100 || e.SafeSpeed < e.MinSpeed || e.SafeSpeed > 100 {
		return fmt.Errorf("speed bounds must be within 1..100")
	}
	if !finite(e.CriticalTemperature) || !finite(e.Hysteresis) || e.Hysteresis < 0 {
		return fmt.Errorf("critical_temperature and nonnegative hysteresis must be finite")
	}
	if len(e.Curve) < 2 {
		return fmt.Errorf("curve requires at least two points")
	}
	for i, p := range e.Curve {
		if !finite(p.Temperature) || p.Speed < e.MinSpeed || p.Speed > MaxSpeed {
			return fmt.Errorf("curve point %d has invalid temperature or unsafe speed", i)
		}
		if i > 0 && (p.Temperature <= e.Curve[i-1].Temperature || p.Speed < e.Curve[i-1].Speed) {
			return fmt.Errorf("curve must have increasing temperatures and nondecreasing speeds")
		}
	}
	if e.CriticalTemperature <= e.Curve[0].Temperature || e.CriticalTemperature > e.Curve[len(e.Curve)-1].Temperature {
		return fmt.Errorf("critical_temperature must be above curve minimum and no higher than its maximum")
	}
	if e.RequiredSensors != nil && len(e.RequiredSensors) == 0 {
		return fmt.Errorf("required_sensors must not be empty")
	}
	seen := map[int]bool{}
	for _, s := range e.RequiredSensors {
		if s < 0 || seen[s] {
			return fmt.Errorf("required_sensors must contain unique nonnegative indices")
		}
		seen[s] = true
	}
	return nil
}
