package discovery

import (
	"context"
	"github.com/MrZoidberg/sc200/internal/serial"
	"io"
	"os"
	"path/filepath"
	"time"
)

func defaults(o *Options) {
	if o.Enumerate == nil {
		o.Enumerate = enumerate
	}
	if o.Identity == nil {
		o.Identity = serial.DeviceIdentity
	}
	if o.AcquireLock == nil {
		o.AcquireLock = func() (io.Closer, error) { return serial.AcquireCompatibilityLock() }
	}
	if o.Open == nil {
		o.Open = func(ctx context.Context, p string, b int) (Probe, error) {
			port, err := serial.OpenPort(p, b)
			if err != nil {
				return nil, err
			}
			s, err := serial.New(ctx, port, 2*time.Second, serial.DefaultResponseLimit)
			if err != nil {
				_ = port.Close()
				return nil, err
			}
			return s, nil
		}
	}
}
func enumerate() ([]string, error) {
	var out []string
	stable, err := filepath.Glob("/dev/serial/by-id/*")
	if err != nil {
		return nil, err
	}
	out = append(out, stable...)
	// sysfs tty/device excludes virtual consoles and pseudo terminals, includes UART/USB.
	paths, err := filepath.Glob("/sys/class/tty/*/device")
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		name := filepath.Base(filepath.Dir(p))
		path := "/dev/" + name
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out, nil
}
