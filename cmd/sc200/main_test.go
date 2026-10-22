package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestExecute(t *testing.T) {
	for _, arg := range []string{"--version", "--help", "--bad"} {
		t.Run(arg, func(t *testing.T) {
			var out, errout bytes.Buffer
			stopped := false
			code := execute([]string{arg}, &out, &errout, func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				return context.Background(), func() { stopped = true }
			})
			want := 0
			if arg == "--bad" {
				want = 2
			}
			if code != want || !stopped {
				t.Fatalf("code=%d stopped=%v", code, stopped)
			}
			if arg == "--version" && !strings.Contains(out.String(), "sc200") {
				t.Fatal(out.String())
			}
		})
	}
}
