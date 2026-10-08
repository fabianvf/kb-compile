package main

// End-to-end tests for the command surface, run against the real binary.
//
// The library tests in internal/kb cover the rules. These cover what an agent
// actually sees, which is a different thing and the one that was never pinned:
// every defect reported against this tool in the field so far has been about
// the OUTPUT - a flag silently discarded, an OK line that read as bookkeeping,
// a warning that fired for somebody else's files. None of those would have
// been caught by testing the checker.
//
// Exec'ing a built binary rather than calling main() keeps os.Exit and the
// stdout/stderr split honest. A test that reaches past them cannot tell a
// failure from a success.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var kbBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kb-cli")
	if err != nil {
		panic(err)
	}
	kbBin = filepath.Join(dir, "kb")
	build := exec.Command("go", "build", "-o", kbBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("building kb: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	// Explicitly, not deferred: os.Exit does not run deferred functions, so a
	// `defer os.RemoveAll(dir)` here is dead code and every `go test` run
	// abandons a temp directory holding a freshly built binary.
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixtureRepo is a throwaway git repository holding a copy of testdata/fixture, with
// every KB-owned file already recorded as reviewed.
type fixtureRepo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	dir := t.TempDir()
	src, err := filepath.Abs("../../testdata/fixture")
	if err != nil {
		t.Fatal(err)
	}
	// -a so the fixture's own dotfiles (.kb/, the baselines) come along.
	if out, err := exec.Command("cp", "-a", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copying fixture: %v: %s", err, out)
	}
	r := &fixtureRepo{t: t, dir: dir}
	r.git("init", "-q")
	r.git("add", "-A")
	r.git("-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-qm", "fixture")
	if res := r.run("compiled"); res.code != 0 {
		t.Fatalf("baseline compile failed: %s%s", res.stdout, res.stderr)
	}
	return r
}

func (r *fixtureRepo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// write creates or replaces a file. It does NOT stage it, because untracked is
// the state half of these tests are about.
func (r *fixtureRepo) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fixtureRepo) touch(rel string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, append(b, []byte("\n// changed\n")...), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

type result struct {
	stdout, stderr string
	code           int
}

func (res result) all() string { return res.stdout + res.stderr }

func (r *fixtureRepo) run(args ...string) result {
	r.t.Helper()
	cmd := exec.Command(kbBin, args...)
	cmd.Dir = r.dir
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("running kb %v: %v", args, err)
	}
	return result{stdout: out.String(), stderr: errb.String(), code: code}
}

func mustContain(t *testing.T, got, want, why string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("output missing %q (%s)\n--- got ---\n%s", want, why, got)
	}
}

func mustNotContain(t *testing.T, got, unwanted, why string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("output contains %q and must not (%s)\n--- got ---\n%s", unwanted, why, got)
	}
}

// ── kb compiled: scope ──────────────────────────────────────────────────────

// The defect this exists for: `kb compiled arch-nas` silently discarded the
// argument and recorded all 162 owned files, so one command attested to
// seventeen articles nobody had opened. Reported from the field, twice.
func TestCompiledArticleRecordsOnlyThatArticle(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.touch("app/core/engine.py")
	r.touch("web/client.js")

	res := r.run("compiled", "arch-api")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.all())
	}
	mustContain(t, res.all(), "recorded 1 file(s) for arch-api",
		"the record has to name its own scope")
	mustContain(t, res.all(), "2 file(s) still unreviewed",
		"the remaining obligation is the point of scoping")

	fresh := r.run("fresh")
	if fresh.code == 0 {
		t.Error("kb fresh passed after compiling one of three articles")
	}
	mustContain(t, fresh.all(), "arch-core.md", "an uncompiled article must stay stale")
	mustContain(t, fresh.all(), "arch-web.md", "an uncompiled article must stay stale")
	mustNotContain(t, fresh.all(), "arch-api.md", "the compiled article must clear")
}

// Several articles at once, and the manifest must MERGE rather than replace:
// recording arch-core second must not undo arch-api.
func TestCompiledArticlesAccumulate(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.touch("app/core/engine.py")
	r.touch("web/client.js")

	r.run("compiled", "arch-api")
	r.run("compiled", "arch-core")

	fresh := r.run("fresh")
	mustNotContain(t, fresh.all(), "arch-api.md", "the first record must survive the second")
	mustNotContain(t, fresh.all(), "arch-core.md", "the second record must apply")
	mustContain(t, fresh.all(), "arch-web.md", "the untouched article must stay stale")
}

