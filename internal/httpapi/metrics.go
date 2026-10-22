// Package httpapi serves cached enclosure state without hardware access.
package httpapi

import (
	"strconv"
	"time"

	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/prometheus/client_golang/prometheus"
)

// Source must return an independent snapshot of enabled enclosures only.
type Source interface{ Snapshots() []control.Snapshot }
type BuildInfo struct{ Version, Commit, Date string }

type collector struct {
	source Source
	build  BuildInfo
	desc   map[string]*prometheus.Desc
}

func newCollector(source Source, build BuildInfo) *collector {
	c := &collector{source: source, build: build, desc: map[string]*prometheus.Desc{}}
	for _, name := range []string{"target_percent", "completed_command_percent", "enclosure_healthy", "failsafe_active", "telemetry_fresh", "telemetry_last_success_timestamp_seconds", "command_last_success_timestamp_seconds"} {
		c.desc[name] = prometheus.NewDesc("sc200_"+name, "Cached enclosure "+name+".", []string{"enclosure"}, nil)
	}
	c.desc["temperature_celsius"] = prometheus.NewDesc("sc200_temperature_celsius", "Current temperature in degrees Celsius.", []string{"enclosure", "sensor"}, nil)
	c.desc["fan_rpm"] = prometheus.NewDesc("sc200_fan_rpm", "Current fan speed in revolutions per minute.", []string{"enclosure", "fan"}, nil)
	for _, name := range []string{"operations_total", "errors_total"} {
		c.desc[name] = prometheus.NewDesc("sc200_"+name, "Bounded enclosure "+name+".", []string{"enclosure", "operation"}, nil)
	}
	c.desc["build_info"] = prometheus.NewDesc("sc200_build_info", "Build metadata.", []string{"version", "commit", "date"}, nil)
	return c
}
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.desc {
		ch <- d
	}
}
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	emit := func(name string, v float64, labels ...string) {
		kind := prometheus.GaugeValue
		if name == "operations_total" || name == "errors_total" {
			kind = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(c.desc[name], kind, v, labels...)
	}
	emit("build_info", 1, c.build.Version, c.build.Commit, c.build.Date)
	for _, s := range c.source.Snapshots() {
		emit("target_percent", float64(s.Target), s.ID)
		emit("completed_command_percent", float64(s.CompletedCommand), s.ID)
		emit("enclosure_healthy", boolean(s.Healthy), s.ID)
		emit("failsafe_active", boolean(s.State == control.Failsafe), s.ID)
		emit("telemetry_fresh", boolean(s.Fresh), s.ID)
		emit("telemetry_last_success_timestamp_seconds", unix(s.LastTelemetry), s.ID)
		emit("command_last_success_timestamp_seconds", unix(s.LastCommand), s.ID)
		for _, set := range []struct {
			name   string
			counts control.Counters
		}{{"operations_total", s.Operations}, {"errors_total", s.Errors}} {
			for i, v := range []uint64{set.counts.Open, set.counts.Read, set.counts.Command, set.counts.Fallback, set.counts.Shutdown} {
				emit(set.name, float64(v), s.ID, []string{"open", "read", "command", "fallback", "shutdown"}[i])
			}
		}
		if !s.Fresh {
			continue
		}
		for _, t := range s.Telemetry.Temperatures {
			if t.Installed && t.Healthy() {
				emit("temperature_celsius", float64(t.Celsius), s.ID, strconv.Itoa(t.Index))
			}
		}
		for _, f := range s.Telemetry.Fans {
			if f.Installed && f.Healthy() {
				emit("fan_rpm", float64(f.RPM), s.ID, strconv.Itoa(f.Index))
			}
		}
	}
}
func boolean(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
func unix(v time.Time) float64 {
	if v.IsZero() {
		return 0
	}
	return float64(v.UnixNano()) / 1e9
}
