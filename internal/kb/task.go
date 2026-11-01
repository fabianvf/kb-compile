package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fabianvf/kb-compile/internal/repo"
)

// A task is an agent's scratch notes for one piece of work, kept so it can drop
// its raw trajectory without losing where it is.
//
// Nothing here may reach the KB by itself. A log is unverified, and the whole
// value of `kb fresh` is that a green result means somebody reviewed the
// articles, so this file never writes an article, never writes the manifest and
// is never read by a gate. The lessons get out through kb-harvest and kb-compile
// or not at all. It reads exactly one KB artifact, the reverse index, which is
// derived.
const (
	// TaskRoot is relative to the repository root, like the config beside it.
	TaskRoot = ".kb/tasks"

	// ContextBudget is the most `kb task context` prints by default: 1.5k tokens
	// at the same four bytes per token EstimateTokens uses. This is what an agent
	// reads straight after compaction, so an answer that grows with the log would
	// spend the room compaction just made.
	ContextBudget = 6000

	taskCurrent = "CURRENT"
	taskFile    = "TASK.md"
	taskLog     = "log.md"
	taskHarvest = "HARVEST.md"

	// A heading cannot separate milestones, because the bodies are markdown an
	// agent wrote and will contain headings of their own.
	milestoneMarker = "<!-- kb-task-milestone -->"
)

var taskName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Task is what TASK.md records.
type Task struct {
	ID        string
	Goal      string
	Started   string
	StartHEAD string
	Status    string
	Finished  string
}

func taskDir(id string) string { return filepath.Join(TaskRoot, id) }

func (t *Task) write() error {
	var b strings.Builder
	fmt.Fprintf(&b, "started: %s\nhead: %s\nstatus: %s\nfinished: %s\n\n## Goal\n\n%s\n",
		t.Started, t.StartHEAD, t.Status, t.Finished, t.Goal)
	return os.WriteFile(filepath.Join(taskDir(t.ID), taskFile), []byte(b.String()), 0o644)
}

// loadTask refuses a name that is not a single path segment. `kb task drop` ends
// in RemoveAll, and `drop ../..` is a mistake that must fail here rather than
// be survivable by luck.
func loadTask(id string) (*Task, error) {
	if !taskName.MatchString(id) {
		return nil, fmt.Errorf("%q is not a task id. `kb task list` shows them", id)
	}
	data, err := os.ReadFile(filepath.Join(taskDir(id), taskFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no task %q in %s. `kb task list` shows them", id, TaskRoot)
	}
	if err != nil {
		return nil, err
	}
	head, goal, _ := strings.Cut(string(data), "## Goal\n")
	t := &Task{ID: id, Goal: strings.TrimSpace(goal)}
	for _, l := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(l, ": ")
		switch k {
		case "started":
			t.Started = v
		case "head":
			t.StartHEAD = v
		case "status":
			t.Status = v
		case "finished":
			t.Finished = v
		}
	}
	return t, nil
}

// currentTaskID returns "" when there is no current task. A CURRENT naming a
// directory somebody deleted by hand counts as none, because the alternative is
// a pointer that can only be repaired by knowing it exists.
func currentTaskID() string {
	data, err := os.ReadFile(filepath.Join(TaskRoot, taskCurrent))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if _, err := loadTask(id); err != nil {
		return ""
	}
	return id
}

