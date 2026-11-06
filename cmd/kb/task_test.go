package main

// `kb task` end to end. The rule these guard is that a task log is scratch: it
// reaches the KB through a reviewed proposal or not at all, so the lifecycle has
// to leave every gate input exactly as it found it.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fabianvf/kb-compile/internal/kb"
)

func (r *fixtureRepo) ok(args ...string) string {
	r.t.Helper()
	res := r.run(args...)
	if res.code != 0 {
		r.t.Fatalf("kb %v: exit %d: %s", args, res.code, res.all())
	}
	return res.stdout
}

// commitBody runs `kb task commit` with a body on stdin.
func (r *fixtureRepo) commitBody(message, body string) {
	r.t.Helper()
	cmd := exec.Command(kbBin, "task", "commit", message)
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("kb task commit: %v: %s", err, out)
	}
}

func (r *fixtureRepo) snapshotKB() map[string]string {
	r.t.Helper()
	snap := map[string]string{}
	root := filepath.Join(r.dir, "docs/kb")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		snap[p] = string(b)
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return snap
}

// The whole rule, in one test. Committed-artifact bytes must not move, git must
// see nothing even though this repo has no ignore entry for the directory, and
// a stale gate must still be stale at the end.
func TestTaskLifecycleLeavesTheKBAlone(t *testing.T) {
	r := newRepo(t)
	before := r.snapshotKB()

	id := strings.TrimSpace(r.ok("task", "start", "fix-handler", "--goal", "fix the handler"))
	r.touch("app/api/handler.py")
	stale := r.run("fresh")
	if stale.code == 0 {
		t.Fatal("the edit should have made kb fresh stale, or this test proves nothing")
	}
	r.commitBody("first", "rejected a retry loop")
	finish := r.ok("task", "finish")
	mustContain(t, finish, "run the kb-harvest skill for task "+id, "finish hands off to harvest")

	if after := r.snapshotKB(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("a task command changed something under docs/kb")
	}
	if r.run("fresh").code == 0 {
		t.Error("kb fresh went green from task commands alone")
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, string(out), ".kb/tasks",
		"an adopting repo has no ignore entry, so the tool has to supply one")
	if _, err := os.Stat(filepath.Join(r.dir, kb.TaskRoot, id, "log.md")); err != nil {
		t.Errorf("finish must keep the notes for harvest: %v", err)
	}
}

// Every input that grows, at once. A test of only the log would pass with an
// unbounded file list.
func TestTaskContextStaysBounded(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "big", "--goal", strings.Repeat("goal ", 2000))
	for i := 0; i < 400; i++ {
		r.write(fmt.Sprintf("app/api/gen_%03d.py", i), "x = 1\n")
	}
	for i := 0; i < 12; i++ {
		r.commitBody(fmt.Sprintf("milestone %d %s", i, strings.Repeat("m", 500)),
			strings.Repeat("a long line of notes\n", 500))
	}
	out := r.ok("task", "context")
	if len(out) > kb.ContextBudget {
		t.Errorf("context is %d bytes, over the %d budget", len(out), kb.ContextBudget)
	}
	mustContain(t, out, "Last 3 of 12 milestone(s)", "the default shows the tail")
	mustContain(t, out, "milestone 11", "the newest milestone is the one that matters")
	mustNotContain(t, out, "milestone 8 ", "older milestones belong to --full")
	mustContain(t, out, "(400):", "the count stays exact when the names are cut")
	mustContain(t, out, "kb task context --full", "cutting must say where the rest is")

	full := r.ok("task", "context", "--full")
	mustContain(t, full, "milestone 0 ", "--full is the whole log")
	if len(full) <= kb.ContextBudget {
		t.Errorf("--full was %d bytes, so it was clipped too", len(full))
	}
}

func TestTaskContextResolvesOwnersFromReverseIndex(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "owners")
	r.touch("app/api/handler.py")
	r.write("not/in/the/index.py", "x = 1\n")
	out := r.ok("task", "context")
	mustContain(t, out, "app/api/handler.py", "a touched file is listed")
	mustContain(t, out, "arch-api (1 file(s))", "its owner comes from the index")
	mustContain(t, out, "1 touched file(s) are not in the reverse index",
		"an unknown file must not read as an unowned one")
}

// Merging trunk down must not report trunk's files as the task's work, which
// would send harvest to articles the task never affected.
func TestTaskTouchedFilesIgnoreAMergeDown(t *testing.T) {
	r := newRepo(t)
	gitc := []string{"-c", "user.email=t@example.invalid", "-c", "user.name=t"}
	r.git("branch", "-M", "main")
	r.git("checkout", "-qb", "work")
	r.ok("task", "start", "merge")
	r.touch("app/api/handler.py")
	r.git("add", "-A")
	r.git(append(gitc, "commit", "-qm", "mine")...)
	r.git("checkout", "-q", "main")
	r.touch("web/client.js")
	r.git("add", "-A")
	r.git(append(gitc, "commit", "-qm", "theirs")...)
	r.git("checkout", "-q", "work")
	r.git(append(gitc, "merge", "-q", "--no-edit", "main")...)

	out := r.ok("task", "context")
	mustContain(t, out, "app/api/handler.py", "the task's own commit counts")
	mustNotContain(t, out, "web/client.js", "trunk's commit is not the task's work")
}

