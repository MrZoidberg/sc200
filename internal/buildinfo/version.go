// Package buildinfo holds metadata supplied by release linker flags.
package buildinfo

import "fmt"

var Version = "dev"
var Commit = "unknown"
var Date = "unknown"

func String() string { return fmt.Sprintf("sc200 %s (commit %s, built %s)", Version, Commit, Date) }
