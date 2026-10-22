// Package control implements thermal decisions independently of hardware and clocks.
package control

import (
	"math"
	"sort"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/protocol"
)

// PolicyState is retained per enclosure, including across transport reconnects.
// A worker must reset recovery and pending reductions on reconnect and supply ControlOK.
// RequiredSensors must never be reset on reconnect: missing sensors remain required.
type PolicyState struct {
	RequiredSensors     []int
	Target              int
	Recovery            int
	Healthy             bool
	RetainedTemperature float64
	ReductionTarget     int
	ReductionSince      time.Time
	ReductionPending    bool
}

// PolicyInput describes cached telemetry and current control-path health. NewSample
// is true exactly once per successful complete map transaction. Now and SampleTime
// should come from the same monotonic clock. ErrReason covers parse/transport errors.
type PolicyInput struct {
	Now, SampleTime time.Time
	Map             protocol.Map
	NewSample       bool
	ControlOK       bool
	ErrReason       string
}

type PolicyDecision struct {
	Target         int
	Healthy        bool
	Reason         string
	MaxTemperature float64
	HasTemperature bool
}

// EvaluatePolicy returns a new state without changing its arguments. Configuration
// must already have passed Validate. A target is a request, never proof of actuation.
func EvaluatePolicy(c config.Enclosure, previous PolicyState, in PolicyInput) (PolicyState, PolicyDecision) {
	s := previous
	s.RequiredSensors = append([]int(nil), previous.RequiredSensors...)
	d := PolicyDecision{Target: c.SafeSpeed, Reason: "recovering"}
	fail := func(reason string, speed int) (PolicyState, PolicyDecision) {
		s.Target, s.Recovery, s.Healthy = speed, 0, false
		s.ReductionPending = false
		d.Target, d.Reason = speed, reason
		return s, d
	}
	if in.ErrReason != "" {
		return fail(in.ErrReason, c.SafeSpeed)
	}
	if in.SampleTime.IsZero() || in.Now.Before(in.SampleTime) || in.Now.Sub(in.SampleTime) >= c.StaleAfter {
		return fail("stale_telemetry", c.SafeSpeed)
	}
	if c.RequiredSensors != nil {
		// Explicit configuration is authoritative, and is copied rather than aliased.
		s.RequiredSensors = append([]int(nil), c.RequiredSensors...)
	} else if len(s.RequiredSensors) == 0 {
		for _, t := range in.Map.Temperatures {
			if t.Installed {
				s.RequiredSensors = append(s.RequiredSensors, t.Index)
			}
		}
		sort.Ints(s.RequiredSensors)
	}
	temps := make(map[int]protocol.Temperature, len(in.Map.Temperatures))
	emergency := false
	for _, t := range in.Map.Temperatures {
		if _, ok := temps[t.Index]; ok {
			return fail("invalid_sensor", c.SafeSpeed)
		}
		temps[t.Index] = t
		if t.Installed && (t.Overtemperature || t.Available && float64(t.Celsius) >= c.CriticalTemperature) {
			emergency = true
		}
	}
	if emergency {
		return fail("critical_temperature", config.ShutdownSpeed)
	}
	if len(s.RequiredSensors) == 0 {
		return fail("missing_sensor", c.SafeSpeed)
	}
	maxTemp := math.Inf(-1)
	for _, index := range s.RequiredSensors {
		t, ok := temps[index]
		if !ok || !t.Installed {
			return fail("missing_sensor", c.SafeSpeed)
		}
		if !t.Healthy() {
			return fail("invalid_sensor", c.SafeSpeed)
		}
		maxTemp = math.Max(maxTemp, float64(t.Celsius))
	}
	d.MaxTemperature, d.HasTemperature = maxTemp, true
	installedFans := 0
	for _, f := range in.Map.Fans {
		if f.Installed {
			installedFans++
			if !f.Healthy() {
				return fail("fan_fault", config.ShutdownSpeed)
			}
		}
	}
	if installedFans == 0 {
		return fail("missing_fan", c.SafeSpeed)
	}
	if !in.ControlOK {
		return fail("control_failed", c.SafeSpeed)
	}
	if !s.Healthy {
		if in.NewSample {
			s.Recovery++
		}
		if s.Recovery < c.RecoverySamples {
			s.Target = c.SafeSpeed
			d.Target = c.SafeSpeed
			return s, d
		}
		s.Healthy = true
	}
	target := CurveSpeed(c, maxTemp)
	if s.Target == 0 {
		s.Target = c.SafeSpeed
	}
	if target > s.Target {
		s.Target = target
		s.RetainedTemperature = maxTemp
		s.ReductionPending = false
	} else if target == s.Target {
		s.RetainedTemperature = math.Max(s.RetainedTemperature, maxTemp)
		s.ReductionPending = false
	} else {
		// Evaluate the warmer edge too: the reduction must survive hysteresis and
		// be below the temperature which justified the retained target.
		lower := CurveSpeed(c, maxTemp+c.Hysteresis)
		if lower >= s.Target || maxTemp+c.Hysteresis > s.RetainedTemperature && s.RetainedTemperature != 0 {
			s.ReductionPending = false
		} else {
			if !s.ReductionPending || s.ReductionTarget != lower {
				s.ReductionPending = true
				s.ReductionTarget = lower
				s.ReductionSince = in.Now
			}
			if in.Now.Sub(s.ReductionSince) >= c.Cooldown {
				s.Target = lower
				s.RetainedTemperature = maxTemp + c.Hysteresis
				s.ReductionPending = false
			}
		}
	}
	d.Target, d.Healthy, d.Reason = s.Target, true, "healthy"
	return s, d
}

// CurveSpeed interpolates the configured curve, rounds toward more cooling, and
// clamps to the configured minimum and curve maximum (bounded by safe speed).
func CurveSpeed(c config.Enclosure, temperature float64) int {
	speed := float64(c.Curve[len(c.Curve)-1].Speed)
	if temperature <= c.Curve[0].Temperature {
		speed = float64(c.Curve[0].Speed)
	} else {
		for i := 1; i < len(c.Curve); i++ {
			a, b := c.Curve[i-1], c.Curve[i]
			if temperature <= b.Temperature {
				speed = float64(a.Speed) + (temperature-a.Temperature)*float64(b.Speed-a.Speed)/(b.Temperature-a.Temperature)
				break
			}
		}
	}
	return max(c.MinSpeed, min(c.SafeSpeed, int(math.Ceil(speed))))
}
