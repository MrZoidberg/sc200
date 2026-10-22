// Package discovery probes read-only console evidence before proposing configuration.
package discovery

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/protocol"
)

type Probe interface {
	Identify(context.Context) (string, error)
	ReadMap(context.Context) (protocol.Map, error)
	Close() error
}

// Options injects hardware boundaries. Open and Probe methods must honor context cancellation.
type Options struct {
	Enumerate   func() ([]string, error)
	Identity    func(string) (string, error)
	Open        func(context.Context, string, int) (Probe, error)
	AcquireLock func() (io.Closer, error)
	Timeout     time.Duration
	Concurrency int
}
type Outcome struct {
	Port string
	Err  error
}
type Result struct {
	Enclosures []config.Enclosure
	Outcomes   []Outcome
}

// Scan bounds the entire scan and each session; one failed port cannot suppress another.
func Scan(ctx context.Context, ports []string, baud int, o Options) (Result, error) {
	defaults(&o)
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.Concurrency > 16 {
		o.Concurrency = 16
	}
	e := config.DefaultEnclosure()
	e.Baud = baud
	if err := e.Validate(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if len(ports) == 0 {
		var err error
		ports, err = o.Enumerate()
		if err != nil {
			return Result{}, err
		}
	}
	lock, err := o.AcquireLock()
	if err != nil {
		return Result{}, fmt.Errorf("discovery cannot acquire daemon ownership: %w", err)
	}
	defer func() { _ = lock.Close() }()
	sort.Slice(ports, func(i, j int) bool {
		a, b := strings.HasPrefix(ports[i], "/dev/serial/by-id/"), strings.HasPrefix(ports[j], "/dev/serial/by-id/")
		if a != b {
			return a
		}
		return ports[i] < ports[j]
	})
	result := Result{}
	seen := map[string]bool{}
	var candidates []string
	for _, p := range ports {
		id, err := o.Identity(p)
		if err != nil {
			result.Outcomes = append(result.Outcomes, Outcome{p, err})
			continue
		}
		if !seen[id] {
			seen[id] = true
			candidates = append(candidates, p)
		}
	}
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range o.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				entry, err := probe(ctx, p, baud, o.Open)
				mu.Lock()
				result.Outcomes = append(result.Outcomes, Outcome{p, err})
				if err == nil {
					result.Enclosures = append(result.Enclosures, entry)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range candidates {
		select {
		case jobs <- p:
		case <-ctx.Done():
			mu.Lock()
			result.Outcomes = append(result.Outcomes, Outcome{p, ctx.Err()})
			mu.Unlock()
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(result.Enclosures, func(i, j int) bool { return result.Enclosures[i].Port < result.Enclosures[j].Port })
	sort.Slice(result.Outcomes, func(i, j int) bool { return result.Outcomes[i].Port < result.Outcomes[j].Port })
	return result, nil
}
func probe(ctx context.Context, path string, baud int, open func(context.Context, string, int) (Probe, error)) (config.Enclosure, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	p, err := open(ctx, path, baud)
	if err != nil {
		return config.Enclosure{}, err
	}
	defer func() { _ = p.Close() }()
	text, err := p.Identify(ctx)
	if err != nil {
		return config.Enclosure{}, err
	}
	// This signature is evidence for the supplied 12Pack firmware, not a model certificate.
	for _, marker := range []string{"12Pack.", "devils      Print the Help Screen", "chassistype", "Blue Devil", "set_speed   Sets Fan Speeds", "ses_page"} {
		if !strings.Contains(text, marker) {
			return config.Enclosure{}, fmt.Errorf("uncertain device: missing supported console evidence %q", marker)
		}
	}
	m, err := p.ReadMap(ctx)
	if err != nil {
		return config.Enclosure{}, err
	}
	e := config.DefaultEnclosure()
	e.Port = path
	e.Baud = baud
	for _, t := range m.Temperatures {
		if t.Installed {
			if !t.Healthy() {
				return config.Enclosure{}, fmt.Errorf("installed sensor %d is unhealthy", t.Index)
			}
			e.RequiredSensors = append(e.RequiredSensors, t.Index)
		}
	}
	if len(e.RequiredSensors) == 0 || len(m.Fans) == 0 {
		return config.Enclosure{}, fmt.Errorf("unsupported map: need installed temperature sensors and fans")
	}
	e.ID = "sc200-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, filepath.Base(path))
	return e, nil
}

// Identity resolves the platform device identity used for merge deduplication.
func Identity(path string) (string, error) { o := Options{}; defaults(&o); return o.Identity(path) }