// A `.md` suffix is what `kb fresh` prints, so it is what someone will paste.
func TestCompiledAcceptsTheSpellingFreshPrints(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")

	res := r.run("compiled", "arch-api.md")
	if res.code != 0 {
		t.Fatalf("`kb compiled arch-api.md` should work, exit %d: %s", res.code, res.all())
	}
	if fresh := r.run("fresh"); fresh.code != 0 {
		t.Errorf("nothing was recorded: %s", fresh.all())
	}
}

// ── kb compiled: co-owned files ─────────────────────────────────────────────

// coOwn makes arch-core claim app/api/** as well, so app/api/handler.py has
// two owners. Overlapping `covers:` is legal - BuildOwnership builds a SET of
// ids per path - and `kb graph` passes on it.
func (r *fixtureRepo) coOwn() {
	r.t.Helper()
	p := filepath.Join(r.dir, "docs/kb/arch-core.md")
	b, err := os.ReadFile(p)
	if err != nil {
		r.t.Fatal(err)
	}
	old := "covers:\n  - app/core/**\n"
	body := strings.Replace(string(b), old, old+"  - app/api/**\n", 1)
	if body == string(b) {
		r.t.Fatalf("fixture changed shape; %q not found in arch-core.md", old)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", "-A")
	r.git("-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-qm", "co-own")
	if res := r.run("compiled"); res.code != 0 {
		r.t.Fatalf("re-baselining after co-own failed: %s", res.all())
	}
}

// The manifest is keyed by path alone, so recording a file attests it to every
// article that owns it. `kb fresh` prints the full article list and says "drop
// the ones you did not read" - and dropping a co-owner used to record the
// shared file anyway, clearing the dropped article. The honest act was the
// damaging one, and the gate went green on an article nobody opened.
func TestCompiledDoesNotClearAnUnnamedCoOwner(t *testing.T) {
	r := newRepo(t)
	r.coOwn()
	r.touch("app/api/handler.py")

	if out := r.run("fresh").all(); !strings.Contains(out, "arch-core.md") {
		t.Fatalf("setup: both owners should be listed stale:\n%s", out)
	}

	res := r.run("compiled", "arch-api")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.all())
	}
	mustContain(t, res.all(), "HELD", "holding the file back has to be stated, not silent")
	mustContain(t, res.all(), "arch-core", "name the article that would have been attested")

	if fresh := r.run("fresh"); fresh.code == 0 {
		t.Errorf("naming one of two owners cleared the other:\n%s", fresh.all())
	}
}

// And the way out has to work: name every owner and the file records.
func TestCompiledRecordsACoOwnedFileWhenEveryOwnerIsNamed(t *testing.T) {
	r := newRepo(t)
	r.coOwn()
	r.touch("app/api/handler.py")

	res := r.run("compiled", "arch-api", "arch-core")
	mustContain(t, res.all(), "recorded 1 file(s)", "naming both owners must record")
	mustNotContain(t, res.all(), "HELD", "nothing is held back once every owner is named")

	if fresh := r.run("fresh"); fresh.code != 0 {
		t.Errorf("naming both owners left the gate red:\n%s", fresh.all())
	}
}

// ── kb compiled: argument handling ──────────────────────────────────────────

// `flag` stops parsing at the first positional, so a flag after an article
// name is not applied AND is then reported as a bogus article. For `--root`
// that silently means the command ran against the wrong repository.
func TestCompiledRejectsAFlagAfterAnArticleName(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")

	res := r.run("compiled", "arch-api", "--quiet")
	if res.code == 0 {
		t.Fatal("a flag after an article name was accepted, so it was silently dropped")
	}
	mustContain(t, res.all(), "flags must come before", "name the real problem")
	mustNotContain(t, res.all(), "no article", "a flag is not a misspelled article")
}

