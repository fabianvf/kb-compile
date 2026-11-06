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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

// ── Found by running kb-init against an unfamiliar repo ─────────────────────
//
// Both of these shipped green through the fixture suite. The fixture had no
// case that produced either, which is the recurring lesson: a corpus proves
// what it contains, and the first contact with a real codebase is where the
// gaps show up.

// TestSelfLinkIsNotAnOutboundEdge pins that a `## SEE ALSO` pointing at its
// own article does not clear the dead-end ratchet.
//
// Otherwise the cheapest way past the gate is to link the article to itself:
// the section exists, it contains a link, the ratchet is satisfied, and a
// reader who follows it is exactly where they started. Frontmatter `see_also:`
// already rejected self-edges; the section path did not, and nothing tested it.
func TestSelfLinkIsNotAnOutboundEdge(t *testing.T) {
	cfg := &config.Config{ArticleTypes: map[string]string{"arch": "arch"}}
	dir := t.TempDir()

	parse := func(name, body string) *Article {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		var errs []string
		a, err := ParseArticle(cfg, p, "arch-self", &errs)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	const head = "---\nid: arch-self\ntype: arch\n---\n# Self\n\n## SEE ALSO\n\n"

	if parse("a.md", head+"- [arch-self.md](arch-self.md) — see above.\n").HasOutboundEdge() {
		t.Error("a SEE ALSO link to the article's own id counted as an outbound " +
			"edge; the dead-end ratchet can now be cleared by linking to self")
	}
	if !parse("b.md", head+"- [arch-other.md](arch-other.md) — owns the write path.\n").HasOutboundEdge() {
		t.Error("a link to another article was not counted")
	}
	// A self-link alongside a real one must still count: the real one is the edge.
	if !parse("c.md", head+
		"- [arch-self.md](arch-self.md) — see above.\n"+
		"- [arch-other.md](arch-other.md) — owns the write path.\n").HasOutboundEdge() {
		t.Error("a real edge was discarded because a self-link sat beside it")
	}
}

// TestStemLinkRejectsUniversalFilenames pins that a single-word stem does not
// match across directories.
//
// Universal filenames recur once per package — `main`, `index`, `client`,
// `utils`. Requiring the same LANGUAGE was not enough: Go has one `main.go`
// per command, so every entry point linked to every unrelated entry-point test
// three subsystems away. The link has to be same-directory, or the stem has to
// be distinctive enough to mean something on its own.
func TestStemLinkRejectsUniversalFilenames(t *testing.T) {
	cfg := &config.Config{LangFamilies: map[string]string{
		".go": "go", ".py": "py", ".dart": "dart", ".js": "js", ".mjs": "js",
	}}
	c := New(cfg, map[string]*Article{}, nil)

	cases := []struct {
		test, candidate, stem string
		want                  bool
		why                   string
	}{
		{"lsp/protocol/generate/main_test.go", "cmd/analyzer/main.go", "main", false,
			"single-word stem, different directories, unrelated programs"},
		{"web/index.spec.js", "server/index.js", "index", false,
			"`index` recurs once per directory in JS"},

		{"cmd/analyzer/main_test.go", "cmd/analyzer/main.go", "main", true,
			"same directory: the ordinary convention, no ambiguity possible"},
		{"web/client.spec.js", "web/client.js", "client", true,
			"same directory"},

		{"functions/tests/test_join_org_challenge.py",
			"lib/services/callables/join_org_challenge.dart", "join_org_challenge", true,
			"distinctive multi-word stem across languages: two halves of one " +
				"contract, and no import can ever reveal it"},
		{"tests/parse_ruleset_test.go", "engine/parse_ruleset.go", "parse_ruleset", true,
			"distinctive multi-word stem, same language, different directory"},
	}
	for _, tc := range cases {
		if got := c.stemLinkAllowed(tc.test, tc.candidate, tc.stem); got != tc.want {
			t.Errorf("stemLinkAllowed(%q, %q, %q) = %v, want %v — %s",
				tc.test, tc.candidate, tc.stem, got, tc.want, tc.why)
		}
	}
}

// TestTaxonomyIsOptional pins that a repo can adopt this without renaming its
// existing documentation.
//
// The type prefix carries no graph meaning: ownership, edges and both ratchets
// work identically without it. Requiring it made adoption start with a rename
// of every file, which is friction bought with nothing.
func TestTaxonomyIsOptional(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// No article_types configured: any filename, and `type:` not required.
	loose := &config.Config{ProdRoots: []string{"src"}}
	p := write("authentication.md", "---\nid: authentication\n---\n# Auth\n")
	var errs []string
	a, err := ParseArticle(loose, p, "authentication", &errs)
	if err != nil {
		t.Fatal(err)
	}
	c := New(loose, map[string]*Article{"authentication": a}, nil)
	c.CheckFrontmatter()
	if len(c.Errors) != 0 {
		t.Errorf("an untyped article failed with no taxonomy configured: %v", c.Errors)
	}

	// With a taxonomy configured, the same article must fail: opting in means
	// opting in, or the setting would be decorative.
	strict := &config.Config{
		ProdRoots:    []string{"src"},
		ArticleTypes: map[string]string{"arch": "arch"},
	}
	c2 := New(strict, map[string]*Article{"authentication": a}, nil)
	c2.CheckFrontmatter()
	if len(c2.Errors) == 0 {
		t.Error("a configured taxonomy did not reject an off-taxonomy filename")
	}
}

// TestVendoredTreesAreExcludedByDefault pins that adoption does not begin with
// thousands of orphans from committed dependencies.
//
// A baseline nobody could ever work through is a baseline nobody reads, and
// the ratchet only works if people look at the list.
func TestVendoredTreesAreExcludedByDefault(t *testing.T) {
	cfg := &config.Config{ProdRoots: []string{"src"}, ProdExtensions: []string{".go", ".js"}}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"src/vendor/github.com/x/y.go",
		"src/node_modules/pkg/index.js",
		"src/third_party/lib.go",
	} {
		if cfg.IsProductionSource(p) {
			t.Errorf("%s counted as production source; a committed dependency "+
				"tree is not this repo's code", p)
		}
	}
	if !cfg.IsProductionSource("src/app.go") {
		t.Error("ordinary source was excluded")
	}
	// An explicit setting replaces the defaults rather than extending them,
	// so a repo that genuinely documents its vendor tree can say so.
	custom := &config.Config{
		ProdRoots: []string{"src"}, ProdExtensions: []string{".go"},
		ExcludeDirs: []string{"generated"},
	}
	if err := custom.Finalize(); err != nil {
		t.Fatal(err)
	}
	if !custom.IsProductionSource("src/vendor/x.go") {
		t.Error("an explicit exclude list did not replace the defaults")
	}
}

