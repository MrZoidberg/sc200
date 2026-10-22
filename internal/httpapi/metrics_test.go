package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetrics(t *testing.T) {
	a, b := healthy("a"), healthy("b")
	a.Operations.Read = 7
	a.Errors.Command = 2
	b.State = control.Failsafe
	b.Healthy = false
	b.Fresh = false
	s := New("", &source{states: []control.Snapshot{a, b}}, BuildInfo{Version: "1.2.3", Commit: "abc", Date: "today"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := w.Body.String()
	for _, line := range []string{`sc200_temperature_celsius{enclosure="a",sensor="2"} 32`, `sc200_fan_rpm{enclosure="a",fan="3"} 2680`, `sc200_target_percent{enclosure="a"} 40`, `sc200_completed_command_percent{enclosure="a"} 30`, `sc200_telemetry_fresh{enclosure="b"} 0`, `sc200_failsafe_active{enclosure="b"} 1`, `sc200_telemetry_last_success_timestamp_seconds{enclosure="a"} 100`, `sc200_command_last_success_timestamp_seconds{enclosure="a"} 90`, `sc200_operations_total{enclosure="a",operation="read"} 7`, `sc200_errors_total{enclosure="a",operation="command"} 2`, `sc200_build_info{commit="abc",date="today",version="1.2.3"} 1`} {
		if !strings.Contains(body, line) {
			t.Errorf("missing %s in %s", line, body)
		}
	}
	if strings.Contains(body, `sc200_temperature_celsius{enclosure="b"`) || strings.Contains(body, `sc200_fan_rpm{enclosure="b"`) {
		t.Fatal("stale measurements exposed")
	}
}
func TestCollectorPedanticUnavailable(t *testing.T) {
	a := healthy("a")
	a.Telemetry.Temperatures[0].Available = false
	a.Telemetry.Fans[0].Available = false
	a.LastTelemetry = a.LastTelemetry.Add(-100000000000)
	r := prometheus.NewPedanticRegistry()
	r.MustRegister(newCollector(&source{states: []control.Snapshot{a}}, BuildInfo{}))
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "sc200_temperature_celsius" || f.GetName() == "sc200_fan_rpm" {
			t.Fatal("unavailable sample emitted")
		}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				switch l.GetName() {
				case "enclosure", "sensor", "fan", "operation", "version", "commit", "date":
				default:
					t.Fatal("unbounded label", l)
				}
			}
		}
	}
}
