package kb

// The command strategy hands test-link discovery to the language's own
// tooling. That removes a whole class of approximation error and introduces a
// new one: the command can fail, or succeed while producing nothing.
//
// Producing nothing is the dangerous case, because an empty tests column is
// indistinguishable from a repo that has no tests. Every test below exists to
// make some flavour of "quietly produced no links" loud.

import (
	"strings"
	"testing"

	"github.com/fabianvf/kb-compile/internal/config"
)

func cmdChecker(t *testing.T, tracked []string, script string, optional bool) *Checker {
	t.Helper()
	cfg := &config.Config{
		ProdRoots:    []string{"app"},
		ArticleTypes: map[string]string{"arch": "arch"},
		TestRules:    config.TestRules{BaseSuffixes: []string{"_test.go"}},
		LinkCommands: []config.LinkCommand{{
			Name:     "probe",
			Command:  []string{"sh", "-c", script},
			Optional: optional,
		}},
	}
	return New(cfg, map[string]*Article{}, tracked)
}

func TestLinkCommandProducesEdges(t *testing.T) {
	c := cmdChecker(t, []string{"app/engine.go", "app/engine_test.go", "app/util.go"},
		`printf 'app/engine_test.go\tapp/engine.go\napp/engine_test.go\tapp/util.go\n'`, false)

	got := c.RunLinkCommands()
	for _, e := range c.Errors {
		t.Errorf("unexpected error: %s", e)
	}
	if want := []string{"app/engine_test.go"}; !equal(got["app/engine.go"], want) {
		t.Errorf("app/engine.go -> %v, want %v", got["app/engine.go"], want)
	}
	if !equal(got["app/util.go"], []string{"app/engine_test.go"}) {
		t.Errorf("a second edge from the same test was dropped: %v", got["app/util.go"])
	}
}

// TestLinkCommandEmptyOutputFails is the important one. A command that exits 0
// having printed nothing looks exactly like success, and the resulting empty
// column reads as "nothing to run here".
func TestLinkCommandEmptyOutputFails(t *testing.T) {
	c := cmdChecker(t, []string{"app/engine.go", "app/engine_test.go"}, `true`, false)
	c.RunLinkCommands()
	if !anyContains(c.Errors, "no edges at all") {
		t.Errorf("a command that produced nothing was accepted; errors: %v", c.Errors)
	}
}

func TestLinkCommandFailureIsFatalByDefault(t *testing.T) {
	c := cmdChecker(t, []string{"app/engine.go", "app/engine_test.go"},
		`echo "go: command not found" >&2; exit 127`, false)
	c.RunLinkCommands()
	if !anyContains(c.Errors, "go: command not found") {
		t.Errorf("the failure did not surface the command's own stderr, which "+
			"is the only thing that says WHICH tool is missing; errors: %v", c.Errors)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("a non-optional failure was downgraded to a warning: %v", c.Warnings)
	}
}

func TestLinkCommandOptionalFailureWarns(t *testing.T) {
	c := cmdChecker(t, []string{"app/engine.go", "app/engine_test.go"},
		`exit 127`, true)
	c.RunLinkCommands()
	if len(c.Errors) != 0 {
		t.Errorf("an optional command's failure was fatal: %v", c.Errors)
	}
	if !anyContains(c.Warnings, "read as stale") {
		t.Errorf("the warning does not say what the missing links will do to "+
			"the committed index; warnings: %v", c.Warnings)
	}
}

// TestLinkCommandRejectsUnknownPaths pins that the script's output is
// validated rather than trusted. A script emitting absolute paths, stale
// paths, or paths relative to its own directory would otherwise produce links
// that point nowhere and look real.
func TestLinkCommandRejectsUnknownPaths(t *testing.T) {
	tracked := []string{"app/engine.go", "app/engine_test.go"}

	for _, tc := range []struct {
		name, script, expect string
	}{
		{"untracked source", `printf 'app/engine_test.go\tapp/ghost.go\n'`,
			"app/ghost.go"},
		{"untracked test", `printf 'app/ghost_test.go\tapp/engine.go\n'`,
			"app/ghost_test.go"},
		{"absolute path", `printf 'app/engine_test.go\t/abs/app/engine.go\n'`,
			"/abs/app/engine.go"},
		{"malformed line", `printf 'app/engine_test.go app/engine.go\n'`,
			"expected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cmdChecker(t, tracked, tc.script, false)
			c.RunLinkCommands()
			if !anyContains(c.Errors, tc.expect) {
				t.Errorf("expected an error naming %q; got %v", tc.expect, c.Errors)
			}
		})
	}
}

// TestLinkCommandRejectsNonTestOnTheTestSide keeps the command and the
// configured test_rules in agreement.
//
// If the script calls something a test that the ratchet calls production
// source, the reverse index and the orphan baseline are describing different
// repos, and the disagreement is invisible in both.
func TestLinkCommandRejectsNonTestOnTheTestSide(t *testing.T) {
	c := cmdChecker(t, []string{"app/engine.go", "app/helper.go"},
		`printf 'app/helper.go\tapp/engine.go\n'`, false)
	c.RunLinkCommands()
	if !anyContains(c.Errors, "test_rules") {
		t.Errorf("a non-test on the test side was accepted; errors: %v", c.Errors)
	}
}

// TestMergeLinksUnions pins that adapters and commands compose, for a repo
// whose languages are not all served by one approach.
func TestMergeLinksUnions(t *testing.T) {
	a := map[string][]string{
		"app/engine.go": {"app/engine_test.go"},
		"web/client.js": {"web/client.spec.js"},
	}
	b := map[string][]string{
		"app/engine.go": {"app/integration_test.go"},
		"app/util.go":   {"app/util_test.go"},
	}
	got := MergeLinks(a, b)

	if want := []string{"app/engine_test.go", "app/integration_test.go"}; !equal(got["app/engine.go"], want) {
		t.Errorf("overlapping key = %v, want the union %v", got["app/engine.go"], want)
	}
	if !equal(got["web/client.js"], []string{"web/client.spec.js"}) {
		t.Errorf("a key present only in the first map was lost: %v", got["web/client.js"])
	}
	if !equal(got["app/util.go"], []string{"app/util_test.go"}) {
		t.Errorf("a key present only in the second map was lost: %v", got["app/util.go"])
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func anyContains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