// ── Found by a review of the implementation this was ported from ───────────

// TestRatchetBootstrapIsExplicit pins that a missing baseline is not a bypass.
//
// The growth guard used to be skipped entirely when the file was absent, so
// `rm .orphans-baseline.txt && kb graph --write` widened the ratchet silently
// and permanently. The read-only error recommended that exact command.
//
// Absence is indistinguishable from deletion, which is the same reasoning that
// makes a missing freshness manifest a failure rather than a skip. Bootstrap
// stays possible, but has to be asked for.
func TestRatchetBootstrapIsExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.txt")

	// Bootstrap is a per-run field rather than an environment variable, so
	// there is no ambient value to pin here: an earlier version of this test
	// had to clear KB_RATCHET_INIT, because a developer who had followed
	// kb-init's instructions had it exported and the test then failed
	// accusing the code of the bug the shell had caused.

	c := newTestChecker(t, nil)
	c.writeRatchet(path, "# h\n", []string{"lib/a.dart", "lib/b.dart"}, "orphans", "claim them")
	if len(c.Errors) == 0 {
		t.Fatal("writing entries with no baseline present was allowed; deleting " +
			"the file is now a way to widen the ratchet")
	}
	if !anyContains(c.Errors, BootstrapFlag) {
		t.Errorf("the refusal does not name the bootstrap escape hatch, so a "+
			"first-time adopter is stuck: %v", c.Errors)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the baseline was written despite the refusal")
	}

	// Bootstrap, explicitly.
	c2 := newTestChecker(t, nil)
	c2.AllowRatchetInit = true
	c2.writeRatchet(path, "# h\n", []string{"lib/a.dart", "lib/b.dart"}, "orphans", "claim them")
	if len(c2.Errors) != 0 {
		t.Fatalf("explicit bootstrap was refused: %v", c2.Errors)
	}
	got, err := readBaseline(path)
	if err != nil || len(got) != 2 {
		t.Errorf("bootstrap wrote %v (err=%v), want both entries", got, err)
	}

	// Bootstrap is for a baseline that does not exist. Now that one does, the
	// hatch must stop working, or `kb graph --write --init` is the
	// `rm && kb graph --write` bypass with one extra token — and it is the
	// command both refusal messages print.
	c3 := newTestChecker(t, nil)
	c3.AllowRatchetInit = true
	c3.writeRatchet(path, "# h\n",
		[]string{"lib/a.dart", "lib/b.dart", "lib/NEW.dart"}, "orphans", "claim them")
	if len(c3.Errors) == 0 {
		t.Error("bootstrap widened a baseline that already existed; the " +
			"escape hatch is a general way past the ratchet")
	}
}

// TestMissingBaselineDoesNotRecommendTheBypass pins the error text.
//
// The old message said "Run `kb graph --write` to create it", which was the
// bypass. An error that recommends the hole is worse than no error.
func TestMissingBaselineDoesNotRecommendTheBypass(t *testing.T) {
	c := newTestChecker(t, nil)
	c.checkRatchet(filepath.Join(t.TempDir(), "absent.txt"), nil, func(string) {}, "orphans")
	if len(c.Errors) == 0 {
		t.Fatal("a missing baseline was accepted")
	}
	if anyContains(c.Errors, "Run `kb graph --write`") {
		t.Errorf("the error recommends the bypass it exists to close: %v", c.Errors)
	}
	if !anyContains(c.Errors, "git checkout") {
		t.Errorf("the error does not say what to actually do: %v", c.Errors)
	}
}