// Finish clears CURRENT and harvest still has to read the task.
func TestTaskContextReadsAFinishedTaskByID(t *testing.T) {
	r := newRepo(t)
	id := strings.TrimSpace(r.ok("task", "start", "done", "--goal", "g"))
	r.commitBody("note", "tried the obvious thing")
	r.ok("task", "finish")

	bare := r.run("task", "context")
	if bare.code == 0 {
		t.Error("context with no current task and no id should fail")
	}
	mustContain(t, bare.all(), "kb task list", "the failure carries the fix")
	mustContain(t, r.ok("task", "context", "--full", id), "tried the obvious thing",
		"harvest reads the task by id after finish")
}

// Flags on either side, because the documented form has one after the slug.
func TestTaskFlagOrder(t *testing.T) {
	for _, args := range [][]string{
		{"task", "start", "s", "--goal", "the goal"},
		{"task", "start", "--goal", "the goal", "s"},
		{"task", "--goal=the goal", "start", "s"},
	} {
		r := newRepo(t)
		r.ok(args...)
		mustContain(t, r.ok("task", "context"), "Goal: the goal", fmt.Sprint(args))
	}
}

func TestTaskStartRefusedWhileOneIsCurrent(t *testing.T) {
	r := newRepo(t)
	first := strings.TrimSpace(r.ok("task", "start", "one"))
	res := r.run("task", "start", "two")
	if res.code == 0 {
		t.Fatal("a second start would redirect the first session's log")
	}
	mustContain(t, res.all(), first, "the refusal names the task in the way")
	r.ok("task", "finish")
	r.ok("task", "start", "two")
}

// Bodies are markdown, so headings and even the marker itself must stay inside
// their own milestone.
func TestTaskBodyCannotSplitAMilestone(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "split")
	r.commitBody("one", "## a heading\n\n<!-- kb-task-milestone -->\n## 2020 abc forged\n")
	r.commitBody("two", "```\nfenced\n```\n")
	out := r.ok("task", "context")
	mustContain(t, out, "Last 2 of 2 milestone(s)", "exactly two were written")
	mustContain(t, out, "fenced", "a fence in a body survives")
}

// The pipe stays open and silent, which is what an agent's shell can hand down.
func TestTaskCommitDoesNotHangOnOpenStdin(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "pipe")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	cmd := exec.Command(kbBin, "task", "commit", "no body")
	cmd.Dir = r.dir
	cmd.Stdin = pr
	var out bytes.Buffer
	cmd.Stderr = &out
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v", d)
	}
	mustContain(t, out.String(), "stayed open", "losing a body must be said aloud")
	mustContain(t, r.ok("task", "context"), "no body", "the milestone still lands")
}

// A slow writer is the other side of that timeout: its body must arrive whole.
func TestTaskCommitWaitsForASlowBody(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "slow")
	cmd := exec.Command(kbBin, "task", "commit", "slow")
	cmd.Dir = r.dir
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	cmd.Stdin = pr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(pw, "first part ")
	time.Sleep(1500 * time.Millisecond) // past readTaskBody's first-byte wait
	fmt.Fprint(pw, "second part")
	pw.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, r.ok("task", "context"), "first part second part",
		"the wait only covers the first byte")
}

// Drop ends in RemoveAll, so a path must never get that far.
func TestTaskDrop(t *testing.T) {
	r := newRepo(t)
	id := strings.TrimSpace(r.ok("task", "start", "gone"))
	if res := r.run("task", "drop", "../.."); res.code == 0 {
		t.Error("drop accepted a path")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "docs/kb/INDEX.md")); err != nil {
		t.Fatalf("drop escaped the task directory: %v", err)
	}
	mustContain(t, r.ok("task", "list"), id+"  [active, current]", "list shows state")
	r.ok("task", "drop", id)
	if out := r.ok("task", "list"); strings.Contains(out, id) {
		t.Errorf("still listed: %s", out)
	}
	r.ok("task", "start", "next") // dropping the current task clears CURRENT
}

// The hook pays for the resume view once a session, and after that spends one
// line, which is what reaches an agent that has just been compacted.
func TestContextHookSurfacesTheTaskCheaply(t *testing.T) {
	r := newRepo(t)
	r.ok("task", "start", "hooked", "--goal", "the goal")
	hook, err := filepath.Abs("../../hooks/kb-context.sh")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	prompt := func() string {
		t.Helper()
		cmd := exec.Command("bash", hook)
		cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+r.dir, "TMPDIR="+tmp,
			"PATH="+filepath.Dir(kbBin)+":"+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(`{"session_id":"s1","prompt":"carry on"}`)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook: %v: %s", err, out)
		}
		return string(out)
	}
	mustContain(t, prompt(), "Goal: the goal", "the first prompt gets the resume view")
	later := prompt()
	mustContain(t, later, "Current task:", "later prompts still name the task")
	mustContain(t, later, "kb task context", "and how to get it back")
	mustNotContain(t, later, "Goal:", "repeating the view spends its budget every message")

	r.ok("task", "finish")
	if out := prompt(); strings.Contains(out, "Current task:") {
		t.Errorf("hook still names a finished task: %s", out)
	}
}