func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// gitHEAD is "" in a repository with no commits yet, which is a legitimate place
// to start work.
func gitHEAD() string {
	out, err := gitOut("rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// TaskStart creates a task and makes it current.
//
// It refuses while another task is current. One checkout has one CURRENT, so a
// second session starting silently would send the first session's milestones
// into the wrong log, and neither would find out. A worktree has its own.
func TaskStart(slug, goal string) (string, error) {
	if !taskName.MatchString(slug) {
		return "", fmt.Errorf("%q cannot be a task slug: use letters, digits, "+
			"dots, dashes and underscores", slug)
	}
	if cur := currentTaskID(); cur != "" {
		return "", fmt.Errorf("task %s is still current in this checkout, and "+
			"there is one CURRENT per checkout. `kb task finish` or `kb task "+
			"drop %s` first, or use a worktree for parallel work", cur, cur)
	}
	if err := os.MkdirAll(TaskRoot, 0o755); err != nil {
		return "", err
	}
	// Written by the tool because the tool is what puts the notes here. An
	// adopting repo has no ignore entry for this directory, so without it `git
	// add -A` commits raw tool output, and an article could then cite it.
	if err := os.WriteFile(filepath.Join(TaskRoot, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return "", err
	}

	now := time.Now().UTC()
	base := now.Format("20060102-150405") + "-" + slug
	id := base
	for n := 2; ; n++ {
		err := os.Mkdir(taskDir(id), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	t := &Task{
		ID: id, Goal: strings.TrimSpace(goal), Status: "active",
		Started: now.Format(time.RFC3339), StartHEAD: gitHEAD(),
	}
	if err := t.write(); err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(TaskRoot, taskCurrent), []byte(id+"\n"), 0o644)
}

type milestone struct {
	When, Head, Message, Stat, Body string
}

func readMilestones(id string) ([]milestone, error) {
	data, err := os.ReadFile(filepath.Join(taskDir(id), taskLog))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ms []milestone
	for _, block := range strings.Split(string(data), milestoneMarker+"\n")[1:] {
		head, rest, _ := strings.Cut(block, "\n")
		f := strings.SplitN(strings.TrimPrefix(head, "## "), " ", 3)
		m := milestone{}
		if len(f) == 3 {
			m.When, m.Head, m.Message = f[0], f[1], f[2]
		}
		_, afterOpen, _ := strings.Cut(rest, "```\n")
		m.Stat, m.Body, _ = strings.Cut(afterOpen, "\n```\n")
		m.Body = strings.TrimSpace(m.Body)
		ms = append(ms, m)
	}
	return ms, nil
}

// TaskCommit appends a milestone to the current task's log and returns its id.
func TaskCommit(message, body string) (string, error) {
	id := currentTaskID()
	if id == "" {
		return "", errors.New("no current task. `kb task start <slug>` begins one")
	}
	t, err := loadTask(id)
	if err != nil {
		return "", err
	}
	ms, err := readMilestones(id)
	if err != nil {
		return "", err
	}
	since := t.StartHEAD
	if len(ms) > 0 {
		since = ms[len(ms)-1].Head
	}
	head := gitHEAD()
	if head == "" {
		head = "none"
	}
	message = neutralise(strings.Join(strings.Fields(message), " "))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n## %s %s %s\n\n```\n%s\n```\n", milestoneMarker,
		time.Now().UTC().Format(time.RFC3339), head, message, milestoneStat(since))
	if body = strings.TrimSpace(body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", neutralise(body))
	}
	b.WriteString("\n")

	f, err := os.OpenFile(filepath.Join(taskDir(id), taskLog),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return id, err
}

// neutralise stops a body quoting the marker from splitting its own milestone
// in two.
func neutralise(s string) string {
	return strings.ReplaceAll(s, milestoneMarker, "<!-- kb-task-milestone (quoted) -->")
}

// milestoneStat diffs the working tree rather than HEAD..HEAD, because an agent
// often reaches a milestone before committing and that diff would be empty. So
// uncommitted work appears in each milestone until it is committed.
//
// Never fatal: losing a stat to a rebase that discarded the previous HEAD is no
// reason to lose the note being written.
func milestoneStat(since string) string {
	var lines []string
	if since == "" || since == "none" {
		since = gitHEAD()
	}
	if since != "" {
		out, err := gitOut("diff", "--stat", since)
		switch {
		case err != nil:
			lines = append(lines, "diff unavailable: "+since+" no longer resolves")
		case strings.TrimSpace(out) == "":
			lines = append(lines, "no tracked changes since "+since)
		default:
			lines = append(lines, strings.TrimRight(out, "\n"))
		}
	}
	if u, err := repo.UntrackedSource(nil); err == nil && len(u) > 0 {
		shown := u
		suffix := ""
		if len(shown) > 8 {
			shown, suffix = shown[:8], fmt.Sprintf(", and %d more", len(u)-8)
		}
		lines = append(lines, "untracked: "+strings.Join(shown, ", ")+suffix)
	}
	return strings.Join(lines, "\n")
}

// touchedFiles lists what this task changed, plus any reason the list may be
// wrong.
//
// First-parent and no merges, rather than `git diff start..HEAD`, because
// bringing trunk down into a branch would otherwise report trunk's files as the
// task's work and send harvest to articles the task never affected. A per-commit
// stamp made every merge look like drift for the same reason (README, "Per-file
// state"). A rebase cannot be seen through this way, so it is reported instead.
func touchedFiles(t *Task) ([]string, []string) {
	var notes []string
	seen := map[string]bool{}
	add := func(out string) {
		for _, p := range strings.Split(out, "\x00") {
			if p = strings.TrimSpace(p); p != "" {
				seen[p] = true
			}
		}
	}
	head := gitHEAD()
	if head != "" {
		spec := head
		if t.StartHEAD != "" {
			spec = t.StartHEAD + ".." + head
			if _, err := gitOut("merge-base", "--is-ancestor", t.StartHEAD, head); err != nil {
				notes = append(notes, "history was rewritten since this task "+
					"started, so this list may name files the task did not change.")
			}
		}
		if out, err := gitOut("log", "--first-parent", "--no-merges",
			"--name-only", "--format=", "-z", spec); err == nil {
			add(out)
		} else {
			notes = append(notes, "cannot read commits since start: "+err.Error())
		}
		if out, err := gitOut("diff", "--name-only", "-z", "HEAD"); err == nil {
			add(out)
		} else {
			notes = append(notes, "cannot read uncommitted changes: "+err.Error())
		}
	}
	if u, err := repo.UntrackedSource(nil); err == nil {
		add(strings.Join(u, "\x00"))
	} else {
		notes = append(notes, "cannot list untracked files: "+err.Error())
	}
	files := make([]string, 0, len(seen))
	for p := range seen {
		files = append(files, p)
	}
	sort.Strings(files)
	return files, notes
}

// ownersOf reads the reverse index rather than rebuilding ownership: this needs
// no graph pass, and a stale answer costs a resume view one line. Returns the
// article -> touched file count, how many files the index does not know, and
// any reason there is no answer at all.
func ownersOf(indexPath string, files []string) (map[string]int, int, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, 0, err
	}
	var idx struct {
		Owners map[string]struct {
			Articles []string `json:"articles"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, 0, fmt.Errorf("%s: %w", indexPath, err)
	}
	arts := map[string]int{}
	unknown := 0
	for _, f := range files {
		e, ok := idx.Owners[f]
		if !ok || len(e.Articles) == 0 {
			unknown++
			continue
		}
		for _, a := range e.Articles {
			arts[a]++
		}
	}
	return arts, unknown, nil
}

// clip cuts on a rune boundary, and says that it cut.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const mark = " [...]"
	return strings.ToValidUTF8(s[:max-len(mark)], "") + mark
}

// TaskContext renders the resume view of a task: the current one when id is
// empty. An id is what lets harvest read a task `finish` has already taken off
// CURRENT.
func TaskContext(indexPath, id string, full bool) (string, error) {
	if id == "" {
		if id = currentTaskID(); id == "" {
			return "", errors.New("no current task. `kb task list` shows finished " +
				"ones, and `kb task context <id>` reads one")
		}
	}
	t, err := loadTask(id)
	if err != nil {
		return "", err
	}
	ms, err := readMilestones(id)
	if err != nil {
		return "", err
	}
	files, notes := touchedFiles(t)

	var b strings.Builder
	// The label travels with the text because this output lands in an agent's
	// context beside the KB, and it must not read with the KB's authority.
	fmt.Fprintf(&b, "Task %s (%s). Unverified working notes, not KB content.\n", t.ID, t.Status)
	start := t.StartHEAD
	if start == "" {
		start = "no commits"
	}
	fmt.Fprintf(&b, "Started %s at %s.\n", t.Started, start)

	maxFiles, maxOwners, shownFrom := len(files), -1, 0
	if full {
		fmt.Fprintf(&b, "\nGoal: %s\n", orNone(t.Goal))
		data, err := os.ReadFile(filepath.Join(taskDir(id), taskLog))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		fmt.Fprintf(&b, "\n%d milestone(s):\n\n%s", len(ms), data)
	} else {
		maxFiles, maxOwners = 25, 15
		fmt.Fprintf(&b, "\nGoal: %s\n", clip(orNone(t.Goal), 600))
		if len(ms) > 3 {
			shownFrom = len(ms) - 3
		}
		fmt.Fprintf(&b, "\nLast %d of %d milestone(s):\n", len(ms)-shownFrom, len(ms))
		for _, m := range ms[shownFrom:] {
			fmt.Fprintf(&b, "- %s %s %s\n", m.When, shortSHA(m.Head), clip(m.Message, 200))
			for _, l := range strings.Split(m.Stat, "\n") {
				if !strings.Contains(l, " | ") && strings.TrimSpace(l) != "" {
					fmt.Fprintf(&b, "  %s\n", clip(strings.TrimSpace(l), 200))
				}
			}
			if m.Body != "" {
				fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(clip(m.Body, 600), "\n", "\n  "))
			}
		}
	}

	fmt.Fprintf(&b, "\nFiles touched since start (%d):\n", len(files))
	for i, f := range files {
		if i == maxFiles {
			fmt.Fprintf(&b, "  and %d more\n", len(files)-maxFiles)
			break
		}
		fmt.Fprintf(&b, "  %s\n", clip(f, 120))
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "  NOTE: %s\n", n)
	}

	arts, unknown, err := ownersOf(indexPath, files)
	if err != nil {
		fmt.Fprintf(&b, "\nOwning articles unavailable, `kb graph --write` "+
			"regenerates the reverse index: %s\n", clip(err.Error(), 200))
	} else {
		ids := make([]string, 0, len(arts))
		for a := range arts {
			ids = append(ids, a)
		}
		sort.Strings(ids)
		fmt.Fprintf(&b, "\nOwning articles (%d):\n", len(ids))
		for i, a := range ids {
			if i == maxOwners {
				fmt.Fprintf(&b, "  and %d more\n", len(ids)-maxOwners)
				break
			}
			fmt.Fprintf(&b, "  %s (%d file(s))\n", clip(a, 80), arts[a])
		}
		if unknown > 0 {
			fmt.Fprintf(&b, "  %d touched file(s) are not in the reverse index.\n", unknown)
		}
	}
	if full {
		return b.String(), nil
	}

	// The per-part limits should hold this under budget on their own. This is
	// what makes the budget a guarantee, with the way to the rest kept intact.
	tail := fmt.Sprintf("\nWhole log: kb task context --full %s\n", id)
	return clip(b.String(), ContextBudget-len(tail)) + tail, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none recorded)"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// TaskFinish marks the current task complete and takes it off CURRENT. The
// directory stays, because harvest needs it.
func TaskFinish() (string, error) {
	id := currentTaskID()
	if id == "" {
		return "", errors.New("no current task to finish. `kb task list` shows them")
	}
	t, err := loadTask(id)
	if err != nil {
		return "", err
	}
	t.Status = "complete"
	t.Finished = time.Now().UTC().Format(time.RFC3339)
	if err := t.write(); err != nil {
		return "", err
	}
	return id, os.Remove(filepath.Join(TaskRoot, taskCurrent))
}

// TaskList renders one line per task, oldest first.
func TaskList() (string, error) {
	entries, err := os.ReadDir(TaskRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	current := currentTaskID()
	var b strings.Builder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := loadTask(e.Name())
		if err != nil {
			continue
		}
		ms, err := readMilestones(t.ID)
		if err != nil {
			return "", err
		}
		state := t.Status
		if t.ID == current {
			state += ", current"
		}
		if _, err := os.Stat(filepath.Join(taskDir(t.ID), taskHarvest)); err == nil {
			state += ", harvested"
		}
		fmt.Fprintf(&b, "%s  [%s]  %d milestone(s)  %s\n", t.ID, state, len(ms),
			clip(strings.Join(strings.Fields(t.Goal), " "), 80))
	}
	return b.String(), nil
}

// TaskDrop deletes a task, and CURRENT with it if it was the current one.
func TaskDrop(id string) error {
	if _, err := loadTask(id); err != nil {
		return err
	}
	if currentTaskID() == id {
		if err := os.Remove(filepath.Join(TaskRoot, taskCurrent)); err != nil {
			return err
		}
	}
	return os.RemoveAll(taskDir(id))
}