// TestManifestConflictIsExplained pins the message for the failure the design
// deliberately produces.
//
// Per-file state means two branches compiling the same file conflict on
// purpose. Hitting that is normal, so the error has to rule out the obvious
// fix: `kb compiled` would resolve it by re-recording every file on both sides
// as reviewed, including the ones nobody looked at.
func TestManifestConflictIsExplained(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".compiled-sources")
	conflicted := "file a.go\n<<<<<<< HEAD\nsha256 aaa\n=======\nsha256 bbb\n>>>>>>> other\nend a.go\n"
	if err := os.WriteFile(path, []byte(conflicted), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Freshness(path, map[string][]string{"a.go": {"arch-x"}})
	if err == nil {
		t.Fatal("a conflicted manifest parsed successfully")
	}
	for _, want := range []string{"merge conflict", "Do NOT resolve it with `kb compiled`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// ── Found by a full-codebase review ────────────────────────────────────────

// TestBaselineRejectsConflictMarkers pins that a conflicted baseline is
// refused rather than parsed.
//
// Read as entries, the markers make `existing` the UNION of both branches, so
// `--write` accepts an entry only one side ever grandfathered — the widening
// the ratchet exists to refuse. The read-only error even said to run
// `--write`, which resolved the conflict silently and picked a winner nobody
// chose.
func TestBaselineRejectsConflictMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	conflicted := "# h\n<<<<<<< HEAD\nlib/a.dart\n=======\nlib/b.dart\n>>>>>>> other\n"
	if err := os.WriteFile(path, []byte(conflicted), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readBaseline(path); err == nil {
		t.Fatal("a conflicted baseline parsed as entries; the union of both " +
			"branches is now grandfathered")
	}

	// And it must not be resolvable by regenerating.
	c := newTestChecker(t, nil)
	c.writeRatchet(path, "# h\n", []string{"lib/b.dart"}, "orphans", "claim it")
	if len(c.Errors) == 0 {
		t.Error("`--write` resolved a conflicted baseline instead of refusing")
	}
	c2 := newTestChecker(t, nil)
	c2.checkRatchet(path, []string{"lib/b.dart"}, func(string) {}, "orphans")
	if !anyContains(c2.Errors, "conflict") {
		t.Errorf("the read-only error does not name the conflict: %v", c2.Errors)
	}
}

// TestUnreadableBaselineIsNotReportedAsMissing pins that only ENOENT reads as
// absence.
//
// Collapsing every read error into "missing" told the reader to restore a file
// that was sitting right there, and offered them the bootstrap hatch for a
// baseline nobody had deleted.
func TestUnreadableBaselineIsNotReportedAsMissing(t *testing.T) {
	// A directory at the path: EISDIR, not ENOENT, and it needs no chmod to
	// reproduce as root in a container.
	path := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := readBaseline(path); err == nil || errors.Is(err, ErrNoBaseline) {
		t.Fatalf("an unreadable baseline read as absent: %v", err)
	}
	c := newTestChecker(t, nil)
	c.checkRatchet(path, nil, func(string) {}, "orphans")
	if anyContains(c.Errors, "is missing") {
		t.Errorf("an unreadable baseline was reported as missing: %v", c.Errors)
	}
}

// TestBaselineCommentsAreTrimmedFirst pins that the `#` test runs on the
// trimmed line, not the raw one.
//
// An indented `  # note` was stored under the key "# note", and the build then
// failed reporting that a comment no longer qualified as a file.
func TestBaselineCommentsAreTrimmedFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(path, []byte("# top\n  # indented\nlib/a.dart\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["lib/a.dart"] {
		t.Errorf("readBaseline = %v, want only lib/a.dart; an indented "+
			"comment became an entry", got)
	}
}

// TestHalfResolvedManifestIsStillExplained pins that every conflict marker
// counts, not just the opening one.
//
// Deleting `<<<<<<<` and the losing hunk by hand and missing the tail is the
// usual editor flow, and it is exactly the state where the reader most needs
// to be told not to reach for `kb compiled`.
func TestHalfResolvedManifestIsStillExplained(t *testing.T) {
	for _, leftover := range []string{"=======", ">>>>>>> other"} {
		path := filepath.Join(t.TempDir(), ".compiled-sources")
		body := "file a.go\nsha256 aaa\n" + leftover + "\nend a.go\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := Freshness(path, map[string][]string{"a.go": {"arch-x"}})
		if err == nil {
			t.Fatalf("%q: a conflicted manifest parsed successfully", leftover)
		}
		if !strings.Contains(err.Error(), "Do NOT resolve it with `kb compiled`") {
			t.Errorf("%q: the reader is given a parser diagnostic instead of "+
				"the one thing they must not do: %v", leftover, err)
		}
	}
}

// TestFencedHeadingIsAnExampleNotASection pins that a heading inside a code
// fence does not open a section.
//
// Read as one it is a free pass past both ratchets: a fenced `## SEE ALSO`
// sample clears the dead-end gate, and a fenced `## FILES` sample claims
// ownership of the paths it shows, erasing them from the orphan gate. An
// article documenting the KB format is the ordinary way to hit this.
func TestFencedHeadingIsAnExampleNotASection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arch-x.md")
	body := "---\nid: arch-x\ntype: arch\n---\n# X\n\n" +
		"An article may declare ownership like this:\n\n" +
		"```markdown\n## FILES\n\nlib/undocumented.dart\n\n" +
		"## SEE ALSO\n\n- [arch-y.md](arch-y.md) - reason\n```\n\n" +
		"### Notes\n\nNothing else.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PathExtensions: []string{".dart"}}
	var errs []string
	a, err := ParseArticle(cfg, path, "arch-x", &errs)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.FilesBlock) != 0 {
		t.Errorf("a fenced `## FILES` example claimed ownership of %v; those "+
			"files are now out of the orphan ratchet", a.FilesBlock)
	}
	if a.HasSeeAlsoSectionLink {
		t.Error("a fenced `## SEE ALSO` example cleared the dead-end ratchet")
	}
}

// TestFilesSectionIsClosedByAnyHeading pins that a `### ` subhead ends a
// `## FILES` section.
//
// Only `## ` used to close it, so backticked paths in the prose below a
// subhead became silent ownership claims against an article that never
// documents them.
func TestFilesSectionIsClosedByAnyHeading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arch-x.md")
	body := "---\nid: arch-x\ntype: arch\n---\n# X\n\n## FILES\n\n" +
		"| Path | Role |\n| --- | --- |\n| `lib/owned.dart` | the thing |\n\n" +
		"### Notes\n\nThe upstream copy at `vendor/other/copy.dart` is not ours.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PathExtensions: []string{".dart"}}
	var errs []string
	a, err := ParseArticle(cfg, path, "arch-x", &errs)
	if err != nil {
		t.Fatal(err)
	}
	if a.FilesBlock["lib/owned.dart"] == 0 {
		t.Error("the real FILES table entry was dropped")
	}
	if _, claimed := a.FilesBlock["vendor/other/copy.dart"]; claimed {
		t.Error("prose under a `### ` subhead was read as an ownership claim; " +
			"the FILES section never closed")
	}
}

