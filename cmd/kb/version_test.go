package main

import (
	"strings"
	"testing"
)

// A tagged build identifies itself completely, so appending bytes to it would
// break the thing kb-init tells teams to pin. Only the two forms that cannot
// identify a build get the suffix.
func TestBuildDigestOnlyWhereTheVersionCannotIdentifyTheBuild(t *testing.T) {
	for _, v := range []string{"v0.2.0", "v1.0.0-rc1"} {
		if got := buildDigest(v); got != "" {
			t.Errorf("buildDigest(%q) = %q, want empty — a tagged build is already identified", v, got)
		}
	}
	for _, v := range []string{"dev", "v0.0.0-20260921151245-0764b97e1d8f+dirty"} {
		got := buildDigest(v)
		if got == "" {
			t.Fatalf("buildDigest(%q) = empty, want a digest — this version cannot identify a build on its own", v)
		}
		if !strings.HasPrefix(got, ".") || len(got) != 13 {
			t.Errorf("buildDigest(%q) = %q, want a leading dot and 12 hex chars", v, got)
		}
	}
}

// Two calls in one process must agree, or the string is worse than useless:
// an agent comparing it against the one it saw earlier would read every run as
// a new build.
func TestBuildDigestIsStableForOneBinary(t *testing.T) {
	a := buildDigest("dev")
	b := buildDigest("dev")
	if a != b {
		t.Errorf("buildDigest is not stable: %q then %q", a, b)
	}
}
