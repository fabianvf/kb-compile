package kb_test

// Golden harness: run every check against a complete miniature repository and
// assert the generated artifacts come back byte-identical.
//
// The fixture under testdata/fixture is a real repo, not a mock: a KB with
// both `## FILES` shapes, both edge kinds, a deliberate orphan and a
// deliberate dead end, three import dialects, and a selector file whose keys
// are patterns rather than paths. Its committed artifacts are the goldens.
//
// Byte-identity is the bar rather than "no errors", because every one of these
// artifacts is a gate input. A reverse index that is merely PLAUSIBLE sends an
// agent to the wrong article; a baseline that is merely plausible grandfathers
// something it should have failed.
//
// The same harness can be pointed at any real repository:
//
//	KB_COMPARE_REPO=/path/to/repo KB_COMPARE_CONFIG=/abs/path/config.json \
//	  go test ./internal/kb -run Golden -v
//
// That mode is READ-ONLY against the target: nothing is written and --write is
// never invoked there. It is how a port or a config change is validated
// against a KB that a different implementation generated.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fabianvf/kb-compile/internal/config"
	"github.com/fabianvf/kb-compile/internal/kb"
	"github.com/fabianvf/kb-compile/internal/repo"
)

// stageFixture copies the fixture into a temp dir and makes it a real git
// repo, because the checker asks git what the repo contains and must never be
// given a filesystem walk instead.
func stageFixture(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", dst+"/").CombinedOutput(); err != nil {
		t.Fatalf("staging fixture: %v: %s", err, out)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-qm", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dst
}

