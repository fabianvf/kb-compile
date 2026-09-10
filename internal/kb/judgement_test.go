package kb

// Judgement calls the equivalence corpus cannot discriminate.
//
// A mutation pass over the golden harness caught the ownership and
// stem-matching rules but left three mutations alive, because the fixture
// happens to contain no case that distinguishes them. A mutation no case
// catches is a hole, and the hole is in the corpus, not in the rule — so each
// one gets a constructed case here instead.
//
// Every test below fails if its rule is inverted. That is the bar: an
// assertion that still passes when the behaviour flips is documentation.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fabianvf/kb-compile/internal/config"
)

func newTestChecker(t *testing.T, tracked []string) *Checker {
	t.Helper()
	cfg := &config.Config{
		KBDir:          "docs/kb",
		ProdRoots:      []string{"lib"},
		ProdExtensions: []string{".dart"},
		ArticleTypes:   map[string]string{"arch": "arch"},
	}
	return New(cfg, map[string]*Article{}, tracked)
}

// TestPathIsKnownIgnoresTheFilesystem pins the rule that git, not the working
// tree, decides what the repo contains.
//
// This is the one that shipped broken in the original: a generated file left
// over from a local test run made the check pass on a dev machine and fail in
// CI. A gate whose verdict depends on which machine runs it is worse than no
// gate, because the red build looks like someone else's problem.
func TestPathIsKnownIgnoresTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// A file that genuinely EXISTS on disk but which git does not track.
	if err := os.MkdirAll("lib", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("lib", "leftover.dart"), []byte("// build artifact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestChecker(t, []string{"lib/real.dart"})

	if c.PathIsKnown("lib/leftover.dart") {
		t.Error("an untracked file that exists on disk was reported as known — " +
			"the checker is probing the filesystem, so its verdict now differs " +
			"between a dev machine and CI")
	}
	if !c.PathIsKnown("lib/real.dart") {
		t.Error("a tracked file was reported as unknown")
	}

	// The escape hatch for real-but-gitignored generated files must still work,
	// and must work WITHOUT the file existing — that is what makes it portable.
	c.Cfg.GeneratedPaths = map[string]string{
		"lib/generated.dart": "emitted by the codegen step; gitignored",
	}
	if !c.PathIsKnown("lib/generated.dart") {
		t.Error("an allowlisted generated path was reported as unknown")
	}
}

// TestUnderscoreSplitDropsAmbiguity pins that a flattened test stem with more
// than one real interpretation yields NO link.
//
// pytest turns app/core/engine.py into test_core_engine.py, and reconstructing
// that is worth doing. But `test_a_b_c.py` can mean a/b_c.py or a_b/c.py, and
// if both exist there is no way to tell. Guessing produces a
// confident wrong answer: an agent is told which test guards its change, opens
// it, and finds it guards something else.
func TestUnderscoreSplitDropsAmbiguity(t *testing.T) {
	c := newTestChecker(t, []string{
		"app/alpha/beta_gamma.py", // test_alpha_beta_gamma.py, split at 1
		"app/alpha_beta/gamma.py", // ...or split at 2
		"app/solo/only.py",        // unambiguous
	})

	if got := c.underscoreSplit("app", ".py", "alpha_beta_gamma"); got != "" {
		t.Errorf("ambiguous stem resolved to %q; two tracked files match the "+
			"same flattened name, so the only correct answer is no link", got)
	}
	if got := c.underscoreSplit("app", ".py", "solo_only"); got != "app/solo/only.py" {
		t.Errorf("unambiguous stem = %q, want app/solo/only.py — the "+
			"ambiguity guard has eaten the legitimate case too", got)
	}
	if got := c.underscoreSplit("app", ".py", "nothing_here"); got != "" {
		t.Errorf("stem matching no tracked file resolved to %q; reconstruction "+
			"must build a real path, not a plausible one", got)
	}
}

// TestGlobMatchesRespectsDirectoryBoundaries pins that `dir/**` means "beneath
// dir", not "starting with dir".
//
// The difference only shows up when a sibling shares a prefix — lib/feed/ and
// lib/feedback/ — which is exactly when it is most damaging: an article
// silently claims a subsystem it does not document, and the orphan ratchet
// stops asking anyone to document the real one.
func TestGlobMatchesRespectsDirectoryBoundaries(t *testing.T) {
	cases := []struct {
		path, glob string
		want       bool
		why        string
	}{
		{"lib/feed/service.dart", "lib/feed/**", true, "directly beneath"},
		{"lib/feed/a/b/c.dart", "lib/feed/**", true, "nested arbitrarily deep"},
		{"lib/feedback/form.dart", "lib/feed/**", false,
			"a SIBLING sharing the prefix must not match"},
		{"lib/feed.dart", "lib/feed/**", false,
			"a file named like the directory is not inside it"},

		{"lib/main.dart", "lib/*.dart", true, "single star at one level"},
		{"lib/screens/home.dart", "lib/*.dart", false,
			"a single star must not cross a path separator"},
		{"lib/exact.dart", "lib/exact.dart", true, "a literal path is its own glob"},
	}
	for _, tc := range cases {
		if got := GlobMatches(tc.path, tc.glob); got != tc.want {
			t.Errorf("GlobMatches(%q, %q) = %v, want %v — %s",
				tc.path, tc.glob, got, tc.want, tc.why)
		}
	}
}

// TestIsProductionSourceExcludesBothTestConventions pins the scope rule that
// decides what the orphan ratchet governs.
//
// BOTH naming conventions matter and the original checked only one: Dart and
// JS mark a test with a `_test`/`.spec` SUFFIX, pytest with a `test_` PREFIX.
// Checking only the suffix let a pytest file count as production source, so
// the ratchet demanded KB ownership for a test file.
func TestIsProductionSourceExcludesBothTestConventions(t *testing.T) {
	cfg := &config.Config{
		ProdRoots:      []string{"lib", "app"},
		ProdExtensions: []string{".dart", ".py"},
		ArticleTypes:   map[string]string{"arch": "arch"},
		TestRules: config.TestRules{
			DirSubstrings:  []string{"/test/", "/tests/"},
			PathPrefixes:   []string{"test/"},
			BasePrefixes:   []string{"test_"},
			BaseSuffixes:   []string{"_test.dart", "_test.py"},
			PathSubstrings: []string{".spec."},
		},
	}
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{"lib/services/feed.dart", true, "ordinary production source"},
		{"app/core/engine.py", true, "ordinary production source"},
		{"lib/services/feed_test.dart", false, "suffix convention"},
		{"app/test_invite.py", false,
			"PREFIX convention — a pytest file directly under a prod root"},
		{"app/tests/test_core.py", false, "under a tests directory"},
		{"lib/widgets/thing.spec.js", false, "spec substring"},
		{"docs/notes.dart", false, "outside every prod root"},
		{"lib/assets/logo.png", false, "not a prod extension"},
	}
	for _, tc := range cases {
		if got := cfg.IsProductionSource(tc.path); got != tc.want {
			t.Errorf("IsProductionSource(%q) = %v, want %v — %s",
				tc.path, got, tc.want, tc.why)
		}
	}
}

