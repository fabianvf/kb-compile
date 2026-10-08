package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo builds a throwaway repository with one committed file, one
// gitignored build artifact, and one written-but-unstaged source file.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "build/\n")
	write("app/committed.py", "x = 1\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-qm", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// The case the warning exists for: a new subsystem written but not added.
	write("app/new_subsystem.py", "y = 2\n")
	// And the case it must stay quiet about.
	write("build/generated.py", "z = 3\n")
	return dir
}

func inDir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
}

// The whole point of the warning is the file somebody wrote and did not stage.
// A gitignored build artifact reported alongside it would train people to
// ignore the line, which costs more than the line is worth.
func TestUntrackedSourceSeesNewFilesAndNotBuildOutput(t *testing.T) {
	inDir(t, gitRepo(t))

	got, err := UntrackedSource(func(p string) bool { return filepath.Ext(p) == ".py" })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "app/new_subsystem.py" {
		t.Errorf("UntrackedSource = %v, want [app/new_subsystem.py]", got)
	}
}

// A tracked file must never appear here. If it did, `git add` would not clear
// the warning and the advice attached to it would be wrong.
func TestUntrackedSourceExcludesTracked(t *testing.T) {
	dir := gitRepo(t)
	inDir(t, dir)

	cmd := exec.Command("git", "add", "app/new_subsystem.py")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	got, err := UntrackedSource(func(p string) bool { return filepath.Ext(p) == ".py" })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("UntrackedSource = %v after staging, want empty", got)
	}
}

// A nil predicate accepts everything except what .gitignore already removed,
// so callers that want no filtering do not have to pass a tautology.
func TestUntrackedSourceNilPredicate(t *testing.T) {
	inDir(t, gitRepo(t))

	got, err := UntrackedSource(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "app/new_subsystem.py" {
		t.Errorf("UntrackedSource(nil) = %v, want [app/new_subsystem.py]", got)
	}
}
