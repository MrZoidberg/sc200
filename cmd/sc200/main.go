package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/MrZoidberg/sc200/internal/app"
)

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, signal.NotifyContext)) }
func execute(args []string, out, errout io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)) int {
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, args, out, errout, app.Options{})
}
