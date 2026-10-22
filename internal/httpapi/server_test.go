package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/MrZoidberg/sc200/internal/protocol"
)

type source struct {
	mu     sync.Mutex
	states []control.Snapshot
}

func (s *source) Snapshots() []control.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]control.Snapshot{}, s.states...)
}
func healthy(id string) control.Snapshot {
	return control.Snapshot{ID: id, State: control.Healthy, Healthy: true, Fresh: true, Target: 40, CompletedCommand: 30, LastTelemetry: time.Unix(100, 0), LastCommand: time.Unix(90, 0), Telemetry: protocol.Map{Temperatures: []protocol.Temperature{{Element: protocol.Element{Index: 2, Installed: true, Status: 1}, Available: true, Celsius: 32}}, Fans: []protocol.Fan{{Element: protocol.Element{Index: 3, Installed: true, Status: 1}, Available: true, RPM: 2680}}}}
}
func TestHealth(t *testing.T) {
	for _, state := range []control.State{control.Healthy, control.Starting, control.Failsafe, control.Disconnected, control.Stopping} {
		t.Run(string(state), func(t *testing.T) {
			a, b := healthy("a"), healthy("b")
			b.State = state
			b.Healthy = state == control.Healthy
			b.Fresh = b.Healthy
			s := New("", &source{states: []control.Snapshot{a, b}}, BuildInfo{})
			for _, path := range []string{"/healthz", "/readyz", "/api/v1/health"} {
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				want := 200
				if path != "/healthz" && state != control.Healthy {
					want = 503
				}
				if w.Code != want {
					t.Fatalf("%s: %d", path, w.Code)
				}
				if path == "/api/v1/health" {
					var v Health
					if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
						t.Fatal(err)
					}
					if w.Header().Get("Content-Type") != "application/json" || len(v.Enclosures) != 2 {
						t.Fatal(w.Body.String())
					}
					e := v.Enclosures[1]
					if e.Target != 40 || e.CompletedCommand != 30 || e.Temperatures[0].Valid != b.Fresh || (e.Temperatures[0].Value != nil) != b.Fresh {
						t.Fatal(e)
					}
				}
			}
		})
	}
}
func TestMethodsEmptyAndUnavailable(t *testing.T) {
	s := New("", &source{}, BuildInfo{})
	for _, path := range []string{"/metrics", "/healthz", "/readyz", "/api/v1/health"} {
		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal(w)
			}
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	a := healthy("a")
	a.Telemetry.Temperatures[0].Available = false
	a.Telemetry.Fans[0].Failed = true
	a.LastCommand = time.Time{}
	s = New("", &source{states: []control.Snapshot{a}}, BuildInfo{})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/health", nil))
	var h Health
	_ = json.Unmarshal(w.Body.Bytes(), &h)
	if h.Enclosures[0].Fans[0].Value != nil || h.Enclosures[0].Temperatures[0].Value != nil || h.Enclosures[0].LastCommand != nil {
		t.Fatal(h)
	}
}
func TestServeAndShutdown(t *testing.T) {
	s := New("", &source{}, BuildInfo{})
	if s.server.ReadHeaderTimeout <= 0 || s.server.WriteTimeout <= 0 || s.server.IdleTimeout <= 0 {
		t.Fatal("missing timeouts")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown blocked")
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := New("", &source{}, BuildInfo{}).Serve(closed); err == nil {
		t.Fatal("closed listener accepted")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	if err := New(occupied.Addr().String(), &source{}, BuildInfo{}).ListenAndServe(); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
func TestConcurrentCachedReads(t *testing.T) {
	src := &source{states: []control.Snapshot{healthy("a")}}
	s := New("", src, BuildInfo{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				for _, path := range []string{"/metrics", "/api/v1/health", "/readyz"} {
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
					if w.Code != 200 && w.Code != 503 {
						t.Error(w.Code)
					}
				}
			}
		})
	}
	wg.Go(func() {
		for i := 0; i < 200; i++ {
			src.mu.Lock()
			src.states[0].Fresh = i%2 == 0
			src.mu.Unlock()
		}
	})
	wg.Wait()
}