// TestRatchetRefusesToGrow pins the rule that separates a ratchet from a todo
// list: --write may SHRINK a baseline and never widen it.
//
// Without this, regenerating after adding an undocumented file silently
// legitimises it. The gate keeps passing and stops gating, which is the single
// worst outcome available here — worse than failing, because nobody looks.
func TestRatchetRefusesToGrow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.txt")
	if err := os.WriteFile(path, []byte("# header\nlib/old.dart\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestChecker(t, nil)
	c.writeRatchet(path, "# header\n",
		[]string{"lib/old.dart", "lib/NEW.dart"}, "orphans", "claim it")

	if len(c.Errors) == 0 {
		t.Fatal("writing a GROWN baseline was allowed; the ratchet can now be " +
			"widened by regenerating, which makes it a todo list")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# header\nlib/old.dart\n" {
		t.Errorf("baseline was modified despite the refusal:\n%s", data)
	}

	// Shrinking is the whole point, and must still work.
	c2 := newTestChecker(t, nil)
	c2.writeRatchet(path, "# header\n", nil, "orphans", "claim it")
	if len(c2.Errors) != 0 {
		t.Fatalf("shrinking to empty was refused: %v", c2.Errors)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# header\n" {
		t.Errorf("shrunk baseline = %q, want just the header", data)
	}
}

// TestDeadEndNeedsALinkNotAHeading pins that an empty `## SEE ALSO` section
// does not count as an outbound edge.
//
// Otherwise the cheapest way past the dead-end ratchet is to paste the heading
// in, which produces an article that still points nowhere and a baseline that
// says it does.
func TestDeadEndNeedsALinkNotAHeading(t *testing.T) {
	cfg := &config.Config{ArticleTypes: map[string]string{"arch": "arch"}}
	dir := t.TempDir()

	write := func(name, body string) *Article {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		var errs []string
		a, err := ParseArticle(cfg, p, "arch-x", &errs)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	bare := write("bare.md", "---\nid: arch-x\ntype: arch\n---\n# X\n\n## SEE ALSO\n\n")
	if bare.HasOutboundEdge() {
		t.Error("an empty `## SEE ALSO` heading counted as an outbound edge — " +
			"pasting the heading in is now enough to clear the ratchet")
	}

	linked := write("linked.md",
		"---\nid: arch-x\ntype: arch\n---\n# X\n\n## SEE ALSO\n\n- [Y](arch-y.md) — owns the write path\n")
	if !linked.HasOutboundEdge() {
		t.Error("a `## SEE ALSO` section containing a real link was not counted")
	}

	fm := write("fm.md", "---\nid: arch-x\ntype: arch\nsee_also:\n  - arch-y\n---\n# X\n")
	if !fm.HasOutboundEdge() {
		t.Error("a frontmatter `see_also:` edge was not counted")
	}
}
