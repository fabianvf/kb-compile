package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestYAMLAndJSONAgree pins that the format is an implementation detail of one
// schema.
//
// YAML is the default because a config is read and edited by people far more
// often than by programs, and the allowlists are worthless without their
// reasons written beside them. But a repo with an existing JSON config should
// not be forced to convert, so both must produce an identical Config. Routing
// YAML through JSON is what guarantees that: one set of struct tags governs
// both, and they cannot drift.
func TestYAMLAndJSONAgree(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	y := write("c.yaml", `
kb_dir: docs/kb
prod_roots: [src, cmd]
prod_extensions: [.go]
article_types: {arch: arch}
not_repo_paths:
  s3://bucket/key: an object store key, not a repo file
adapters:
  - name: go
    extensions: [.go]
    pattern: '"example\.com/m/([^"]+)"'
    strategy: package
    resolve: ["$1"]
`)
	j := write("c.json", `{
  "kb_dir": "docs/kb",
  "prod_roots": ["src", "cmd"],
  "prod_extensions": [".go"],
  "article_types": {"arch": "arch"},
  "not_repo_paths": {"s3://bucket/key": "an object store key, not a repo file"},
  "adapters": [{"name":"go","extensions":[".go"],"pattern":"\"example\\.com/m/([^\"]+)\"","strategy":"package","resolve":["$1"]}]
}`)

	fromYAML, err := Load(y)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := Load(j)
	if err != nil {
		t.Fatal(err)
	}
	// Compiled regexps are unexported pointers and will never be equal;
	// compare the fields that came from the file.
	if !reflect.DeepEqual(fromYAML.ProdRoots, fromJSON.ProdRoots) ||
		!reflect.DeepEqual(fromYAML.NotRepoPaths, fromJSON.NotRepoPaths) ||
		fromYAML.Adapters[0].Pattern != fromJSON.Adapters[0].Pattern ||
		fromYAML.Adapters[0].Strategy != fromJSON.Adapters[0].Strategy {
		t.Errorf("the two formats produced different configs:\n  yaml: %+v\n  json: %+v",
			fromYAML, fromJSON)
	}
}

// TestUnknownKeysRejectedInYAML pins that the strictness survives the format
// change. A typo'd key is the failure this exists for: `cover:` instead of
// `covers:` silently owns nothing.
func TestUnknownKeysRejectedInYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("prod_roots: [src]\nprod_extension: [.go]\n"), 0o644)
	_, err := Load(p)
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "prod_extension") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
}

// TestDiscoverRefusesAmbiguity pins that two configs is an error, not a
// precedence rule.
//
// Two configs in a repo means one is stale. Silently preferring either is how
// an edit lands in the file nobody is reading, which is the same failure the
// ambiguous-path rule exists to prevent.
func TestDiscoverRefusesAmbiguity(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	os.MkdirAll(".kb", 0o755)

	if _, err := Discover(); !os.IsNotExist(err) {
		t.Errorf("with no config, want ErrNotExist, got %v", err)
	}

	os.WriteFile(".kb/config.yaml", []byte("prod_roots: [src]\n"), 0o644)
	got, err := Discover()
	if err != nil || got != ".kb/config.yaml" {
		t.Fatalf("Discover() = %q, %v; want .kb/config.yaml", got, err)
	}

	os.WriteFile(".kb/config.json", []byte(`{"prod_roots":["src"]}`), 0o644)
	if _, err := Discover(); err == nil {
		t.Error("two configs were accepted; one of them is stale and nothing said so")
	} else if !strings.Contains(err.Error(), "config.json") {
		t.Errorf("the error does not name both files: %v", err)
	}
}
