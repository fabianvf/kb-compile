// Package repo answers "what files does this repository contain?".
package repo

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// TrackedFiles lists every file git tracks, sorted, minus paths the caller
// excludes.
//
// The predicate comes from the caller rather than a list of patterns matched
// here, so that "is this path excluded?" has exactly one implementation. The
// version that took a []string and matched it itself had a second, subtly
// different copy of the rule in config, and the two could disagree about a
// path without anything failing.
//
// Deliberately NOT a filesystem walk. A build artifact left in a working tree
// would make the answer differ between a dev machine and CI, which is the one
// thing a gate must never do. The original checker learned this the hard way:
// a generated file left over from a local test run made the check pass locally
// and fail in CI.
func TrackedFiles(excluded func(string) bool) ([]string, error) {
	// -z is not optional. Without it git applies core.quotePath, which is ON
	// by default, so `src/café.go` comes back as the 14-character string
	// `"src/caf\303\251.go"` — quotes and octal escapes included. That never
	// matches a prod root or a `covers:` glob, so a non-ASCII source file is
	// silently outside every gate, while a link command reporting its real
	// UTF-8 path is told git does not track it. NUL-separated output is
	// literal, and it is also the only form safe for a path with a newline.
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git ls-files failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}
	var files []string
	// During an unresolved merge git lists a conflicted path once per stage,
	// so the same file arrives three times and every per-file check reports
	// it three times. Mid-merge is exactly when someone runs this.
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\x00") {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if excluded != nil && excluded(l) {
			continue
		}
		files = append(files, l)
	}
	sort.Strings(files)
	return files, nil
}

// UntrackedSource lists files git does not track, sorted, that the caller's
// predicate accepts. Intended for one thing: warning that a gate's view of the
// repository is narrower than the author's.
//
// TrackedFiles is deliberately not a filesystem walk, because a build artifact
// in a working tree would make a gate answer differently on a dev machine than
// in CI. That reasoning still holds and this does not weaken it — the result
// here must never change a pass into a fail or back. It exists because the
// blind spot has a cost the other direction: a whole new subsystem is
// untracked until someone stages it, so `covers:` matches nothing and a
// compile marker records a narrower set than the person running it believes.
// Silent, and indistinguishable from success.
//
// --exclude-standard applies .gitignore, so genuine build output is already
// out of scope and what remains is source somebody has written and not yet
// added.
func UntrackedSource(accept func(string) bool) ([]string, error) {
	out, err := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git ls-files --others failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files --others failed: %w", err)
	}
	var files []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\x00") {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if accept != nil && !accept(l) {
			continue
		}
		files = append(files, l)
	}
	sort.Strings(files)
	return files, nil
}
