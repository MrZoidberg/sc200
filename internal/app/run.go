// Package app owns CLI parsing and the bounded process lifecycle.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/MrZoidberg/sc200/internal/buildinfo"
	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/control"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"github.com/MrZoidberg/sc200/internal/httpapi"
	"github.com/MrZoidberg/sc200/internal/service"
)

type Manager interface {
	Start(context.Context) error
	Snapshots() []control.Snapshot
	Shutdown(context.Context) error
	ShutdownTimeout() time.Duration
}
type HTTPServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
}

// Options injects process boundaries; nil fields use production implementations.
type Options struct {
	Discover          func(context.Context, []string, int) (discovery.Result, error)
	DiscoveryIdentity func(string) (string, error)
	Load              func(string) (config.Config, error)
	NewManager        func(config.Config) (Manager, error)
	Listen            func(string, string) (net.Listener, error)
	NewHTTP           func(string, Manager) HTTPServer
}

// Run returns shell exit codes: 0 success, 1 runtime/configuration error, 2 usage error.
// ctx is canceled by the caller's signal handler; shutdown uses an independent deadline.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	if len(args) > 0 && args[0] == "discover" {
		return discover(ctx, args[1:], stdout, stderr, opts)
	}
	flags := flag.NewFlagSet("sc200", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", config.DefaultPath, "configuration file path")
	check := flags.Bool("check-config", false, "validate configuration without hardware access")
	version := flags.Bool("version", false, "print version metadata")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: sc200 [--config PATH] [--check-config | --version]\n       sc200 discover [--config PATH] [--port PATH] [--baud N] [--write]\nDefault configuration: %s\n", config.DefaultPath)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*version && *check) || *path == "" {
		flags.Usage()
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	fail := func(err error) int { log.Error("service failed", "error", err); return 1 }
	if opts.Load == nil {
		opts.Load = config.Load
	}
	cfg, err := opts.Load(*path)
	if err != nil {
		return fail(fmt.Errorf("load configuration %s: %w", *path, err))
	}
	if err = cfg.Validate(); err != nil {
		return fail(fmt.Errorf("configuration %s: %w", *path, err))
	}
	if *check {
		fmt.Fprintf(stdout, "Configuration valid: %s\n", *path)
		return 0
	}
	if ctx.Err() != nil {
		return 0
	}
	if opts.NewManager == nil {
		opts.NewManager = func(c config.Config) (Manager, error) { return service.New(c, service.Options{}) }
	}
	manager, err := opts.NewManager(cfg)
	if err != nil {
		return fail(err)
	}
	if opts.Listen == nil {
		opts.Listen = net.Listen
	}
	listener, err := opts.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		return fail(fmt.Errorf("listen %s: %w", cfg.HTTP.Listen, err))
	}
	defer listener.Close()
	// The manager owns normal polling cancellation; cleanup coordinates both owners.
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	if err = manager.Start(run); err != nil {
		cleanup, c := context.WithTimeout(context.Background(), manager.ShutdownTimeout())
		defer c()
		return fail(errors.Join(err, manager.Shutdown(cleanup)))
	}
	if opts.NewHTTP == nil {
		opts.NewHTTP = func(address string, m Manager) HTTPServer {
			return httpapi.New(address, m, httpapi.BuildInfo{Version: buildinfo.Version, Commit: buildinfo.Commit, Date: buildinfo.Date})
		}
	}
	server := opts.NewHTTP(cfg.HTTP.Listen, manager)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	log.Info("service started", "config", *path, "listen", listener.Addr().String())
	select {
	case <-ctx.Done():
	case err = <-served:
		if err == nil {
			err = errors.New("HTTP server stopped unexpectedly")
		}
	}
	cleanup, c := context.WithTimeout(context.Background(), manager.ShutdownTimeout())
	defer c()
	var wg sync.WaitGroup
	var managerErr, httpErr error
	wg.Add(2)
	go func() { defer wg.Done(); managerErr = manager.Shutdown(cleanup) }()
	go func() { defer wg.Done(); httpErr = server.Shutdown(cleanup) }()
	wg.Wait()
	cancel()
	if err = errors.Join(err, managerErr, httpErr); err != nil {
		return fail(err)
	}
	log.Info("service stopped")
	return 0
}
