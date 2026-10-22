// Command checkcoverage enforces the project's statement coverage contract.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/MrZoidberg/sc200/"

var blockPattern = regexp.MustCompile(`^(.+\.go):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)
var required = map[string]float64{"internal/config": 90, "internal/protocol": 90, "internal/control": 90, "cmd/sc200": 0}

type counts struct{ statements, covered uint64 }

func (c counts) percent() float64 {
	if c.statements == 0 {
		return 0
	}
	return 100 * float64(c.covered) / float64(c.statements)
}

func check(r io.Reader, out io.Writer) error {
	s := bufio.NewScanner(r)
	if !s.Scan() {
		return fmt.Errorf("missing coverage mode")
	}
	mode := s.Text()
	if mode != "mode: set" && mode != "mode: count" && mode != "mode: atomic" {
		return fmt.Errorf("invalid coverage mode %q", mode)
	}
	packages := map[string]counts{}
	seen := map[string]bool{}
	total := counts{}
	line := 1
	for s.Scan() {
		line++
		fields := blockPattern.FindStringSubmatch(s.Text())
		if fields == nil {
			return fmt.Errorf("malformed coverage block at line %d", line)
		}
		nums := make([]uint64, 6)
		for i := range nums {
			n, err := strconv.ParseUint(fields[i+2], 10, 32)
			if err != nil {
				return fmt.Errorf("invalid number at line %d: %w", line, err)
			}
			nums[i] = n
		}
		if nums[0] == 0 || nums[1] == 0 || nums[2] < nums[0] || nums[3] == 0 || (nums[0] == nums[2] && nums[3] < nums[1]) || (mode == "mode: set" && nums[5] > 1) {
			return fmt.Errorf("invalid coverage block at line %d", line)
		}
		file := strings.TrimPrefix(fields[1], module)
		if file == fields[1] || path.Clean(file) != file || strings.HasPrefix(file, "../") || strings.HasSuffix(file, "_test.go") {
			return fmt.Errorf("unexpected source path at line %d", line)
		}
		key := strings.Join(fields[1:6], ":")
		if seen[key] {
			return fmt.Errorf("duplicate coverage block at line %d", line)
		}
		seen[key] = true
		pkg := path.Dir(file)
		c := packages[pkg]
		c.statements += nums[4]
		total.statements += nums[4]
		if nums[5] > 0 {
			c.covered += nums[4]
			total.covered += nums[4]
		}
		packages[pkg] = c
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("read coverage: %w", err)
	}
	if total.statements == 0 {
		return fmt.Errorf("coverage profile contains no statements")
	}
	var failures []string
	_, _ = fmt.Fprintf(out, "overall: %.2f%% (minimum 85%%)\n", total.percent())
	if total.percent() < 85 {
		failures = append(failures, "overall coverage below 85%")
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c, ok := packages[name]
		if !ok {
			failures = append(failures, "missing expected package "+name)
			continue
		}
		limit := required[name]
		_, _ = fmt.Fprintf(out, "%s: %.2f%% (minimum %.0f%%)\n", name, c.percent(), limit)
		if c.percent() < limit {
			failures = append(failures, fmt.Sprintf("%s coverage below %.0f%%", name, limit))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}
func run(args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: checkcoverage PROFILE")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return check(f, out)
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
