package serial

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type scripted struct {
	mu      sync.Mutex
	queued  []byte
	replies []string
	writes  []string
	partial int
	failure error
	closed  bool
	flushes int
	started chan struct{}
}

func (p *scripted) Read(ctx context.Context, b []byte) (int, error) {
	p.mu.Lock()
	if len(p.queued) > 0 {
		n := copy(b, p.queued[:min(len(p.queued), 7)])
		p.queued = p.queued[n:]
		p.mu.Unlock()
		return n, nil
	}
	err := p.failure
	p.mu.Unlock()
	if err != nil {
		return 0, err
	}
	<-ctx.Done()
	return 0, ctx.Err()
}
func (p *scripted) Write(_ context.Context, b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return 0, p.failure
	}
	n := len(b)
	if p.partial > 0 {
		n = min(n, p.partial)
	}
	p.writes = append(p.writes, string(b[:n]))
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if strings.Contains(string(b[:n]), "\r") && len(p.replies) > 0 {
		p.queued = append(p.queued, p.replies[0]...)
		p.replies = p.replies[1:]
	}
	return n, nil
}
func (p *scripted) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued = nil
	p.flushes++
	return nil
}
func (p *scripted) Close() error { p.closed = true; return nil }
func session(t *testing.T, p *scripted) *Session {
	t.Helper()
	s, e := New(context.Background(), p, time.Second, 65536)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

const testPrompt = "\r\n12Pack.Slot0.101 7 > "

func TestReadMapAndCompletion(t *testing.T) {
	b, e := os.ReadFile("../protocol/testdata/map-12pack.txt")
	if e != nil {
		t.Fatal(e)
	}
	p := &scripted{queued: []byte("stale map"), replies: []string{testPrompt, "_map\r\n" + string(b) + testPrompt, "set_speed 30\r\n" + testPrompt}, partial: 2}
	s := session(t, p)
	m, e := s.ReadMap(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Temperatures) != 4 {
		t.Fatal(m)
	}
	if e = s.SetSpeed(context.Background(), 30); e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(p.writes, ""); got != "\r_map\rset_speed 30\r" {
		t.Fatal(got)
	}
	if p.flushes != 3 {
		t.Fatal(p.flushes)
	}
	if e = s.Close(); e != nil || !p.closed {
		t.Fatal(e)
	}
}
func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		failure        error
		want           error
	}{
		{name: "deadline", want: context.DeadlineExceeded},
		{name: "unsupported", response: "unknown_cmd -> cmd:_map args:1\r\n" + testPrompt, want: ErrUnsupported},
		{name: "transport", failure: io.ErrUnexpectedEOF, want: io.ErrUnexpectedEOF},
		{name: "size", response: strings.Repeat("x", 66000), want: ErrResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &scripted{replies: []string{testPrompt}}
			s := session(t, p)
			if tc.name == "size" {
				s.limit = 100
			}
			s.timeout = 40 * time.Millisecond
			p.replies = []string{tc.response}
			p.failure = tc.failure
			e := s.SetSpeed(context.Background(), 50)
			if !errors.Is(e, tc.want) {
				t.Fatalf("%v want %v", e, tc.want)
			}
			if tc.want != ErrUnsupported {
				p.queued = []byte(testPrompt)
				if e = s.SetSpeed(context.Background(), 50); !errors.Is(e, ErrDesynchronized) {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestValidationAndCancellation(t *testing.T) {
	p := &scripted{replies: []string{testPrompt}}
	s := session(t, p)
	for _, n := range []int{-1, 0, 101} {
		if s.SetSpeed(context.Background(), n) == nil {
			t.Fatal(n)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.SetSpeed(ctx, 10); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := New(ctx, p, time.Second, 0); e == nil {
		t.Fatal("limit")
	}
}
func TestSerializationAndQueuedCancellation(t *testing.T) {
	p := &scripted{replies: []string{testPrompt}}
	s := session(t, p)
	s.timeout = 40 * time.Millisecond
	p.started = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- s.SetSpeed(context.Background(), 50) }()
	<-p.started
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if e := s.SetSpeed(ctx, 20); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if strings.Join(p.writes, "") != "\rset_speed 50\r" {
		t.Fatal(p.writes)
	}
}
func TestStartupBounded(t *testing.T) {
	p := &scripted{}
	if _, e := New(context.Background(), p, time.Millisecond, 100); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

// A queued complete old map must not turn an otherwise empty new transaction
// into fresh telemetry. A speed prompt only establishes completion, never RPM.
func TestQueuedMapAndPromptOnlyTelemetry(t *testing.T) {
	b, err := os.ReadFile("../protocol/testdata/map-12pack.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := &scripted{replies: []string{testPrompt}}
	s := session(t, p)
	p.queued = append(b, []byte(testPrompt)...)
	p.replies = []string{testPrompt, testPrompt}
	m, err := s.ReadMap(context.Background())
	if err == nil || len(m.Temperatures) != 0 || len(m.Fans) != 0 {
		t.Fatalf("prompt-only response accepted old telemetry: map=%+v err=%v", m, err)
	}
	if err = s.SetSpeed(context.Background(), 30); err != nil {
		t.Fatalf("prompt-only speed completion: %v", err)
	}
	if got := strings.Join(p.writes, ""); got != "\r_map\rset_speed 30\r" {
		t.Fatal(got)
	}
}

func TestLateMapAfterTimeoutRequiresReconnect(t *testing.T) {
	b, err := os.ReadFile("../protocol/testdata/map-12pack.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := &scripted{replies: []string{testPrompt}}
	s := session(t, p)
	s.timeout = time.Millisecond
	if _, err = s.ReadMap(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	p.queued = append(b, []byte(testPrompt)...)
	before := len(p.writes)
	if _, err = s.ReadMap(context.Background()); !errors.Is(err, ErrDesynchronized) {
		t.Fatalf("late map accepted after timeout: %v", err)
	}
	if len(p.writes) != before {
		t.Fatal("poisoned session issued a new command")
	}
}
