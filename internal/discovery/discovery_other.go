//go:build !linux

package discovery

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
)

func defaults(o *Options) {
	if o.Enumerate == nil {
		o.Enumerate = func() ([]string, error) { return nil, fmt.Errorf("discovery requires Linux") }
	}
	if o.Identity == nil {
		o.Identity = filepath.EvalSymlinks
	}
	if o.AcquireLock == nil {
		o.AcquireLock = func() (io.Closer, error) { return nil, fmt.Errorf("discovery requires Linux") }
	}
	if o.Open == nil {
		o.Open = func(context.Context, string, int) (Probe, error) { return nil, fmt.Errorf("discovery requires Linux") }
	}
}