// The warning says files "will not be recorded", which is false when the
// command is about to refuse. Validate the names before printing anything.
func TestCompiledValidatesNamesBeforeItWarns(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.write("app/api/mine_wip.py", "x = 1\n")

	res := r.run("compiled", "arch-api", "no-such-article")
	if res.code == 0 {
		t.Fatal("an unknown article name exited 0")
	}
	mustNotContain(t, res.all(), "untracked production file",
		"nothing is about to be recorded, so nothing is about to be narrowed")
}

// A discarded argument is how the original defect did its damage. Refusing is
// most of the fix on its own: it stops the command dead instead of doing
// something broader than asked.
func TestCompiledRejectsUnknownArticle(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")

	res := r.run("compiled", "no-such-article")
	if res.code == 0 {
		t.Fatal("an unknown article name exited 0, which is how it gets discarded silently")
	}
	mustContain(t, res.all(), `no article "no-such-article"`, "name what was rejected")
	mustContain(t, res.all(), "docs/kb", "name where it looked")

	// And it must not have recorded anything on the way out.
	if fresh := r.run("fresh"); fresh.code == 0 {
		t.Error("a refused `kb compiled` still wrote to the manifest")
	}
}

// ── kb compiled: what the OK line claims ────────────────────────────────────

// The bare form used to print a file count, which is the part nobody can get
// wrong. What it actually asserts is that every stale ARTICLE was reviewed.
func TestCompiledBareStatesTheBreadthOfItsClaim(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.touch("app/core/engine.py")

	res := r.run("compiled")
	mustContain(t, res.all(), "2 article(s) were reviewed", "name the breadth being attested")
	mustContain(t, res.all(), "2 changed file(s)", "against how many changed files")
	mustContain(t, res.all(), "`kb compiled ARTICLE...` records just the ones you read",
		"the scoped form has to be discoverable at the moment the broad claim is made")
}

// One article is not a choice between honest and careless, so the nudge would
// be noise. A warning that fires when it cannot apply is one people stop
// reading.
func TestCompiledBareOmitsTheNudgeForASingleArticle(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")

	res := r.run("compiled")
	mustContain(t, res.all(), "1 article(s) were reviewed", "still state the scope")
	mustNotContain(t, res.all(), "records just the ones you read",
		"there is no narrower claim available with one article")
}

// Recording when nothing changed is a no-op and must not read as an
// attestation, or the phrase "attesting that 0 articles were reviewed" ends up
// in a transcript as though it meant something.
func TestCompiledBareOnACleanTreeClaimsNothing(t *testing.T) {
	r := newRepo(t)

	res := r.run("compiled")
	mustContain(t, res.all(), "nothing had changed to review", "say it was a no-op")
	mustNotContain(t, res.all(), "attesting", "a no-op attests to nothing")
}

// ── kb fresh ────────────────────────────────────────────────────────────────

// Bare `kb compiled` is the shortest thing to type in the tool, so the
// careless path was also the cheapest. Printing the scoped line makes the
// honest answer a paste and the false one a deletion.
func TestFreshPrintsTheScopedCommandForExactlyWhatItListed(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.touch("web/client.js")

	res := r.run("fresh")
	if res.code == 0 {
		t.Fatal("kb fresh passed with two stale files")
	}
	mustContain(t, res.all(), "kb compiled arch-api arch-web",
		"the command has to be pasteable and sorted")
	mustNotContain(t, res.all(), "arch-core",
		"an article with nothing stale must not appear in the command")
	mustContain(t, res.all(), "Drop the ones you did not read",
		"deleting names is the intended edit")
}

// The command it prints has to actually work. A pasteable line that errors is
// worse than none, because the next move after it fails is bare `kb compiled`.
func TestTheCommandFreshPrintsIsRunnable(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.touch("web/client.js")

	out := r.run("fresh").all()
	_, after, ok := strings.Cut(out, "kb compiled ")
	if !ok {
		t.Fatalf("no scoped command in output:\n%s", out)
	}
	args := strings.Fields(strings.SplitN(after, "\n", 2)[0])

	if res := r.run(append([]string{"compiled"}, args...)...); res.code != 0 {
		t.Fatalf("the command kb fresh printed does not run: exit %d\n%s", res.code, res.all())
	}
	if fresh := r.run("fresh"); fresh.code != 0 {
		t.Errorf("pasting the printed command left the gate red:\n%s", fresh.all())
	}
}

// ── untracked production source ─────────────────────────────────────────────

