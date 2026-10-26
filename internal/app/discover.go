package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"io"
	"path/filepath"
)

type portsFlag []string

func (p *portsFlag) String() string { return fmt.Sprint([]string(*p)) }
func (p *portsFlag) Set(s string) error {
	if !filepath.IsAbs(s) {
		return fmt.Errorf("port must be absolute")
	}
	*p = append(*p, s)
	return nil
}
func discover(ctx context.Context, args []string, out, diag io.Writer, o Options) int {
	f := flag.NewFlagSet("sc200 discover", flag.ContinueOnError)
	f.SetOutput(diag)
	path := f.String("config", config.DefaultPath, "configuration path")
	write := f.Bool("write", false, "atomically append discoveries; restart service to apply")
	verbose := f.Bool("verbose", false, "show failures for every probed port")
	baud := f.Int("baud", 38400, "serial baud")
	var ports portsFlag
	f.Var(&ports, "port", "probe only this port (repeatable)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || *path == "" {
		return 2
	}
	scan := o.Discover
	if scan == nil {
		scan = func(ctx context.Context, p []string, b int) (discovery.Result, error) {
			return discovery.Scan(ctx, p, b, discovery.Options{})
		}
	}
	r, err := scan(ctx, ports, *baud)
	if err != nil {
		_, _ = fmt.Fprintln(diag, err)
		return 1
	}
	failed := 0
	for _, v := range r.Outcomes {
		if v.Err != nil {
			failed++
			if *verbose || len(ports) > 0 {
				_, _ = fmt.Fprintf(diag, "%s: probe failed: %v\n", v.Port, v.Err)
			}
		} else {
			_, _ = fmt.Fprintf(diag, "%s: supported 12Pack console evidence (related firmware/models may share signature)\n", v.Port)
		}
	}
	if failed > 0 && !*verbose && len(ports) == 0 {
		_, _ = fmt.Fprintf(diag, "Skipped %d ports that could not be probed or did not match; use --verbose for details.\n", failed)
	}
	if len(r.Enclosures) == 0 {
		_, _ = fmt.Fprintln(diag, "no supported enclosures found; configuration unchanged")
		return 3
	}
	identity := o.DiscoveryIdentity
	if identity == nil {
		identity = discovery.Identity
	}
	data, err := config.MergeFile(*path, r.Enclosures, *write, identity)
	if err != nil {
		_, _ = fmt.Fprintln(diag, err)
		return 1
	}
	if !*write {
		_, _ = fmt.Fprintf(diag, "Configuration preview (not saved); use --write to save to %s.\n", *path)
	}
	if _, err := out.Write(data); err != nil {
		_, _ = fmt.Fprintln(diag, "write discovery output:", err)
		return 1
	}
	if *write {
		_, _ = fmt.Fprintf(diag, "Configuration written: %s; restart an already running service to use additions.\n", *path)
	}
	return 0
}