// TestFrontmatterAcceptsEveryListSpelling pins that a correctly-spelled
// `covers:` is never silently empty.
//
// A zero-indent `- item` and an inline `[a, b]` are both valid YAML and both
// parsed to nothing, so the article owned no files and they landed in the
// orphan baseline. That is the same silent no-op that rejecting unknown
// frontmatter keys exists to prevent, so a scalar now fails the same way.
func TestFrontmatterAcceptsEveryListSpelling(t *testing.T) {
	cfg := &config.Config{PathExtensions: []string{".dart"}}
	cases := []struct {
		name, frontmatter string
		want              []string
	}{
		{"indented block", "covers:\n  - lib/a.dart\n  - lib/b.dart\n",
			[]string{"lib/a.dart", "lib/b.dart"}},
		{"zero-indent block", "covers:\n- lib/a.dart\n- lib/b.dart\n",
			[]string{"lib/a.dart", "lib/b.dart"}},
		{"inline", "covers: [lib/a.dart, lib/b.dart]\n",
			[]string{"lib/a.dart", "lib/b.dart"}},
		{"explicit empty", "covers: []\n", nil},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "arch-x.md")
		body := "---\nid: arch-x\ntype: arch\n" + tc.frontmatter + "---\n# X\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		var errs []string
		a, err := ParseArticle(cfg, path, "arch-x", &errs)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(a.Covers, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: covers = %v, want %v — the article silently owns "+
				"nothing and its files fall into the orphan baseline",
				tc.name, a.Covers, tc.want)
		}
		if len(errs) != 0 {
			t.Errorf("%s: unexpected errors %v", tc.name, errs)
		}
	}

	// A scalar is not a list, and must SAY so rather than vanishing.
	path := filepath.Join(t.TempDir(), "arch-x.md")
	if err := os.WriteFile(path,
		[]byte("---\nid: arch-x\ntype: arch\ncovers: lib/a.dart\n---\n# X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errs []string
	if _, err := ParseArticle(cfg, path, "arch-x", &errs); err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Error("a scalar `covers:` was accepted and read as nothing at all")
	}
}

// TestDigestDoesNotMutateTheArticle pins that a digest is built from a COPY of
// the article's edges.
//
// `ArticleDigest.SeeAlso` shared the article's own slice, so the `## SEE ALSO`
// merge appended into whatever spare capacity the parser had left and the sort
// reordered it in place. A declared edge silently vanished from
// `Article.SeeAlso`, and a second build rendered a different digest.
func TestDigestDoesNotMutateTheArticle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arch-x.md")
	body := "---\nid: arch-x\ntype: arch\nsee_also:\n  - arch-b\n  - arch-c\n" +
		"  - arch-d\n---\n# X\n\n## SEE ALSO\n\n- [arch-a.md](arch-a.md) - reason\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{KBDir: dir, PathExtensions: []string{".dart"}}
	var errs []string
	a, err := ParseArticle(cfg, path, "arch-x", &errs)
	if err != nil {
		t.Fatal(err)
	}
	declared := strings.Join(a.SeeAlso, ",")

	c := New(cfg, map[string]*Article{"arch-x": a}, nil)
	first := c.BuildDigest()
	if got := strings.Join(a.SeeAlso, ","); got != declared {
		t.Errorf("building the digest rewrote the article's own edges: %q, "+
			"want %q", got, declared)
	}
	// And so the digest is the same every time it is built.
	second := c.BuildDigest()
	if strings.Join(first[0].SeeAlso, ",") != strings.Join(second[0].SeeAlso, ",") {
		t.Errorf("digest is not idempotent: %v then %v",
			first[0].SeeAlso, second[0].SeeAlso)
	}
}

// TestReadArticleResolvesBeforeEmitting pins that `kb read id#SECTION` returns
// ONE section, and says so when it cannot tell which.
//
// Matching per line meant every heading that matched contributed its body, so
// an ambiguous stem returned two sections glued together with nothing to say
// so, and a single empty `## ` heading matched every query. A `## ` line
// inside a fence was treated as a heading, which truncated the section
// actually being read and served the code block back as a section that does
// not exist.
func TestReadArticleResolvesBeforeEmitting(t *testing.T) {
	dir := t.TempDir()
	body := "---\nid: arch-x\n---\n# X\n\n" +
		"## INVARIANT\n\nThe rule holds.\n\n" +
		"```markdown\n## FILES\n\nlib/a.dart\n```\n\n" +
		"and the checker reads both shapes.\n\n" +
		"## IMPLEMENTATION\n\nHow it is done.\n"
	if err := os.WriteFile(filepath.Join(dir, "arch-x.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadArticle(dir, "arch-x", "INVARIANT")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "and the checker reads both shapes.") {
		t.Errorf("the section was truncated at a fenced `## ` line, so the "+
			"agent silently got a partial invariant:\n%s", got)
	}
	if strings.Contains(got, "How it is done.") {
		t.Errorf("the section ran past the next heading:\n%s", got)
	}

	// A `## ` inside a fence is not a section anyone can ask for.
	if _, err := ReadArticle(dir, "arch-x", "FILES"); err == nil {
		t.Error("a heading inside a code fence was served as a section")
	}

	// `#I` matches both INVARIANT and IMPLEMENTATION: say so, do not staple
	// them together and let the reader believe they are one place.
	out, err := ReadArticle(dir, "arch-x", "I")
	if err == nil {
		t.Errorf("an ambiguous section silently returned %d bytes spanning "+
			"two sections", len(out))
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("the error does not name the ambiguity: %v", err)
	}

	// An exact match still wins over a prefix.
	if _, err := ReadArticle(dir, "arch-x", "INVARIANT"); err != nil {
		t.Errorf("an exact section name was rejected: %v", err)
	}
}

// TestExcludeDirsMatchesSegmentsNotSubstrings pins that exclusion is a
// directory NAME, not a substring of the path.
//
// The predecessor matched substrings, and its default list carried both
// "vendor/" and "/vendor/" to cover the root and the nested case — the
// segment rule hand-rolled, and wrong. The unanchored spelling also matched
// `src/myvendor/x.go`, so real source dropped out of the inventory and no
// gate ever asked anyone to document it. A file this tool cannot see is the
// one failure a documentation gate must not have: it is indistinguishable
// from a file that is already documented.
func TestExcludeDirsMatchesSegmentsNotSubstrings(t *testing.T) {
	cfg := &config.Config{
		ProdRoots:      []string{"lib"},
		ProdExtensions: []string{".dart"},
		ExcludeDirs:    []string{"vendor"},
	}
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"lib/vendor/dep.dart", false, "a real vendored tree must still be excluded"},
		{"lib/a/vendor/dep.dart", false, "nested vendored trees too"},
		{"lib/myvendor/x.dart", true, "a directory merely ENDING in vendor is this repo's source"},
		{"lib/vendored/x.dart", true, "a directory merely STARTING with vendor is this repo's source"},
		{"lib/vendor.dart", true, "a FILE named vendor is source; only directory segments are excluded"},
	} {
		if got := cfg.IsProductionSource(tc.path); got != tc.want {
			t.Errorf("IsProductionSource(%q) = %v, want %v — %s",
				tc.path, got, tc.want, tc.why)
		}
	}
}

