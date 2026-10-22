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
		fmt.Fprintln(diag, err)
		return 1
	}
	for _, v := range r.Outcomes {
		if v.Err != nil {
			fmt.Fprintf(diag, "%s: %v\n", v.Port, v.Err)
		} else {
			fmt.Fprintf(diag, "%s: supported 12Pack console evidence (related firmware/models may share signature)\n", v.Port)
		}
	}
	if len(r.Enclosures) == 0 {
		fmt.Fprintln(diag, "no supported enclosures found; configuration unchanged")
		return 3
	}
	identity := o.DiscoveryIdentity
	if identity == nil {
		identity = discovery.Identity
	}
	data, err := config.MergeFile(*path, r.Enclosures, *write, identity)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	fmt.Fprint(out, string(data))
	if *write {
		fmt.Fprintf(diag, "Configuration written: %s; restart an already running service to use additions.\n", *path)
	}
	return 0
}