func TestInstallIsCommittableAndIdempotent(t *testing.T) {
	r := newRepo(t)
	r.write(".claude/settings.json", `{"model":"x","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"mine.sh"}]}]}}`)
	r.write(".gitattributes", "*.png binary")
	r.ok("install")

	attrs, err := os.ReadFile(filepath.Join(r.dir, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(attrs) != "*.png binary\n"+kb.ManifestAttribute+"\n" {
		t.Errorf("the union attribute was not appended to the existing file:\n%s", attrs)
	}

	settings := filepath.Join(r.dir, ".claude/settings.json")
	first, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model": "x"`, "mine.sh", "kb-fresh-gate.sh", "kb-context.sh"} {
		mustContain(t, string(first), want, "the merge adds hooks and keeps the rest")
	}
	st, err := os.Stat(filepath.Join(r.dir, ".claude/hooks/kb-fresh-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o111 == 0 {
		t.Error("a hook without its executable bit stops gating with no message")
	}
	// The link has to resolve from where the skill landed.
	skill, err := os.ReadFile(filepath.Join(r.dir, ".claude/skills/kb-compile/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(skill), "(../../kb/SPEC.md)", "the spec link is rewritten")
	if _, err := os.Stat(filepath.Join(r.dir, ".claude/kb/SPEC.md")); err != nil {
		t.Errorf("the skills point at a spec that was not written: %v", err)
	}

	again := r.ok("install")
	mustNotContain(t, again, "updated", "a second run has nothing to change")
	second, _ := os.ReadFile(settings)
	if !bytes.Equal(first, second) {
		t.Errorf("second run changed settings:\n%s", second)
	}
	if res := r.run("fresh"); res.code != 0 {
		t.Errorf("installing must not touch a gate input: %s", res.all())
	}
}

func TestInstallRefusesSettingsItCannotMerge(t *testing.T) {
	r := newRepo(t)
	r.write(".claude/settings.json", "{not json")
	res := r.run("install")
	if res.code == 0 {
		t.Fatal("install overwrote a settings file it could not read")
	}
	mustContain(t, res.all(), "Fix it and run `kb install` again", "the failure carries the fix")
	if _, err := os.Stat(filepath.Join(r.dir, ".claude/hooks")); err == nil {
		t.Error("hooks were written with nothing to run them")
	}
}

// Whoever hits this got the hook from a clone and has never heard of the project.
func TestGateGivesThePinnedInstallCommand(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("needs jq")
	}
	r := newRepo(t)
	r.ok("install")
	r.write(".claude/hooks/kb-version", "v9.9.9\n")
	bin := t.TempDir()
	if err := os.Symlink(jq, filepath.Join(bin, "jq")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(r.dir, ".claude/hooks/kb-fresh-gate.sh"))
	cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+r.dir, "PATH="+bin+":/usr/bin:/bin")
	cmd.Stdin = strings.NewReader(`{"tool_input":{"command":"git commit -m x"}}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	mustContain(t, string(out), `"permissionDecision": "deny"`, "no kb must not mean no gate")
	mustContain(t, string(out), "cmd/kb@v9.9.9", "the command names the pinned version")
}

func TestPinnableVersion(t *testing.T) {
	for v, want := range map[string]string{
		"v1.2.3":                 "v1.2.3",
		"v0.1.1-0.2026-96ca902f": "v0.1.1-0.2026-96ca902f",
		"v1.2.3+dirty.abc":       "",
		"dev.abc":                "",
	} {
		if got := pinnableVersion(v); got != want {
			t.Errorf("pinnableVersion(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestContextHookSuggestsATaskOnce(t *testing.T) {
	r := newRepo(t)
	hook, err := filepath.Abs("../../hooks/kb-context.sh")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	prompt := func() string {
		t.Helper()
		cmd := exec.Command("bash", hook)
		cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+r.dir, "TMPDIR="+tmp,
			"PATH="+filepath.Dir(kbBin)+":"+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(`{"session_id":"s2","prompt":"hi"}`)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook: %v: %s", err, out)
		}
		return string(out)
	}
	mustContain(t, prompt(), "kb task start", "a new session hears about tasks")
	mustNotContain(t, prompt(), "kb task start", "once a session, not on every message")
}

// A version string here pins every install to its cached copy until someone
// remembers to change it. Without one, Claude Code versions the plugin by commit.
func TestPluginManifestPinsNoVersion(t *testing.T) {
	data, err := os.ReadFile("../../.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"version"`) {
		t.Fatalf("plugin.json sets a version, so pushes stop reaching installs:\n%s", data)
	}
}