// TestEvalVerdictNeverCallsAWorseKBTheSame pins the one sentence in `kb eval`
// that people act on.
//
// The verdict switch had a hole: materially worse recall combined with better
// recall-per-file-read matched no branch and fell through to the default,
// which says the KB "scores about the same ... on both recall and reading
// cost". A KB missing three quarters of every change was told it was a wash.
func TestEvalVerdictNeverCallsAWorseKBTheSame(t *testing.T) {
	// Worse recall by 45 points, but half the reading — so it is cheaper per
	// file read and branch 3 ("no cheaper to read") does not apply either.
	r := &EvalResult{Commits: 60, Files: 200, Recall: 0.35, Reach: 4, BaseR: 0.80, BaseCh: 20}
	out := r.Render(0)

	if strings.Contains(out, "about the same") {
		t.Errorf("a KB recalling %.0f points less was reported as a wash:\n%s",
			(r.BaseR-r.Recall)*100, out)
	}
	if !strings.Contains(out, "LESS") {
		t.Errorf("the verdict does not say the recall is worse:\n%s", out)
	}
}

// TestUnownedCountsOnlyScoredCommits pins that the two halves of the coverage
// ratio come from the same commits.
//
// Unowned was counted while walking every commit, but Files only over the
// commits that qualified for scoring. The report asks the reader to judge one
// against the other, so drawing them from different populations overstated
// how little of the repo the score covers — and the reader's response to that
// number is to distrust a score that was fine.
func TestUnownedCountsOnlyScoredCommits(t *testing.T) {
	dir := newGitRepo(t)
	// One commit that qualifies: two owned files, plus one unowned.
	writeCommit(t, dir, "first", map[string]string{
		"lib/a.dart": "a", "lib/b.dart": "b", "lib/loose.dart": "x",
	})
	// One that does NOT qualify: a single owned file, plus three unowned.
	writeCommit(t, dir, "second", map[string]string{
		"lib/a.dart": "a2", "lib/p.dart": "p", "lib/q.dart": "q", "lib/r.dart": "r",
	})

	cfg := &config.Config{
		ProdRoots:      []string{"lib"},
		ProdExtensions: []string{".dart"},
		ArticleTypes:   map[string]string{"arch": "arch"},
	}
	c := New(cfg, map[string]*Article{}, []string{
		"lib/a.dart", "lib/b.dart", "lib/loose.dart",
		"lib/p.dart", "lib/q.dart", "lib/r.dart",
	})
	ownership := map[string][]string{"lib/a.dart": {"arch-x"}, "lib/b.dart": {"arch-x"}}

	res, err := c.Evaluate(ownership, 50, 15)
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 1 {
		t.Fatalf("scored %d commits, want 1 — the fixture no longer isolates the bug", res.Commits)
	}
	if res.Unowned != 1 {
		t.Errorf("Unowned = %d, want 1: the 3 unowned files in the unscored "+
			"commit contributed to the numerator while contributing nothing "+
			"to Files, so the coverage ratio the report prints is inflated",
			res.Unowned)
	}
}

// newGitRepo makes an empty repo and chdirs into it: `kb eval` reads history
// with `git log` in the working directory, so there is no seam to inject.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.invalid")
	run("config", "user.name", "t")

	// `git log` runs in the working directory, and the fixture has to be it.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return dir
}

// writeCommit writes each path with the given content and commits them.
func writeCommit(t *testing.T, dir, subject string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", subject}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestUntrackedArticleFailsOnceTheKBIsCommitted pins the one place this tool
// read the filesystem instead of asking git.
//
// Every other inventory comes from `git ls-files`, because a gate whose answer
// depends on one working tree is not a gate. The article loader did not, so an
// article nobody had `git add`ed passed every check locally while the DIGEST
// and reverse index beside it — which ARE committed — went on naming it. Two of
// the first two repositories to adopt this shipped exactly that: derived files
// describing articles the repo did not contain, and a `kb graph` that failed
// for everyone but the author.
func TestUntrackedArticleFailsOnceTheKBIsCommitted(t *testing.T) {
	cfg := &config.Config{KBDir: "docs/kb", ProdRoots: []string{"lib"},
		ProdExtensions: []string{".dart"}}
	art := &Article{ID: "arch-a", Path: "docs/kb/arch-a.md"}
	articles := map[string]*Article{"arch-a": art}

	// Nothing under docs/kb is tracked: someone is writing their first
	// articles, which is not yet a contract with anybody.
	c := New(cfg, articles, []string{"lib/a.dart"})
	c.CheckArticlesAreTracked()
	if len(c.Errors) != 0 {
		t.Errorf("fired on a KB that has never been committed, so the first "+
			"`kb graph` of a new KB fails: %v", c.Errors)
	}

	// A DIGEST is committed, the article is not. This is the broken state:
	// the derived file names a source file the repo does not have.
	c2 := New(cfg, articles, []string{"lib/a.dart", "docs/kb/DIGEST.md"})
	c2.CheckArticlesAreTracked()
	if !anyContains(c2.Errors, "docs/kb/arch-a.md") {
		t.Fatalf("an untracked article passed while the KB was committed; the "+
			"next commit ships derived files describing a file nobody has: %v",
			c2.Errors)
	}
	if !anyContains(c2.Errors, "git add") {
		t.Errorf("the error does not name the fix: %v", c2.Errors)
	}

	// Tracked: nothing to say.
	c3 := New(cfg, articles, []string{"lib/a.dart", "docs/kb/DIGEST.md",
		"docs/kb/arch-a.md"})
	c3.CheckArticlesAreTracked()
	if len(c3.Errors) != 0 {
		t.Errorf("a tracked article was reported: %v", c3.Errors)
	}
}

// TestFilesBlockProseIsNotAnOwnershipClaim pins that only a list item or a
// table row inside `## FILES` claims a file.
//
// The section runs to the next heading, so a paragraph written under the table
// claimed every backticked path in it. That is not cosmetic: an owned file is
// removed from the orphan ratchet, so mentioning a path in passing silently
// took it out of the gate — the one direction this tool must never move a file
// without being asked. Found by a first-time adopter whose article said
// 58 files owned against 57 production files.
func TestFilesBlockProseIsNotAnOwnershipClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arch-a.md")
	body := "---\nid: arch-a\ntype: arch\n---\n\n# A\n\n## FILES\n\n" +
		"| Path | Role |\n|---|---|\n" +
		"| `lib/claimed.dart` | the real claim |\n\n" +
		"This paragraph mentions `lib/mentioned.dart` while explaining the\n" +
		"table above, and must NOT thereby own it.\n\n" +
		"- `lib/listed.dart` - a list item is a claim\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{KBDir: dir, ProdExtensions: []string{".dart"},
		PathExtensions: []string{".dart"}, ArticleTypes: map[string]string{"arch": "arch"}}
	var errs []string
	a, err := ParseArticle(cfg, path, "arch-a", &errs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lib/claimed.dart", "lib/listed.dart"} {
		if _, ok := a.FilesBlock[want]; !ok {
			t.Errorf("%s is not claimed; a table row and a list item are both "+
				"ownership claims per SPEC §3", want)
		}
	}
	if _, ok := a.FilesBlock["lib/mentioned.dart"]; ok {
		t.Error("a path named in explanatory prose was claimed, which removes " +
			"it from the orphan ratchet without anyone asking")
	}
}

