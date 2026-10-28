package control

import (
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/protocol"
)

func sample(now time.Time, values ...int) PolicyInput {
	m := protocol.Map{Fans: []protocol.Fan{{Element: protocol.Element{Index: 0, Status: 1, Installed: true}, Available: true, RPM: 2000}}}
	for i, v := range values {
		m.Temperatures = append(m.Temperatures, protocol.Temperature{Element: protocol.Element{Index: i, Status: 1, Installed: true}, Celsius: v, Available: true})
	}
	return PolicyInput{Now: now, SampleTime: now, Map: m, NewSample: true, ControlOK: true}
}
func TestCurve(t *testing.T) {
	c := config.DefaultEnclosure()
	for _, tt := range []struct {
		temp  float64
		speed int
	}{{-20, 20}, {25, 20}, {25.1, 21}, {27, 24}, {30, 30}, {32, 38}, {35, 50}, {40, 75}, {45, 100}, {100, 100}} {
		if got := CurveSpeed(c, tt.temp); got != tt.speed {
			t.Errorf("%g: got %d want %d", tt.temp, got, tt.speed)
		}
	}
	c.MinSpeed = 25
	if CurveSpeed(c, -20) != 25 {
		t.Fatal("minimum not enforced")
	}
}
func TestRecoveryCooldownAndRamps(t *testing.T) {
	c := config.DefaultEnclosure()
	now := time.Unix(100, 0)
	s := PolicyState{}
	for i := 0; i < 3; i++ {
		var d PolicyDecision
		s, d = EvaluatePolicy(c, s, sample(now, 30, 32))
		if d.Healthy != (i == 2) || d.Target != 100 {
			t.Fatalf("sample %d: %+v", i, d)
		}
	}
	// Recovery enters healthy but reductions still wait for a stable cooldown.
	s, d := EvaluatePolicy(c, s, sample(now.Add(c.Cooldown), 30, 32))
	if d.Target != 46 {
		t.Fatalf("cooldown: %+v", d)
	}
	s, d = EvaluatePolicy(c, s, sample(now.Add(c.Cooldown+time.Second), 40, 40))
	if d.Target != 75 {
		t.Fatalf("rise delayed: %+v", d)
	}
	s, d = EvaluatePolicy(c, s, sample(now.Add(c.Cooldown+2*time.Second), 39, 39))
	if d.Target != 75 || s.ReductionPending {
		t.Fatalf("hysteresis: %+v %+v", s, d)
	}
	s, _ = EvaluatePolicy(c, s, sample(now.Add(c.Cooldown+3*time.Second), 30, 30))
	s, d = EvaluatePolicy(c, s, sample(now.Add(2*c.Cooldown+3*time.Second), 30, 30))
	if d.Target != 38 {
		t.Fatalf("cooling: %+v", d)
	}
	_, d = EvaluatePolicy(c, s, sample(now.Add(2*c.Cooldown+4*time.Second), 45, 45))
	if d.Target != 100 || d.Healthy {
		t.Fatalf("spike: %+v", d)
	}
}
func TestFailuresAndSensorMemory(t *testing.T) {
	c := config.DefaultEnclosure()
	now := time.Unix(100, 0)
	base, _ := EvaluatePolicy(c, PolicyState{}, sample(now, 25, 26))
	tests := []struct {
		name, reason string
		change       func(*PolicyInput)
	}{
		{"missing", "missing_sensor", func(i *PolicyInput) { i.Map.Temperatures = i.Map.Temperatures[:1] }},
		{"unavailable", "invalid_sensor", func(i *PolicyInput) { i.Map.Temperatures[0].Available = false }},
		{"failed", "invalid_sensor", func(i *PolicyInput) { i.Map.Temperatures[0].Failed = true }},
		{"uninstalled", "missing_sensor", func(i *PolicyInput) { i.Map.Temperatures[0].Installed = false }},
		{"duplicate", "invalid_sensor", func(i *PolicyInput) { i.Map.Temperatures[1].Index = 0 }},
		{"overtemp", "critical_temperature", func(i *PolicyInput) { i.Map.Temperatures[0].Overtemperature = true }},
		{"critical", "critical_temperature", func(i *PolicyInput) { i.Map.Temperatures[0].Celsius = 45 }},
		{"fan", "fan_fault", func(i *PolicyInput) { i.Map.Fans[0].Failed = true }},
		{"fan absent", "missing_fan", func(i *PolicyInput) { i.Map.Fans = nil }},
		{"control", "control_failed", func(i *PolicyInput) { i.ControlOK = false }},
		{"parse", "parse_error", func(i *PolicyInput) { i.ErrReason = "parse_error" }},
		{"stale", "stale_telemetry", func(i *PolicyInput) { i.Now = now.Add(c.StaleAfter) }},
		{"missing time", "stale_telemetry", func(i *PolicyInput) { i.SampleTime = time.Time{} }},
		{"future time", "stale_telemetry", func(i *PolicyInput) { i.SampleTime = now.Add(time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := sample(now, 25, 26)
			tt.change(&i)
			s, d := EvaluatePolicy(c, base, i)
			if d.Reason != tt.reason || d.Target != 100 || s.Recovery != 0 || d.Healthy {
				t.Fatalf("%+v %+v", s, d)
			}
		})
	}
	// An added sensor does not remove the remembered required identities.
	s, _ := EvaluatePolicy(c, base, sample(now, 25, 26, 27))
	if len(s.RequiredSensors) != 2 {
		t.Fatal(s.RequiredSensors)
	}
	c.RequiredSensors = []int{2}
	s, d := EvaluatePolicy(c, base, sample(now, 25, 26, 27))
	if len(s.RequiredSensors) != 1 || s.RequiredSensors[0] != 2 || d.Reason != "recovering" {
		t.Fatalf("%+v %+v", s, d)
	}
	c.RequiredSensors = nil
	_, d = EvaluatePolicy(c, PolicyState{}, sample(now))
	if d.Reason != "missing_sensor" {
		t.Fatal(d)
	}
}
func TestOscillationAndControlRecovery(t *testing.T) {
	c := config.DefaultEnclosure()
	c.RecoverySamples = 1
	now := time.Unix(100, 0)
	s := PolicyState{Target: 75, Healthy: true, RetainedTemperature: 40}
	for n := 0; n < 10; n++ {
		temp := 30
		if n%2 == 1 {
			temp = 31
		}
		var d PolicyDecision
		s, d = EvaluatePolicy(c, s, sample(now.Add(time.Duration(n)*10*time.Second), temp))
		if d.Target != 75 {
			t.Fatal("unstable target reduced", d)
		}
	}
	i := sample(now, 25)
	i.ControlOK = false
	s, _ = EvaluatePolicy(c, s, i)
	i.ControlOK = true
	i.NewSample = false
	s, d := EvaluatePolicy(c, s, i)
	if d.Healthy || s.Recovery != 0 {
		t.Fatal("cached map recovered failed control", d)
	}
	i.NewSample = true
	s, d = EvaluatePolicy(c, s, i)
	if !d.Healthy {
		t.Fatal(d)
	}
	// Neither input state nor configuration slices are mutated.
	old := PolicyState{RequiredSensors: []int{0}, Target: 100}
	i = sample(now, 25)
	next, _ := EvaluatePolicy(c, old, i)
	next.RequiredSensors[0] = 42
	if old.RequiredSensors[0] != 0 {
		t.Fatal("state alias")
	}
}

func TestQuietProfilePolicy(t *testing.T) {
	c, err := config.EnclosureProfile("quiet")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ temperature, speed int }{{20, 10}, {30, 10}, {35, 12}, {38, 14}, {40, 15}, {41, 20}, {44, 20}} {
		if got := CurveSpeed(c, float64(tc.temperature)); got != tc.speed {
			t.Fatalf("%d°C: got %d want %d", tc.temperature, got, tc.speed)
		}
	}
	// A custom curve may reach 100% even with a 15% fallback.
	custom := c
	custom.Curve = []config.Point{{Temperature: 25, Speed: 10}, {Temperature: 45, Speed: 100}}
	if CurveSpeed(custom, 50) != 100 {
		t.Fatal("fallback clamps curve")
	}
	now := time.Unix(100, 0)
	c.RecoverySamples = 1
	s, d := EvaluatePolicy(c, PolicyState{}, sample(now, 40))
	if !d.Healthy || d.Target != 15 {
		t.Fatal(d)
	}
	s, d = EvaluatePolicy(c, s, sample(now.Add(time.Second), 41))
	if d.Target != 20 {
		t.Fatal("rise above fallback delayed", d)
	}
	s, d = EvaluatePolicy(c, s, sample(now.Add(2*time.Second), 40))
	if d.Target != 20 || s.ReductionPending {
		t.Fatal("hysteresis ignored", d)
	}
	s, _ = EvaluatePolicy(c, s, sample(now.Add(3*time.Second), 38))
	_, d = EvaluatePolicy(c, s, sample(now.Add(3*time.Second+c.Cooldown), 38))
	if d.Target != 15 {
		t.Fatal("cooldown reduction", d)
	}
	for _, failure := range []string{"unavailable", "stale", "critical", "fan"} {
		i := sample(now, 40)
		want := 15
		switch failure {
		case "unavailable":
			i.Map.Temperatures[0].Available = false
		case "stale":
			i.Now = now.Add(c.StaleAfter)
		case "critical":
			i.Map.Temperatures[0].Celsius = 45
			want = 100
		case "fan":
			i.Map.Fans[0].Failed = true
			want = 100
		}
		_, d := EvaluatePolicy(c, s, i)
		if d.Target != want || d.Healthy {
			t.Fatalf("%s: %+v", failure, d)
		}
	}
}