// enter chdirs into the repo under test. Paths in a KB are repo-root-relative
// by design, and that is the invariant the whole checker rests on.
func enter(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// setup returns a Checker over either the fixture or an external repo.
func setup(t *testing.T) (*kb.Checker, *config.Config) {
	t.Helper()
	repoPath, cfgPath := os.Getenv("KB_COMPARE_REPO"), os.Getenv("KB_COMPARE_CONFIG")
	if repoPath == "" {
		repoPath = stageFixture(t)
		cfgPath = filepath.Join(repoPath, config.DefaultPath)
	}
	if cfgPath == "" {
		t.Fatal("KB_COMPARE_REPO set without KB_COMPARE_CONFIG")
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	enter(t, repoPath)

	cfg, err := config.Load(abs)
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := repo.TrackedFiles(cfg.ExcludeSubstrings)
	if err != nil {
		t.Fatal(err)
	}
	articles, errs := loadArticles(t, cfg)
	if len(errs) > 0 {
		t.Fatalf("article parse errors: %v", errs)
	}
	return kb.New(cfg, articles, tracked), cfg
}

func loadArticles(t *testing.T, cfg *config.Config) (map[string]*kb.Article, []string) {
	t.Helper()
	entries, err := os.ReadDir(cfg.KBDir)
	if err != nil {
		t.Fatal(err)
	}
	var errs []string
	articles := map[string]*kb.Article{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "INDEX.md" {
			continue
		}
		a, err := kb.ParseArticle(cfg, filepath.Join(cfg.KBDir, name),
			strings.TrimSuffix(name, ".md"), &errs)
		if err != nil {
			t.Fatal(err)
		}
		articles[strings.TrimSuffix(name, ".md")] = a
	}
	return articles, errs
}

func runAllChecks(c *kb.Checker) {
	c.CheckFrontmatter()
	c.CheckArticleEdges()
	c.CheckFileEdges()
	c.CheckGlobsAreLive()
	c.CheckSectionAnchors()
	c.CheckIndex()
}

// TestGoldenCleanRun asserts a known-good KB produces no errors and no
// warnings. A checker that fabricates findings is as useless as one that
// misses them, and it is the failure mode a rewrite actually hits.
func TestGoldenCleanRun(t *testing.T) {
	c, _ := setup(t)
	runAllChecks(c)
	for _, e := range c.Errors {
		t.Errorf("unexpected error: %s", e)
	}
	for _, w := range c.Warnings {
		t.Errorf("unexpected warning: %s", w)
	}
}

// TestGoldenReverseIndex is the strongest single assertion here: regenerate
// the index and require it byte-for-byte.
func TestGoldenReverseIndex(t *testing.T) {
	c, cfg := setup(t)
	payload := c.BuildReverseIndex(c.BuildOwnership(), c.BuildTestLinks(), c.ExtraLinks())
	got, err := kb.EncodeIndex(payload)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(cfg.ReverseIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Errorf("reverse index differs from the golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestGoldenLinkKinds pins each link mechanism by name against a case that
// only that mechanism can produce.
//
// Without this, a single aggregate byte-comparison can stay green while one
// mechanism silently stops working and another happens to cover for it — the
// import edge dying and a stem match filling the same slot, say. Each case
// below names which mechanism it proves.
func TestGoldenLinkKinds(t *testing.T) {
	if os.Getenv("KB_COMPARE_REPO") != "" {
		t.Skip("fixture-specific expectations")
	}
	c, _ := setup(t)
	links := c.BuildTestLinks()

	cases := []struct {
		path, test, mechanism string
	}{
		{"app/api/handler.py", "tests/test_handler.py",
			"real import: `from app.api.handler import handle`, resolved with an empty root"},
		{"app/core/engine.py", "tests/test_core_engine.py",
			"underscore split: the test imports the PACKAGE, so no import names engine.py"},
		{"web/client.js", "web/client.spec.js",
			"relative import: `from './client.js'`"},
	}
	for _, tc := range cases {
		if !contains(links[tc.path], tc.test) {
			t.Errorf("%s is not linked to %s\n  mechanism: %s\n  got: %v",
				tc.path, tc.test, tc.mechanism, links[tc.path])
		}
	}

	// helpers.js is imported by client.js, which is production, not a test.
	// A production-to-production import must never become a test link.
	if got := links["web/helpers.js"]; len(got) != 0 {
		t.Errorf("web/helpers.js has test links %v; only its importer is a "+
			"production file, so nothing should link here", got)
	}
}

// TestGoldenExtraLinkSubstring pins pattern-keyed columns.
//
// The source file's keys are PATTERNS ("app/"), not paths. Read as exact paths
// they match nothing, and the column comes back silently EMPTY — which reads
// as "no links needed" rather than as a bug. That failure mode is why this is
// asserted on the broadest key in the fixture rather than a narrow one.
func TestGoldenExtraLinkSubstring(t *testing.T) {
	if os.Getenv("KB_COMPARE_REPO") != "" {
		t.Skip("fixture-specific expectations")
	}
	c, _ := setup(t)
	suites := c.ExtraLinks()["suites"]

	cases := []struct {
		path string
		want []string
	}{
		// "app/" is the broadest key and matches no file's full path.
		{"app/legacy_export.py", []string{"smoke"}},
		// Two keys overlap here and the labels must UNION, not overwrite.
		{"app/core/engine.py", []string{"regression", "smoke"}},
		{"app/api/handler.py", []string{"regression", "smoke"}},
		{"web/client.js", []string{"browser"}},
	}
	for _, tc := range cases {
		if got := suites[tc.path]; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("suites[%s] = %v, want %v", tc.path, got, tc.want)
		}
	}
	// A pattern key must never appear in the index as though it were a path.
	for _, key := range []string{"app/", "web/", "app/core/"} {
		if _, ok := suites[key]; ok {
			t.Errorf("pattern key %q leaked into the index as a path", key)
		}
	}
}

// TestGoldenRatchets asserts both baselines reproduce exactly. These decide
// whether a build goes red, so a drifting baseline is a gate that has silently
// stopped gating.
func TestGoldenRatchets(t *testing.T) {
	c, cfg := setup(t)
	ownership := c.BuildOwnership()

	var orphans []string
	for _, f := range c.Tracked {
		if cfg.IsProductionSource(f) && ownership[f] == nil {
			orphans = append(orphans, f)
		}
	}
	sort.Strings(orphans)
	assertBaseline(t, cfg.OrphanBaselinePath(), orphans, "orphans")

	var deadEnds []string
	for id, a := range c.Articles {
		if !a.HasOutboundEdge() {
			deadEnds = append(deadEnds, id)
		}
	}
	sort.Strings(deadEnds)
	assertBaseline(t, cfg.DeadEndBaselinePath(), deadEnds, "dead ends")

	// Non-vacuity: an empty ownership map would make every file an orphan and
	// an empty article set would make every baseline trivially match.
	if len(ownership) == 0 || len(c.Articles) == 0 {
		t.Fatalf("ownership=%d articles=%d — the harness exercised nothing",
			len(ownership), len(c.Articles))
	}
}

func assertBaseline(t *testing.T, path string, got []string, noun string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, l := range strings.Split(string(data), "\n") {
		if tr := strings.TrimSpace(l); tr != "" && !strings.HasPrefix(l, "#") {
			want = append(want, tr)
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s baseline mismatch (golden %d, got %d)\n  golden: %v\n  got:    %v",
			noun, len(want), len(got), want, got)
	}
}

// TestGoldenFreshness asserts the gate is quiet against a manifest recorded
// from this exact content, and loud the moment a byte moves.
//
// The quiet half matters as much as the loud half: adopting the tool in a repo
// whose manifest another implementation wrote must be a no-op, not a
// repo-wide false alarm that trains everyone to waive it.
func TestGoldenFreshness(t *testing.T) {
	c, cfg := setup(t)
	owned := c.OwnedForFreshness(c.BuildOwnership())

	stale, current, err := kb.Freshness(cfg.ManifestPath(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) == 0 {
		t.Fatal("no files hashed — the freshness set collapsed")
	}
	if len(stale) != 0 {
		t.Errorf("expected nothing stale against a manifest recorded from this content, got %d:", len(stale))
		for _, s := range stale {
			t.Errorf("  %s (%s) owned by %v", s.Path, s.Reason, s.Articles)
		}
	}

	if os.Getenv("KB_COMPARE_REPO") != "" {
		return // must not modify an external repo
	}

	// Move one byte in a KB-owned file. Exactly that file must go stale, and
	// it must be reported under the article that owns it — the grouping is
	// what makes the re-read scoped rather than "go read the KB".
	const target = "app/core/util.py"
	orig, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(orig, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, _, err = kb.Freshness(cfg.ManifestPath(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].Path != target {
		t.Fatalf("after touching %s, stale = %v; want exactly that one file", target, stale)
	}
	if stale[0].Reason != "changed" {
		t.Errorf("reason = %q, want \"changed\"", stale[0].Reason)
	}
	if !contains(stale[0].Articles, "arch-core") {
		t.Errorf("stale file reported under %v; arch-core owns it and is the "+
			"article a reader must re-read", stale[0].Articles)
	}
	if grouped := kb.GroupByArticle(stale); !strings.Contains(grouped, "arch-core.md") ||
		!strings.Contains(grouped, target) {
		t.Errorf("grouped output does not name the article and the file:\n%s", grouped)
	}
}

// TestGoldenMissingManifestFails pins that an absent manifest is a FAILURE.
//
// A missing manifest is indistinguishable from a deleted one, so treating it
// as "nothing to check" would make `rm` a one-line bypass of the whole gate —
// the same shape as a test suite that passes because it found no tests.
func TestGoldenMissingManifestFails(t *testing.T) {
	if os.Getenv("KB_COMPARE_REPO") != "" {
		t.Skip("must not modify an external repo")
	}
	c, cfg := setup(t)
	owned := c.OwnedForFreshness(c.BuildOwnership())
	if err := os.Remove(cfg.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	_, _, err := kb.Freshness(cfg.ManifestPath(), owned)
	if err == nil {
		t.Fatal("a missing manifest was accepted; deleting it now bypasses the gate")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %v, want it to say the manifest is missing", err)
	}
}

// TestGoldenDetectsBreakage is the other direction: each check must actually
// fire. A gate nobody has ever seen fail is a gate nobody knows works.
func TestGoldenDetectsBreakage(t *testing.T) {
	if os.Getenv("KB_COMPARE_REPO") != "" {
		t.Skip("must not modify an external repo")
	}
	cases := []struct {
		name   string
		break_ func(t *testing.T, cfg *config.Config)
		expect string
	}{
		{
			name: "a covers glob whose subsystem was deleted",
			break_: func(t *testing.T, cfg *config.Config) {
				appendLine(t, filepath.Join(cfg.KBDir, "arch-core.md"), "")
				replaceIn(t, filepath.Join(cfg.KBDir, "arch-core.md"),
					"  - app/core/**", "  - app/gone/**")
			},
			expect: "matches no tracked file",
		},
		{
			name: "a FILES block naming a path git does not track",
			break_: func(t *testing.T, cfg *config.Config) {
				replaceIn(t, filepath.Join(cfg.KBDir, "arch-core.md"),
					"app/core/util.py", "app/core/renamed.py")
			},
			expect: "git does not track",
		},
		{
			name: "a cited path that is not repo-root-relative",
			break_: func(t *testing.T, cfg *config.Config) {
				appendLine(t, filepath.Join(cfg.KBDir, "arch-core.md"),
					"\nSee `core/engine.py` for the entry point.")
			},
			expect: "not repo-root-relative",
		},
		{
			name: "a link to an article that does not exist",
			break_: func(t *testing.T, cfg *config.Config) {
				appendLine(t, filepath.Join(cfg.KBDir, "arch-core.md"),
					"\nSee [arch-ghost.md](arch-ghost.md).")
			},
			expect: "not an article",
		},
		{
			name: "frontmatter type disagreeing with the filename prefix",
			break_: func(t *testing.T, cfg *config.Config) {
				replaceIn(t, filepath.Join(cfg.KBDir, "arch-web.md"),
					"type: arch", "type: recipe")
			},
			expect: "does not match",
		},
		{
			name: "an article missing from INDEX.md",
			break_: func(t *testing.T, cfg *config.Config) {
				replaceIn(t, filepath.Join(cfg.KBDir, "INDEX.md"),
					"| [arch-web.md](arch-web.md) | The browser client |", "")
			},
			expect: "does not link",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, cfg := setup(t)
			tc.break_(t, cfg)

			// Re-parse after the edit.
			articles, parseErrs := loadArticles(t, cfg)
			c2 := kb.New(cfg, articles, c.Tracked)
			c2.Errors = append(c2.Errors, parseErrs...)
			runAllChecks(c2)

			joined := strings.Join(c2.Errors, "\n")
			if !strings.Contains(joined, tc.expect) {
				t.Errorf("expected an error containing %q; got:\n%s", tc.expect, joined)
			}
		})
	}
}

// TestGoldenOrphanRatchetFailsOnNew pins the ratchet's whole purpose: a
// production file nobody documents must fail, and the message must say how to
// claim it rather than merely reporting a state.
func TestGoldenOrphanRatchetFailsOnNew(t *testing.T) {
	if os.Getenv("KB_COMPARE_REPO") != "" {
		t.Skip("must not modify an external repo")
	}
	dir := stageFixture(t)
	enter(t, dir)

	if err := os.WriteFile("app/brand_new.py", []byte("def x():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, dir, "app/brand_new.py")

	cfg, err := config.Load(filepath.Join(dir, config.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := repo.TrackedFiles(cfg.ExcludeSubstrings)
	if err != nil {
		t.Fatal(err)
	}
	articles, _ := loadArticles(t, cfg)
	c := kb.New(cfg, articles, tracked)
	c.CheckOrphanRatchet(c.BuildOwnership(), false)

	joined := strings.Join(c.Errors, "\n")
	if !strings.Contains(joined, "app/brand_new.py") {
		t.Fatalf("a new undocumented production file did not fail the ratchet:\n%s", joined)
	}
	if !strings.Contains(joined, "`## FILES`") || !strings.Contains(joined, "`covers:`") {
		t.Errorf("the failure does not say how to claim the file. Not every "+
			"agent reading this has the KB's conventions loaded, so the "+
			"message has to carry the fix:\n%s", joined)
	}
}

func gitAdd(t *testing.T, dir, path string) {
	t.Helper()
	cmd := exec.Command("git", "add", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(line+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, old) {
		t.Fatalf("%s does not contain %q, so the breakage was never applied "+
			"and the test would pass vacuously", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(s, old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