// TestEvalFollowsOneSeeAlsoHop pins that the score measures a GRAPH.
//
// It measured a partition: reach was the files of the entry's own articles and
// nothing else, so `## SEE ALSO` - the one affordance a KB has that a directory
// tree does not - earned exactly zero. Two consequences, both observed on real
// repositories. A KB that beat the tree at matched reading cost reported as
// though it were losing, because 5 files read was being compared against 22.
// And merging was the only lever the number could see, so the advice kept
// pushing it even where better edges were the cheaper, better fix.
func TestEvalFollowsOneSeeAlsoHop(t *testing.T) {
	dir := newGitRepo(t)
	// Two files that always change together, deliberately split across two
	// articles joined by an edge. The carve is "wrong" as a partition and
	// right as a graph.
	writeCommit(t, dir, "one", map[string]string{
		"lib/a.dart": "a", "lib/b.dart": "b",
	})
	writeCommit(t, dir, "two", map[string]string{
		"lib/a.dart": "a2", "lib/b.dart": "b2",
	})
	cfg := &config.Config{ProdRoots: []string{"lib"}, ProdExtensions: []string{".dart"}}
	arts := map[string]*Article{
		"arch-a": {ID: "arch-a", SeeAlso: []string{"arch-b"}},
		"arch-b": {ID: "arch-b"},
	}
	c := New(cfg, arts, []string{"lib/a.dart", "lib/b.dart"})
	ownership := map[string][]string{"lib/a.dart": {"arch-a"}, "lib/b.dart": {"arch-b"}}

	res, err := c.Evaluate(ownership, 50, 15)
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits == 0 {
		t.Fatal("nothing scored; the fixture no longer exercises this")
	}
	if res.Recall >= res.Recall1 {
		t.Errorf("one hop bought nothing (%.2f -> %.2f). The edge joins the "+
			"two articles that own these co-changing files, so following it "+
			"must find what the partition alone cannot", res.Recall, res.Recall1)
	}
	if res.Recall1 < 0.99 {
		t.Errorf("Recall1 = %.2f, want 1.00: one hop reaches every file in "+
			"the change", res.Recall1)
	}
	if res.Reach1 <= res.Reach {
		t.Errorf("the hop cost nothing to read (%.0f -> %.0f); a hop that is "+
			"free is a hop that was not taken", res.Reach, res.Reach1)
	}
}

// TestOwnersAnswersWhatWillIBreak pins the lookup that closes the gap between
// "I am editing this file" and "what do I need to know".
//
// The reverse index always held this and nothing consumed it. Two agents given
// a real bug in a repository whose AGENTS.md tells them to look a file up
// before changing it went straight to grep, neither opened the KB, and both
// produced fixes that violated an invariant the owning article states in its
// first two lines. A knowledge base that is accurate, cheap to query and never
// queried is worth nothing, so the lookup has to be one command.
func TestOwnersAnswersWhatWillIBreak(t *testing.T) {
	dir := t.TempDir()
	body := "---\nid: arch-a\ntype: arch\ncovers: [lib/a.dart]\n---\n\n# A\n\n" +
		"## INVARIANT\n\n**Measuring and deriving are separate programs.**\n" +
		"Putting a network call in the fast one costs an hour.\n\n" +
		"## GOTCHA\n\n**A grid point is not where you asked.**\nIt snaps.\n\n" +
		"## DECISION\n\n**Chose X over Y.**\nBecause Z.\n"
	if err := os.WriteFile(filepath.Join(dir, "arch-a.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{KBDir: dir, ProdRoots: []string{"lib"},
		ProdExtensions: []string{".dart"}, PathExtensions: []string{".dart"},
		ArticleTypes: map[string]string{"arch": "arch"}}
	var errs []string
	a, err := ParseArticle(cfg, filepath.Join(dir, "arch-a.md"), "arch-a", &errs)
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, map[string]*Article{"arch-a": a}, []string{"lib/a.dart", "lib/loose.dart"})
	own := map[string][]string{"lib/a.dart": {"arch-a"}}

	got := c.Owners(own, []string{"lib/a.dart"})
	for _, want := range []string{"arch-a", "Measuring and deriving", "A grid point is not"} {
		if !strings.Contains(got, want) {
			t.Errorf("owners output is missing %q, which is what stops the\n"+
				"next edit breaking something:\n%s", want, got)
		}
	}
	// DECISION explains why the code is shaped as it is. It is worth reading
	// and it is not what stops a mistake in the next thirty seconds, and an
	// answer long enough to skim is an answer that gets skimmed.
	if strings.Contains(got, "Chose X over Y") {
		t.Errorf("DECISION was included; the edit-time question is what breaks:\n%s", got)
	}

	// An unowned production file must say so rather than print nothing, or the
	// silence reads as "nothing to know here".
	loose := c.Owners(own, []string{"lib/loose.dart"})
	if !strings.Contains(loose, "no article owns this") {
		t.Errorf("an unowned file printed nothing useful: %q", loose)
	}
	if !strings.Contains(loose, "orphan") {
		t.Errorf("an unowned PRODUCTION file should say it is ratchet-relevant: %q", loose)
	}
}