// Ownership comes from `git ls-files`, so an unstaged subsystem is invisible
// to every check. The warning is the only thing standing between that and a
// marker that records a narrower set than its author believes.
func TestGraphWarnsAboutUntrackedProductionSource(t *testing.T) {
	r := newRepo(t)
	r.write("app/newsub/deploy.py", "x = 1\n")

	res := r.run("graph")
	mustContain(t, res.all(), "untracked production file", "name the condition")
	mustContain(t, res.all(), "app/newsub/deploy.py", "name the file")
	if res.code != 0 {
		t.Errorf("the warning must not fail the build, got exit %d", res.code)
	}
}

// Warn, never fail, and never change a verdict. A dirty tree is the normal
// state mid-work, and a gate people route around asserts less than one that
// nags.
func TestUntrackedWarningNeverChangesTheVerdict(t *testing.T) {
	r := newRepo(t)
	clean := r.run("graph").code
	r.write("app/newsub/deploy.py", "x = 1\n")
	dirty := r.run("graph").code

	if clean != dirty {
		t.Errorf("graph exit changed from %d to %d because of an untracked file", clean, dirty)
	}
}

// Staging is the fix the warning recommends, so it has to be the fix. If the
// file stayed listed after `git add`, the advice would be wrong.
func TestStagingClearsTheUntrackedWarning(t *testing.T) {
	r := newRepo(t)
	r.write("app/newsub/deploy.py", "x = 1\n")
	r.git("add", "app/newsub/deploy.py")

	mustNotContain(t, r.run("graph").all(), "untracked production file",
		"`git add` is the advertised fix and must work")
}

// A warning that names generated files is one people learn to skip, and this
// one is worth keeping sharp.
func TestUntrackedWarningIgnoresGitignoredOutput(t *testing.T) {
	r := newRepo(t)
	r.write(".gitignore", "generated/\n")
	r.git("add", ".gitignore")
	r.write("app/generated/out.py", "x = 1\n")
	r.write("generated/out.py", "x = 1\n")

	mustNotContain(t, r.run("graph").all(), "generated/out.py",
		"gitignored build output is not somebody's unstaged work")
}

// Non-source is not the orphan ratchet's business and is not this warning's
// either. prod_extensions is the filter.
func TestUntrackedWarningIgnoresNonSource(t *testing.T) {
	r := newRepo(t)
	r.write("app/notes.md", "scratch\n")

	mustNotContain(t, r.run("graph").all(), "app/notes.md",
		"only prod_extensions under prod_roots are in scope")
}

// The case from the field: two sessions in one worktree. Warning about a path
// the named article would not own reports somebody else's work in progress,
// which cannot be acted on and teaches the reader to skip the line.
func TestCompiledScopesTheUntrackedWarningToTheNamedArticles(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.write("app/api/mine_wip.py", "x = 1\n")  // arch-api would own this
	r.write("web/peer_wip.js", "var x = 1;\n") // arch-web would, not arch-api

	res := r.run("compiled", "arch-api")
	mustContain(t, res.all(), "app/api/mine_wip.py", "a path this article claims is mine to see")
	mustNotContain(t, res.all(), "web/peer_wip.js", "another article's unstaged work is not")
}

// A path NO article claims is not somebody else's work in progress - it is the
// birth condition of a new subsystem, and the reason the warning exists. The
// scoped form is the one `kb fresh` tells people to paste, so if it were the
// one form that suppressed this, the most-recommended command would be the
// one that hides the case the feature was built for.
func TestCompiledScopedStillWarnsAboutWhollyUnclaimedSource(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.write("app/newsub/deploy.py", "x = 1\n") // no article covers app/newsub

	mustContain(t, r.run("compiled", "arch-api").all(), "app/newsub/deploy.py",
		"an unowned untracked path belongs to every form of the command")
}

func TestCompiledBareWarnsAboutEveryUntrackedFile(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.write("app/api/mine_wip.py", "x = 1\n")
	r.write("web/peer_wip.js", "var x = 1;\n")

	res := r.run("compiled")
	mustContain(t, res.all(), "app/api/mine_wip.py", "the bare form claims everything")
	mustContain(t, res.all(), "web/peer_wip.js", "so it must report everything")
}

