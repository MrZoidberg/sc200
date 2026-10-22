//go:build !linux

package service

import (
	"context"
	"errors"
	"io"

	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/serial"
)

func defaults(o *Options) {
	unsupported := errors.New("SC200 hardware control requires Linux")
	if o.Identity == nil {
		o.Identity = func(string) (string, error) { return "", unsupported }
	}
	if o.AcquireLock == nil {
		o.AcquireLock = func() (io.Closer, error) { return nil, unsupported }
	}
	if o.Open == nil {
		o.Open = func(context.Context, config.Enclosure) (serial.Controller, error) { return nil, unsupported }
	}
}
