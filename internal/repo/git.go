// Package repo answers "what files does this repository contain?".
package repo

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// TrackedFiles lists every file git tracks, sorted, minus excluded paths.
//
// Deliberately NOT a filesystem walk. A build artifact left in a working tree
// would make the answer differ between a dev machine and CI, which is the one
// thing a gate must never do. The original checker learned this the hard way:
// a generated file left over from a local test run made the check pass locally
// and fail in CI.
func TrackedFiles(exclude []string) ([]string, error) {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git ls-files failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" {
			continue
		}
		skip := false
		for _, e := range exclude {
			if strings.Contains(l, e) {
				skip = true
				break
			}
		}
		if !skip {
			files = append(files, l)
		}
	}
	sort.Strings(files)
	return files, nil
}