// The double blind: to be an orphan you must be tracked, and the ownership map
// is built from the tracked list too. A file that is both untracked and
// unowned is what every new subsystem looks like before its article exists.
func TestFreshReportsUntrackedFilesNoArticleClaims(t *testing.T) {
	r := newRepo(t)
	r.write("app/newsub/deploy.py", "x = 1\n")

	res := r.run("fresh")
	if res.code != 0 {
		t.Fatalf("nothing is stale, kb fresh should pass: %s", res.all())
	}
	mustContain(t, res.all(), "no article claims", "the OK path is the silent state that matters")
	mustContain(t, res.all(), "app/newsub/deploy.py", "name it")
}

// An untracked path an article already covers belongs to the `kb compiled`
// warning. Saying it twice, in two different vocabularies, is noise.
func TestFreshOmitsUntrackedFilesAnArticleAlreadyClaims(t *testing.T) {
	r := newRepo(t)
	r.write("app/api/mine_wip.py", "x = 1\n")

	mustNotContain(t, r.run("fresh").all(), "no article claims",
		"arch-api's covers: glob claims this path already")
}

// First adoption: no manifest yet. That is simultaneously the likeliest
// moment for unstaged subsystems and the moment the report used to be
// skipped, because the ErrNoManifest branch exited before it ran - after the
// caller had already paid for the working-tree walk that computed the list.
func TestFreshReportsUnclaimedWhenTheManifestIsMissing(t *testing.T) {
	r := newRepo(t)
	if err := os.Remove(filepath.Join(r.dir, "docs/kb/.compiled-sources.json")); err != nil {
		t.Fatal(err)
	}
	r.write("app/newsub/deploy.py", "x = 1\n")

	res := r.run("fresh")
	if res.code == 0 {
		t.Fatal("a missing manifest must still fail")
	}
	mustContain(t, res.all(), "is missing", "the headline is still the manifest")
	mustContain(t, res.all(), "app/newsub/deploy.py",
		"the unclaimed report is most needed at first adoption")
}

func TestFreshReportsUnclaimedAlongsideStaleness(t *testing.T) {
	r := newRepo(t)
	r.touch("app/api/handler.py")
	r.write("app/newsub/deploy.py", "x = 1\n")

	res := r.run("fresh")
	if res.code == 0 {
		t.Fatal("a stale file must still fail")
	}
	mustContain(t, res.all(), "arch-api.md", "staleness is still the headline")
	mustContain(t, res.all(), "no article claims", "and the unclaimed report survives beside it")
}

// A clean repo must say nothing about untracked files at all, or the section
// becomes furniture.
func TestNoUntrackedNoiseOnACleanTree(t *testing.T) {
	r := newRepo(t)
	for _, cmd := range [][]string{{"graph"}, {"fresh"}, {"compiled"}} {
		out := r.run(cmd...).all()
		mustNotContain(t, out, "untracked production file", "kb "+strings.Join(cmd, " "))
		mustNotContain(t, out, "no article claims", "kb "+strings.Join(cmd, " "))
	}
}

// ── cost ────────────────────────────────────────────────────────────────────

// `kb owners` runs from the UserPromptSubmit hook on every message a human
// sends, and it is the command advertised as a cheap lookup. `git ls-files
// --others` stats the whole working tree, so calling it here would be a real
// cost on the hot path for an answer this command never reads.
//
// Asserted by shimming git rather than by timing, because a timing test on a
// small fixture would pass whatever the code did.
func TestOwnersDoesNotWalkTheWorkingTree(t *testing.T) {
	r := newRepo(t)
	r.write("app/newsub/deploy.py", "x = 1\n")

	shim := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	log := filepath.Join(shim, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	calls := func(args ...string) string {
		os.Remove(log)
		cmd := exec.Command(kbBin, args...)
		cmd.Dir = r.dir
		cmd.Env = append(os.Environ(), "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Run()
		b, _ := os.ReadFile(log)
		return string(b)
	}

	if got := calls("owners", "app/core/engine.py"); strings.Contains(got, "--others") {
		t.Errorf("kb owners walked the working tree:\n%s", got)
	}
	// The control. If graph does not call it either, the test above is
	// passing because the shim is broken rather than because the code is right.
	if got := calls("graph"); !strings.Contains(got, "--others") {
		t.Errorf("shim did not observe the call kb graph must make:\n%s", got)
	}
}