// TestMergeManifestRecordsOnlyWhatWasReviewed pins that a partial review is
// sayable.
//
// `kb compiled` recorded every KB-owned file at once, because per-file
// recording lets you assert coverage you do not have until the gate is green
// and means nothing. That reasoning produced a failure it did not anticipate:
// an agent who had genuinely reviewed three of the five articles `kb fresh`
// named, and genuinely not reviewed the other two, had one truthful option —
// record nothing, leave the gate red. Which is exactly the output a careless
// agent produces. Honesty cost everything and bought no information.
//
// The granularity of the RECORD has to match the granularity of the CHECK.
func TestMergeManifestRecordsOnlyWhatWasReviewed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".compiled-sources")
	if err := WriteManifest(path, map[string]string{
		"lib/a.dart": "old-a", "lib/b.dart": "old-b",
	}); err != nil {
		t.Fatal(err)
	}
	current := map[string]string{"lib/a.dart": "new-a", "lib/b.dart": "new-b"}

	n, err := MergeManifest(path, current, []string{"lib/a.dart"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reported %d files recorded, want 1", n)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["lib/a.dart"], []string{"new-a"}) {
		t.Error("the reviewed file was not recorded, so an honest partial " +
			"review still leaves the gate red")
	}
	if !slices.Equal(got["lib/b.dart"], []string{"old-b"}) {
		t.Errorf("the UNreviewed file was recorded as reviewed (%q); a partial "+
			"record must never assert a judgement nobody made", got["lib/b.dart"])
	}

	// A file the KB no longer owns cannot be vouched for.
	if _, err := MergeManifest(path, current, []string{"lib/gone.dart"}); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadManifest(path)
	if _, ok := got["lib/gone.dart"]; ok {
		t.Error("recorded a hash for a file that is not owned or not present")
	}
}

