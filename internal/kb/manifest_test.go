package kb

// The manifest format exists for how git merges it, so these merge it with git
// rather than asserting a shape and trusting that the shape merges.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// mergeManifests writes base, then ours and theirs on two branches, merges them
// with git, and returns the result. ok is false when git conflicted.
func mergeManifests(t *testing.T, attrs string, base, ours, theirs map[string]string) (merged string, ok bool) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil && args[0] != "merge" {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return err
	}
	path := filepath.Join(dir, ".compiled-sources")
	write := func(m map[string]string) {
		if err := WriteManifest(path, m); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	if attrs != "" {
		if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(attrs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(base)
	git("add", "-A")
	git("commit", "-qm", "base")
	git("checkout", "-qb", "theirs")
	write(theirs)
	git("commit", "-qam", "theirs")
	git("checkout", "-q", "main")
	write(ours)
	git("commit", "-qam", "ours")
	err := git("merge", "-q", "--no-edit", "theirs")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(data), err == nil
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(out, kv[i])
		} else {
			out[kv[i]] = kv[i+1]
		}
	}
	return out
}

// TestManifestMergesReviewsOfDifferentFiles pins the claim the format makes:
// two branches that recorded different files merge cleanly, without any
// attribute.
//
// The JSON format conflicted on most of these, because git conflicts on
// touching lines and sorted order puts a directory's files side by side. That
// made two people working on one subsystem conflict on every merge.
func TestManifestMergesReviewsOfDifferentFiles(t *testing.T) {
	base := map[string]string{"b.go": "1", "c.go": "1", "d.go": "1"}
	cases := []struct {
		name         string
		ours, theirs map[string]string
	}{
		{"neighbouring files", with(base, "b.go", "2"), with(base, "c.go", "2")},
		{"append after a changed last entry", with(base, "d.go", "2"), with(base, "e.go", "1")},
		{"prepend before a changed first entry", with(base, "b.go", "2"), with(base, "a.go", "1")},
		{"insert after a changed entry", with(base, "c.go", "2"), with(base, "ca.go", "1")},
		{"delete beside a changed entry", with(base, "c.go", ""), with(base, "d.go", "2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, ok := mergeManifests(t, "", base, tc.ours, tc.theirs)
			if !ok {
				t.Fatalf("git conflicted on reviews of different files:\n%s", merged)
			}
		})
	}
}

// TestUnionMergeKeepsBothReviewsAndStaysSound pins the same-file case under
// `merge=union`: both hashes survive, and the file is fresh only when one of
// them matches its content.
//
// That is what makes union safe. A hash for content that exists nowhere can
// only make a file stale, never pass one.
func TestUnionMergeKeepsBothReviewsAndStaysSound(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "c.go")
	if err := os.WriteFile(src, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _ := hashFile(t, src)
	base := map[string]string{"b.go": "1", "c.go": "1"}
	attrs := ".compiled-sources merge=union\n"

	merged, ok := mergeManifests(t, attrs, base, with(base, "c.go", "ours-hash"), with(base, "c.go", h))
	if !ok {
		t.Fatalf("union still conflicted:\n%s", merged)
	}
	path := filepath.Join(dir, ".compiled-sources")
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("the union result does not parse: %v\n%s", err, merged)
	}
	if !slices.Contains(got["c.go"], h) || !slices.Contains(got["c.go"], "ours-hash") {
		t.Fatalf("union lost a side's review: %v", got["c.go"])
	}
	// The merged file is keyed by name; check the verdict against a real file.
	if err := writeManifest(path, map[string][]string{src: {"ours-hash", h}}); err != nil {
		t.Fatal(err)
	}
	if stale, _, err := Freshness(path, map[string][]string{src: {"arch-x"}}); err != nil || len(stale) != 0 {
		t.Fatalf("a file whose content matches one union-kept hash is stale: %v %v", stale, err)
	}
	if err := os.WriteFile(src, []byte("merged, a third content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale, _, err := Freshness(path, map[string][]string{src: {"arch-x"}}); err != nil || len(stale) != 1 {
		t.Fatalf("content neither side reviewed passed the gate: %v %v", stale, err)
	}
}

// TestLegacyManifestIsReadAndRetired pins the upgrade: reviews recorded in the
// old JSON file still count, and the next write moves them across.
func TestLegacyManifestIsReadAndRetired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".compiled-sources")
	legacy := path + ".json"
	if err := os.WriteFile(legacy, []byte("{\n  \"a.go\": \"aaa\",\n  \"b.go\": \"bbb\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("a repo with only the legacy manifest lost it: %v", err)
	}
	if !slices.Equal(got["a.go"], []string{"aaa"}) {
		t.Fatalf("legacy entries not read: %v", got)
	}
	if _, err := MergeManifest(path, map[string]string{"a.go": "new"}, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the legacy manifest survived a write, so the two can drift")
	}
	got, err = ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["b.go"], []string{"bbb"}) {
		t.Errorf("an unreviewed legacy entry was dropped by the move: %v", got)
	}
}

func hashFile(t *testing.T, p string) (string, error) {
	t.Helper()
	_, current, err := Freshness(filepath.Join(t.TempDir(), "none"), map[string][]string{p: {"x"}})
	return current[p], err
}
