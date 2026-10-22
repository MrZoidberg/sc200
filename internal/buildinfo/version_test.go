package buildinfo

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	for _, s := range []string{"sc200 ", Version, Commit, Date} {
		if !strings.Contains(String(), s) {
			t.Fatalf("missing %q", s)
		}
	}
}
