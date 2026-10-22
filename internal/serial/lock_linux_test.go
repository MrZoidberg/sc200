package serial

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLockOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	a, e := AcquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = AcquireLock(path); e == nil {
		t.Fatal("duplicate acquired")
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := AcquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = b.Close() }()
	alias := filepath.Join(t.TempDir(), "alias")
	if e = os.Symlink(path, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = AcquireLock(alias); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestRawConfiguration(t *testing.T) {
	var got unix.Termios
	e := configure(3, 38400, func(int, uint) (*unix.Termios, error) {
		return &unix.Termios{Iflag: ^uint32(0), Oflag: ^uint32(0), Lflag: ^uint32(0), Cflag: ^uint32(0)}, nil
	}, func(_ int, _ uint, v *unix.Termios) error { got = *v; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if got.Iflag != 0 || got.Oflag != 0 || got.Lflag != 0 || got.Cflag != (unix.CLOCAL|unix.CREAD|unix.CS8|unix.B38400) || got.Cc[unix.VMIN] != 0 || got.Cc[unix.VTIME] != 0 {
		t.Fatal(got)
	}
	if e = configure(3, 17, nil, nil); e == nil {
		t.Fatal("baud")
	}
	want := errors.New("get")
	if e = configure(3, 38400, func(int, uint) (*unix.Termios, error) { return nil, want }, nil); !errors.Is(e, want) {
		t.Fatal(e)
	}
}
func TestIdentityAliases(t *testing.T) {
	a, e := DeviceIdentity("/dev/null")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "alias")
	if e = os.Symlink("/dev/null", path); e != nil {
		t.Fatal(e)
	}
	b, e := DeviceIdentity(path)
	if e != nil || a != b {
		t.Fatal(a, b, e)
	}
	if _, e = DeviceIdentity(t.TempDir()); e == nil {
		t.Fatal("directory")
	}
	if _, e = OpenPort("/does-not-exist", 38400); e == nil {
		t.Fatal("missing")
	}
}

// PTYs exercise real Linux termios and ownership without physical hardware.
func TestLinuxPTY(t *testing.T) {
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = unix.Close(fd) }()
	if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
		t.Fatal(e)
	}
	n, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join("/dev/pts", strconv.Itoa(n))
	p, e := OpenPort(path, 38400)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenPort(path, 38400); e == nil {
		t.Fatal("busy tty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, e = p.Read(ctx, make([]byte, 10)); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e = p.Flush(); e != nil {
		t.Fatal(e)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if n, e = p.Write(ctx2, []byte("a")); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Read(ctx2, make([]byte, 1)); e == nil {
		t.Fatal("closed")
	}
}
