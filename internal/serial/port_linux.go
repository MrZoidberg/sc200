package serial

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"sync"
	"time"
)

type linuxPort struct {
	fd     int
	mu     sync.Mutex
	closed bool
}

// DeviceIdentity follows aliases and returns the actual character device number.
func DeviceIdentity(path string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return "", fmt.Errorf("%s is not a character device", path)
	}
	return fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev))), nil
}

var baudRates = map[int]uint32{1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800, 9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600, 115200: unix.B115200}

func configure(fd int, baud int, get func(int, uint) (*unix.Termios, error), set func(int, uint, *unix.Termios) error) error {
	speed, ok := baudRates[baud]
	if !ok {
		return fmt.Errorf("unsupported baud %d", baud)
	}
	t, err := get(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag = 0
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = unix.CLOCAL | unix.CREAD | unix.CS8 | speed
	t.Ispeed = speed
	t.Ospeed = speed
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	return set(fd, unix.TCSETS, t)
}

// OpenPort opens nonblocking raw 8N1 without echo or flow control. The caller holds
// the process-wide compatibility lock; each descriptor also takes device ownership.
func OpenPort(path string, baud int) (Port, error) {
	if _, err := DeviceIdentity(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Port, error) { _ = unix.Close(fd); return nil, err }
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(fmt.Errorf("device busy: %w", err))
	}
	// TIOCEXCL additionally rejects non-cooperating opens (except privileged processes).
	if err = unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
		return fail(err)
	}
	if err = configure(fd, baud, unix.IoctlGetTermios, unix.IoctlSetTermios); err != nil {
		return fail(err)
	}
	return &linuxPort{fd: fd}, nil
}
func (p *linuxPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	_ = unix.IoctlSetInt(p.fd, unix.TIOCNXCL, 0)
	return unix.Close(p.fd)
}
func (p *linuxPort) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	return unix.IoctlSetInt(p.fd, unix.TCFLSH, unix.TCIFLUSH)
}
func (p *linuxPort) transfer(ctx context.Context, b []byte, write bool) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return 0, io.ErrClosedPipe
		}
		var n int
		var err error
		if write {
			n, err = unix.Write(p.fd, b)
		} else {
			n, err = unix.Read(p.fd, b)
		}
		p.mu.Unlock()
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return 0, err
		}
		// A short timer bounds cancellation and avoids holding locks during waiting.
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}
func (p *linuxPort) Read(ctx context.Context, b []byte) (int, error) {
	return p.transfer(ctx, b, false)
}
func (p *linuxPort) Write(ctx context.Context, b []byte) (int, error) {
	return p.transfer(ctx, b, true)
}
