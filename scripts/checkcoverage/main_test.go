package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profile(config, protocol, control, command string) string {
	return "mode: atomic\n" + module + "internal/config/config.go:1.1,2.2 100 " + config + "\n" + module + "internal/protocol/map.go:1.1,2.2 100 " + protocol + "\n" + module + "internal/control/policy.go:1.1,2.2 100 " + control + "\n" + module + "cmd/sc200/main.go:1.1,2.2 10 " + command + "\n"
}
func TestCheck(t *testing.T) {
	good := profile("1", "1", "1", "1")
	tests := []struct{ name, input, want string }{
		{"pass", good, ""},
		{"count mode", strings.Replace(good, "mode: atomic", "mode: count", 1), ""},
		{"set mode", strings.Replace(good, "mode: atomic", "mode: set", 1), ""},
		{"package threshold", profile("0", "1", "1", "1"), "internal/config coverage below 90%"},
		{"protocol threshold", profile("1", "0", "1", "1"), "internal/protocol coverage below 90%"},
		{"control threshold", profile("1", "1", "0", "1"), "internal/control coverage below 90%"},
		{"overall includes command", strings.Replace(good, "10 1", "100 0", 1), "overall coverage below 85%"},
		{"missing required", strings.Replace(good, module+"internal/config/config.go:1.1,2.2 100 1\n", "", 1), "missing expected package internal/config"},
		{"missing command", strings.Replace(good, module+"cmd/sc200/main.go:1.1,2.2 10 1\n", "", 1), "missing expected package cmd/sc200"},
		{"empty", "", "missing coverage mode"},
		{"bad mode", "mode: nonsense\n", "invalid coverage mode"},
		{"no blocks", "mode: atomic\n", "contains no statements"},
		{"bad block", good + "garbage\n", "malformed coverage block"},
		{"bad integer", strings.Replace(good, "100 1", "999999999999999999 1", 1), "invalid number"},
		{"invalid coordinates", strings.Replace(good, "1.1,2.2", "2.1,1.2", 1), "invalid coverage block"},
		{"zero line", strings.Replace(good, "1.1,2.2", "0.1,2.2", 1), "invalid coverage block"},
		{"zero column", strings.Replace(good, "1.1,2.2", "1.0,2.2", 1), "invalid coverage block"},
		{"reversed column", strings.Replace(good, "1.1,2.2", "1.3,1.2", 1), "invalid coverage block"},
		{"empty block", good + module + "other/file.go:1.1,1.1 0 0\n", ""},
		{"invalid set count", strings.Replace(strings.Replace(good, "atomic", "set", 1), "100 1", "100 2", 1), "invalid coverage block"},
		{"unexpected source", strings.Replace(good, module, "other/", 1), "unexpected source path"},
		{"unclean source", strings.Replace(good, "internal/config/config.go", "internal/../config.go", 1), "unexpected source path"},
		{"test source", strings.Replace(good, "config/config.go", "config/config_test.go", 1), "unexpected source path"},
		{"duplicate", good + module + "cmd/sc200/main.go:1.1,2.2 10 1\n", "duplicate coverage block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := check(strings.NewReader(tt.input), &out)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "overall: 100.00%") {
					t.Fatal(out.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}
func TestThresholdBoundaries(t *testing.T) {
	input := "mode: atomic\n"
	for _, pkg := range []string{"internal/config", "internal/protocol", "internal/control"} {
		input += module + pkg + "/file.go:1.1,2.1 90 1\n" + module + pkg + "/file.go:2.1,3.1 10 0\n"
	}
	input += module + "cmd/sc200/main.go:1.1,2.1 10 0\n"
	if err := check(strings.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
	// Exact overall boundary with all critical packages still at 90%.
	input += module + "other/file.go:1.1,2.1 50 0\n" + module + "other/file.go:2.1,3.1 240 1\n"
	if err := check(strings.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("broken reader") }
func TestReadFailure(t *testing.T) {
	r := io.MultiReader(strings.NewReader("mode: atomic\n"), brokenReader{})
	if err := check(r, io.Discard); err == nil || !strings.Contains(err.Error(), "broken reader") {
		t.Fatalf("got %v", err)
	}
}
func TestRun(t *testing.T) {
	if err := run(nil, io.Discard); err == nil {
		t.Fatal("missing argument accepted")
	}
	if err := run([]string{"/nonexistent/profile"}, io.Discard); err == nil {
		t.Fatal("missing file accepted")
	}
	name := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(name, []byte(profile("1", "1", "1", "1")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{name}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