// TestExcludedDirsStayCitableButUnowned pins the difference between "this file
// needs no article" and "this file does not exist".
//
// `exclude_dirs` used to be applied to the tracked-file set at load, and that
// one set answers two unrelated questions: what must be documented, and is a
// path an article cites real. Conflating them meant an article citing a test
// file from a VERIFY section was told its own correct reference was dangling.
// On a repo excluding its test directories that produced 154 false errors and
// buried the 8 genuine ones.
//
// So the exclusion belongs where ownership is decided, not where the repo is
// read. Three properties together, because any one alone can be satisfied by
// a broken implementation:
func TestExcludedDirsStayCitableButUnowned(t *testing.T) {
	cfg := &config.Config{
		KBDir:          "docs/kb",
		ProdRoots:      []string{"lib"},
		ProdExtensions: []string{".dart"},
		PathExtensions: []string{".dart", ".md"},
		ExcludeDirs:    []string{"tests"},
		ArticleTypes:   map[string]string{"arch": "arch"},
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	tracked := []string{
		"lib/sync.dart",
		"lib/tests/sync_check.dart",
		"docs/kb/arch-sync.md",
	}
	a := &Article{
		ID: "arch-sync", Path: "docs/kb/arch-sync.md",
		FilesBlock: map[string]int{"lib/sync.dart": 1},
		Covers:     []string{"lib/**"},
	}
	c := New(cfg, map[string]*Article{"arch-sync": a}, tracked)

	// 1. The excluded file is still a real repo path, so citing it is legal.
	if !c.PathIsKnown("lib/tests/sync_check.dart") {
		t.Error("an excluded file was reported as not tracked by git; an " +
			"article citing it from VERIFY would fail on a correct reference")
	}
	// 2. A `covers:` glob does not reach into it. Otherwise excluding a
	//    vendored tree would still drag every file in it into the index.
	owners := c.BuildOwnership()
	if ids := owners["lib/tests/sync_check.dart"]; len(ids) != 0 {
		t.Errorf("covers: glob took ownership of an excluded file (owners %v)", ids)
	}
	// 3. The glob still owns what it should.
	if ids := owners["lib/sync.dart"]; len(ids) == 0 {
		t.Error("covers: glob stopped owning ordinary source")
	}
	// 4. And it is still outside the orphan ratchet's scope, so nobody is
	//    ever asked to write an article for it.
	if cfg.IsProductionSource("lib/tests/sync_check.dart") {
		t.Error("an excluded file re-entered the orphan ratchet")
	}
}

// TestSeeAlsoSectionLinksAreGraphEdges pins that the section contributes
// TARGETS, not just a boolean.
//
// The parser recorded HasSeeAlsoSectionLink and threw the targets away, so a
// `## SEE ALSO` section satisfied "does this article have an outbound edge?"
// while contributing nothing to "where does this edge go?". Everything that
// WALKS the graph - eval's one-hop expansion above all - therefore saw an
// empty graph on any KB that declares edges in the section, which is the form
// SPEC.md's own example uses. A real 36-article KB had 35 such sections, zero
// frontmatter edges, and eval reported one-hop recall identical to zero-hop:
// a confident "your edges do nothing" about edges it could not see.
func TestSeeAlsoSectionLinksAreGraphEdges(t *testing.T) {
	cfg := &config.Config{ArticleTypes: map[string]string{"arch": "arch"}}
	dir := t.TempDir()
	parse := func(body string) *Article {
		p := filepath.Join(dir, "a.md")
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

	// Section only: the form that was invisible to the graph.
	a := parse("---\nid: arch-x\ntype: arch\n---\n# X\n\n## SEE ALSO\n\n" +
		"- [`arch-api.md`](arch-api.md) — owns the wire shape.\n")
	if got := a.OutboundEdges(); len(got) != 1 || got[0] != "arch-api" {
		t.Errorf("OutboundEdges() = %v, want [arch-api]; a section-declared "+
			"edge is invisible to every caller that walks the graph", got)
	}

	// Both forms, overlapping. The union, each target once.
	b := parse("---\nid: arch-x\ntype: arch\nsee_also:\n  - arch-api\n  - arch-db\n" +
		"---\n# X\n\n## SEE ALSO\n\n" +
		"- [`arch-api.md`](arch-api.md) — same target, declared twice.\n" +
		"- [`arch-ui.md`](arch-ui.md) — section only.\n")
	got := b.OutboundEdges()
	want := map[string]bool{"arch-api": true, "arch-db": true, "arch-ui": true}
	if len(got) != len(want) {
		t.Errorf("OutboundEdges() = %v, want the 3-element union with no "+
			"duplicate for the target declared in both forms", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("OutboundEdges() returned unexpected target %q", g)
		}
	}

	// A self-link is still not an edge, in either form.
	c := parse("---\nid: arch-x\ntype: arch\n---\n# X\n\n## SEE ALSO\n\n" +
		"- [`arch-x.md`](arch-x.md) — see above.\n")
	if got := c.OutboundEdges(); len(got) != 0 {
		t.Errorf("OutboundEdges() = %v, want none; a self-edge routes nobody", got)
	}
}

// TestSectionVocabularyIsActuallyChecked pins that SPEC.md's "fixed" is a
// property of the KB rather than a sentence in a document.
//
// Nothing verified the section list, and the cost is not tidiness: DIGEST.md
// and `kb owners` render only INVARIANT and GOTCHA, so a claim under any other
// heading cannot reach an agent choosing what to read. A 36-article KB grown
// over five months had 73 `## PATTERN` sections against 14 INVARIANT and 18
// GOTCHA - 87% of its claims invisible to retrieval, every gate reporting
// clean.
func TestSectionVocabularyIsActuallyChecked(t *testing.T) {
	cfg := &config.Config{
		KBDir:        "docs/kb",
		ArticleTypes: map[string]string{"arch": "arch"},
	}
	mk := func(id string, secs ...string) *Article {
		return &Article{ID: id, Path: "docs/kb/" + id + ".md", Sections: secs}
	}
	arts := map[string]*Article{
		// Every spec section, including the two the parser consumes.
		"arch-ok": mk("arch-ok", "FILES", "INVARIANT", "GOTCHA", "DECISION",
			"VERIFY", "STEPS", "SEE ALSO", "COMMANDS"),
		// A label after a colon is the same heading wearing a name. Without
		// the split, 73 `## PATTERN: foo` sections are 73 distinct
		// "violations" of one word.
		"arch-labelled": mk("arch-labelled", "PATTERN: hash_router",
			"PATTERN: html_shell", "INVARIANT"),
		"arch-free": mk("arch-free", "Overview", "Event Catalog", "PATTERN"),
	}
	c := New(cfg, arts, nil)
	c.CheckSectionVocabulary()

	if len(c.Errors) != 0 {
		t.Errorf("section vocabulary produced errors %v; it must warn, so a "+
			"KB predating the rule is not bricked by it", c.Errors)
	}
	joined := strings.Join(c.Warnings, "\n")
	if strings.Contains(joined, "arch-ok") {
		t.Error("an article using only the fixed vocabulary was warned about")
	}
	if !strings.Contains(joined, "arch-free") {
		t.Error("free-form headings did not warn; the spec's guarantee is " +
			"unenforced and an author gets no signal their claims are " +
			"invisible to DIGEST.md and `kb owners`")
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "arch-labelled") {
			if !strings.Contains(w, "1 section(s)") {
				t.Errorf("labelled headings counted separately: %q — "+
					"`## PATTERN: a` and `## PATTERN: b` are one mistake", w)
			}
			if strings.Contains(w, "hash_router") {
				t.Errorf("warning named the label rather than the heading: %q", w)
			}
		}
	}
}

// TestArticleOwnsReadsTheClaimNotTheResult pins the one property that makes
// Owns worth having separately from the ownership map: it answers for a path
// git has never seen. The ownership map is built by applying `covers:` to the
// tracked file list, so it is structurally incapable of that, and the scoped
// untracked warning has nothing else to ask.
func TestArticleOwnsReadsTheClaimNotTheResult(t *testing.T) {
	a := &Article{
		ID:         "arch-api",
		Covers:     []string{"app/api/**", "web/*.js"},
		FilesBlock: map[string]int{"tools/one_off.py": 12},
	}
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"app/api/brand_new.py", true, "under a `/**` glob, and never staged"},
		{"web/client.js", true, "a single-segment `*` glob"},
		{"web/nested/deep.js", false, "`*` must not cross a directory boundary"},
		{"tools/one_off.py", true, "named explicitly in ## FILES"},
		{"app/core/engine.py", false, "owned by a different article"},
	} {
		if got := a.Owns(tc.path); got != tc.want {
			t.Errorf("Owns(%q) = %v, want %v — %s", tc.path, got, tc.want, tc.why)
		}
	}
}

// An article claiming nothing must not quietly claim everything. Under the
// scoped warning that would re-report every peer session's work in progress,
// which is the noise the scoping exists to remove.
func TestArticleOwnsNothingWhenItClaimsNothing(t *testing.T) {
	a := &Article{ID: "recipe-add-endpoint"}
	if a.Owns("app/api/handler.py") {
		t.Error("an article with no `covers:` and no ## FILES claimed a path")
	}
}
