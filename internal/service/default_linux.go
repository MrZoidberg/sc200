package service

import (
	"context"
	"io"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/serial"
)

func defaults(o *Options) {
	if o.Identity == nil {
		o.Identity = serial.DeviceIdentity
	}
	if o.AcquireLock == nil {
		o.AcquireLock = func() (io.Closer, error) { return serial.AcquireCompatibilityLock() }
	}
	if o.Open == nil {
		o.Open = func(ctx context.Context, c config.Enclosure) (serial.Controller, error) {
			p, err := serial.OpenPort(c.Port, c.Baud)
			if err != nil {
				return nil, err
			}
			s, err := serial.New(ctx, p, c.CommandTimeout, serial.DefaultResponseLimit)
			if err != nil {
				_ = p.Close()
				return nil, err
			}
			return s, nil
		}
	}
}
