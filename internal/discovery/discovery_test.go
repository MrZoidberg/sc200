package discovery

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrZoidberg/sc200/internal/protocol"
)

type closer struct{ closed bool }

func (c *closer) Close() error { c.closed = true; return nil }

type fakeProbe struct {
	text   string
	m      protocol.Map
	err    error
	mapErr error
	closed bool
	calls  []string
}

func (p *fakeProbe) Identify(context.Context) (string, error) {
	p.calls = append(p.calls, "devils")
	return p.text, p.err
}
func (p *fakeProbe) ReadMap(context.Context) (protocol.Map, error) {
	p.calls = append(p.calls, "_map")
	return p.m, p.mapErr
}
func (p *fakeProbe) Close() error { p.closed = true; return nil }
func evidence(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../protocol/testdata/devils-12pack.txt")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func sample(t *testing.T) protocol.Map {
	t.Helper()
	b, e := os.ReadFile("../protocol/testdata/map-12pack.txt")
	if e != nil {
		t.Fatal(e)
	}
	m, e := protocol.ParseMap(string(b))
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestScanStableAliasesAndFailures(t *testing.T) {
	text, m := evidence(t), sample(t)
	lock := &closer{}
	var mu sync.Mutex
	var probes []*fakeProbe
	result, err := Scan(context.Background(), nil, 38400, Options{
		Enumerate: func() ([]string, error) {
			return []string{"/dev/ttyS0", "/dev/ttyUSB0", "/dev/serial/by-id/usb", "/dev/busy", "/dev/unknown", "/dev/missing"}, nil
		},
		Identity: func(p string) (string, error) {
			if p == "/dev/missing" {
				return "", os.ErrNotExist
			}
			if strings.Contains(p, "USB") || strings.Contains(p, "by-id") {
				return "usb", nil
			}
			return p, nil
		},
		AcquireLock: func() (io.Closer, error) { return lock, nil }, Open: func(_ context.Context, p string, b int) (Probe, error) {
			if b != 38400 {
				t.Error(b)
			}
			if p == "/dev/busy" {
				return nil, errors.New("busy")
			}
			probe := &fakeProbe{text: text, m: m}
			if p == "/dev/unknown" {
				probe.text = "12Pack > arbitrary"
			}
			mu.Lock()
			probes = append(probes, probe)
			mu.Unlock()
			return probe, nil
		},
	})
	if err != nil || len(result.Enclosures) != 2 || len(result.Outcomes) != 5 || !lock.closed {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Enclosures[0].Port != "/dev/serial/by-id/usb" || len(result.Enclosures[0].RequiredSensors) != 4 {
		t.Fatal(result.Enclosures)
	}
	for _, p := range probes {
		if !p.closed {
			t.Fatal("leaked probe")
		}
		for _, cmd := range p.calls {
			if cmd != "devils" && cmd != "_map" {
				t.Fatal("control command")
			}
		}
	}
}
func TestProbeRejectsUncertainOrUnusableMap(t *testing.T) {
	text, m := evidence(t), sample(t)
	cases := []fakeProbe{{text: "SES 12Pack >", m: m}, {text: text, err: errors.New("unsupported")}, {text: text, m: protocol.Map{}}, {text: text, mapErr: errors.New("map timeout")}, {text: text, m: m}}
	cases[len(cases)-1].m.Temperatures = append([]protocol.Temperature(nil), m.Temperatures...)
	cases[len(cases)-1].m.Temperatures[0].Available = false
	for i := range cases {
		p := &cases[i]
		_, err := probe(context.Background(), "/dev/a", 38400, func(context.Context, string, int) (Probe, error) { return p, nil })
		if err == nil || !p.closed {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}
func TestScanBoundsCancellationAndConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	active, max := 0, 0
	start := time.Now()
	r, err := Scan(ctx, []string{"/dev/a", "/dev/b", "/dev/c"}, 38400, Options{Timeout: 20 * time.Millisecond, Concurrency: 2, Identity: func(p string) (string, error) { return p, nil }, AcquireLock: func() (io.Closer, error) { return &closer{}, nil }, Open: func(ctx context.Context, _ string, _ int) (Probe, error) {
		mu.Lock()
		active++
		if active > max {
			max = active
		}
		mu.Unlock()
		<-ctx.Done()
		mu.Lock()
		active--
		mu.Unlock()
		return nil, ctx.Err()
	}})
	if err != nil || len(r.Enclosures) != 0 || len(r.Outcomes) != 3 || max > 2 || time.Since(start) > time.Second {
		t.Fatalf("%+v %v max %d", r, err, max)
	}
}
func TestScanSetupErrors(t *testing.T) {
	for _, o := range []Options{{Enumerate: func() ([]string, error) { return nil, errors.New("enumeration") }}, {Enumerate: func() ([]string, error) { return nil, nil }, AcquireLock: func() (io.Closer, error) { return nil, errors.New("daemon active") }}} {
		if _, err := Scan(context.Background(), nil, 38400, o); err == nil {
			t.Fatal("expected failure")
		}
	}
	if _, err := Scan(context.Background(), nil, 12, Options{}); err == nil {
		t.Fatal("invalid baud")
	}
}
