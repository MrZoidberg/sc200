package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server owns an isolated metrics registry and a bounded HTTP server.
type Server struct{ server *http.Server }

func New(address string, source Source, build BuildInfo) *Server {
	registry := prometheus.NewRegistry()
	registry.MustRegister(newCollector(source, build))
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(healthStatus(source.Snapshots())) })
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		snapshots := source.Snapshots()
		status := healthStatus(snapshots)
		response := Health{Healthy: status == http.StatusOK, Enclosures: make([]Enclosure, 0, len(snapshots))}
		for _, s := range snapshots {
			e := Enclosure{ID: s.ID, State: s.State, Reason: s.Reason, Healthy: s.Healthy, Fresh: s.Fresh, TransitionTime: s.TransitionTime, Target: s.Target, CompletedCommand: s.CompletedCommand, LastTelemetry: optionalTime(s.LastTelemetry), LastCommand: optionalTime(s.LastCommand), Temperatures: []Reading{}, Fans: []Reading{}, PowerSupplies: []PowerSupply{}}
			for _, t := range s.Telemetry.Temperatures {
				v := s.Fresh && t.Installed && t.Healthy()
				e.Temperatures = append(e.Temperatures, reading(t.Index, v, t.Celsius))
			}
			for _, f := range s.Telemetry.Fans {
				v := s.Fresh && f.Installed && f.Healthy()
				e.Fans = append(e.Fans, reading(f.Index, v, f.RPM))
			}
			for _, psu := range s.Telemetry.PowerSupplies {
				e.PowerSupplies = append(e.PowerSupplies, PowerSupply{Index: psu.Index, Installed: psu.Installed, Status: psu.Status, Healthy: s.Fresh && psu.Installed && psu.Healthy()})
			}
			response.Enclosures = append(response.Enclosures, e)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return &Server{server: &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}}
}
func (s *Server) Handler() http.Handler { return s.server.Handler }

// Serve returns listener and serving failures to the process lifecycle owner.
func (s *Server) Serve(listener net.Listener) error {
	err := s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }
func healthStatus(s []control.Snapshot) int {
	if len(s) == 0 {
		return http.StatusServiceUnavailable
	}
	for _, e := range s {
		if !e.Healthy || !e.Fresh || e.State != control.Healthy {
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusOK
}
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type Health struct {
	Healthy    bool        `json:"healthy"`
	Enclosures []Enclosure `json:"enclosures"`
}
type Enclosure struct {
	ID               string        `json:"id"`
	State            control.State `json:"state"`
	Reason           string        `json:"reason"`
	Healthy          bool          `json:"healthy"`
	Fresh            bool          `json:"fresh"`
	TransitionTime   time.Time     `json:"transition_time"`
	Target           int           `json:"target_percent"`
	CompletedCommand int           `json:"completed_command_percent"`
	LastTelemetry    *time.Time    `json:"last_telemetry"`
	LastCommand      *time.Time    `json:"last_command"`
	Temperatures     []Reading     `json:"temperatures"`
	Fans             []Reading     `json:"fans"`
	PowerSupplies    []PowerSupply `json:"power_supplies"`
}
type PowerSupply struct {
	Index     int  `json:"index"`
	Installed bool `json:"installed"`
	Status    byte `json:"ses_status"`
	Healthy   bool `json:"healthy"`
}
type Reading struct {
	Index int  `json:"index"`
	Valid bool `json:"valid"`
	Value *int `json:"value"`
}

func reading(index int, valid bool, value int) Reading {
	r := Reading{Index: index, Valid: valid}
	if valid {
		r.Value = &value
	}
	return r
}
