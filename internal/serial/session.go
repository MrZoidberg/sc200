// Package serial implements bounded SC200 console transactions.
package serial

import (
	"context"
	"errors"
	"fmt"
	"github.com/MrZoidberg/sc200/internal/protocol"
	"io"
	"regexp"
	"strings"
	"time"
)

// DefaultResponseLimit bounds a complete console transaction to 64 KiB.
const DefaultResponseLimit = 64 * 1024

var ErrDesynchronized = errors.New("serial session requires reconnect")
var ErrResponseTooLarge = errors.New("serial response exceeds limit")
var ErrUnsupported = errors.New("firmware rejected command")

// Port operations must return when ctx is cancelled. Flush discards queued input.
type Port interface {
	Read(context.Context, []byte) (int, error)
	Write(context.Context, []byte) (int, error)
	Flush() error
	Close() error
}

// Controller is the entire control-path command surface. Completion is not proof of actuation.
type Controller interface {
	ReadMap(context.Context) (protocol.Map, error)
	SetSpeed(context.Context, int) error
	Close() error
}
type Session struct {
	port     Port
	timeout  time.Duration
	limit    int
	gate     chan struct{}
	poisoned bool
}

var prompt = regexp.MustCompile(`(?m)(?:^|[\r\n])12Pack[^\r\n>]*> ?$`)

// New synchronizes to a newly requested prompt after discarding startup data.
// The caller must close p when initialization fails; a successful Session owns it.
func New(ctx context.Context, p Port, timeout time.Duration, limit int) (*Session, error) {
	if timeout <= 0 || limit <= 0 {
		return nil, errors.New("positive timeout and response limit required")
	}
	s := &Session{port: p, timeout: timeout, limit: limit, gate: make(chan struct{}, 1)}
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := p.Flush(); err != nil {
		return nil, err
	}
	if err := s.write(c, []byte("\r")); err != nil {
		return nil, err
	}
	if _, err := s.frame(c); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Session) acquire(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Session) Close() error {
	s.gate <- struct{}{}
	defer func() { <-s.gate }()
	s.poisoned = true
	return s.port.Close()
}
func (s *Session) write(ctx context.Context, b []byte) error {
	for len(b) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := s.port.Write(ctx, b)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (s *Session) frame(ctx context.Context) (string, error) {
	var b []byte
	buf := make([]byte, 512)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := s.port.Read(ctx, buf)
		if n > 0 {
			b = append(b, buf[:n]...)
			if len(b) > s.limit {
				return "", ErrResponseTooLarge
			}
			if loc := prompt.FindIndex(b); loc != nil {
				return string(b), nil
			}
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.ErrNoProgress
		}
	}
}
func (s *Session) command(ctx context.Context, cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer func() { <-s.gate }()
	if s.poisoned {
		return "", ErrDesynchronized
	}
	// Flush before each command; any ambiguous failure requires a new session.
	if err := s.port.Flush(); err != nil {
		s.poisoned = true
		return "", err
	}
	if err := s.write(ctx, []byte(cmd+"\r")); err != nil {
		s.poisoned = true
		return "", err
	}
	out, err := s.frame(ctx)
	if err != nil {
		s.poisoned = true
		return "", err
	}
	lower := strings.ToLower(out)
	for _, marker := range []string{"unknown_cmd", "unknown command", "unsupported", "invalid command", "command not found", "error:"} {
		if strings.Contains(lower, marker) {
			return "", ErrUnsupported
		}
	}
	return out, nil
}
func (s *Session) ReadMap(ctx context.Context) (protocol.Map, error) {
	out, err := s.command(ctx, "_map")
	if err != nil {
		return protocol.Map{}, err
	}
	return protocol.ParseMap(out)
}
func (s *Session) SetSpeed(ctx context.Context, n int) error {
	if n < 1 || n > 100 {
		return fmt.Errorf("speed must be 1–100: %d", n)
	}
	_, err := s.command(ctx, fmt.Sprintf("set_speed %d", n))
	return err
}

// Identify is read-only and used by discovery; arbitrary commands are never exposed.
func (s *Session) Identify(ctx context.Context) (string, error) { return s.command(ctx, "devils") }
