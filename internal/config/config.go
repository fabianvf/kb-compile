// Package config holds the per-repo description of what the KB governs.
//
// Everything in here was a `const` in the Dart original. That is the whole
// point of the extraction: the checker's JUDGEMENTS are portable, its
// INVENTORY is not. A repo declares its roots, its languages and its
// allowlists; the checker supplies the rules.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// DefaultPath is the config this tool writes and documents. YAML, because a
// config file is read and edited by people far more often than by programs,
// and comments are the difference between a setting someone can change and a
// setting nobody dares touch. The allowlists in particular are worthless
// without their reasons written beside them.
const DefaultPath = ".kb/config.yaml"

// SearchPaths are tried in order when no --config is given. JSON still works:
// the format is an implementation detail of the same schema, and a repo that
// already has a JSON config should not be forced to convert.
var SearchPaths = []string{
	".kb/config.yaml",
	".kb/config.yml",
	".kb/config.json",
}

// Config is the full per-repo description. Every field has a default that is
// wrong for most repos and harmless for all of them, so a minimal config is
// short and an explicit one is complete.
type Config struct {
	// KBDir is the article directory, repo-root-relative. e.g. "docs/kb".
	KBDir string `json:"kb_dir"`

	// ProdRoots are the directories scanned for production source by the
	// orphan ratchet. Tests are deliberately excluded from the ratchet's
	// scope: the KB documents subsystems, and test files answer to their own
	// discipline, not to per-file KB ownership.
	ProdRoots []string `json:"prod_roots"`

	// ProdExtensions limits the ratchet to real source. A repo that documents
	// its YAML should add ".yaml" here.
	ProdExtensions []string `json:"prod_extensions"`

	// PathExtensions are the extensions the prose scanner will treat a
	// backticked token as a repo PATH for. Wider than ProdExtensions on
	// purpose: an article may correctly cite a .json or .html it does not own.
	PathExtensions []string `json:"path_extensions"`

	// ArticleTypes maps a filename prefix to the `type:` its frontmatter must
	// declare. "arch-scoring.md" with prefix "arch" must say `type: arch`, and
	// the prefix set is also the allowed-filename set.
	//
	// OPTIONAL. Omit it and the taxonomy is not enforced at all: articles may
	// be named anything and `type:` is not required. The graph does not need
	// it - ownership, edges and the ratchets work the same either way - so a
	// repo that already has docs/ should not have to rename every file to
	// adopt this. Add it later if the taxonomy starts earning its keep.
	ArticleTypes map[string]string `json:"article_types"`

	// AmbiguityRoots are tried when a cited path like "scoring/base.py" fails
	// to resolve from the repo root. If it resolves under exactly one of
	// these, the error says "not repo-root-relative" rather than "missing",
	// because the fix is different: qualify it, don't hunt for the file.
	AmbiguityRoots []string `json:"ambiguity_roots"`

	// ExcludeDirs drops paths from the tracked-file inventory entirely. Each
	// entry is one directory NAME, matched against whole path segments.
	// Defaults to the vendored-dependency directories that are commonly
	// committed; set it explicitly to override rather than extend.
	ExcludeDirs []string `json:"exclude_dirs"`

	// TestRules decides what counts as a test file. Shared by the ratchet's
	// scope rule and the test-link builder, so "is a test" means one thing.
	TestRules TestRules `json:"test_rules"`

	// SourceExtensions are the extensions that can carry an import. A test
	// file outside this set is skipped by the link builder entirely — a PNG
	// fixture parked under a test directory is a test by the path rule, but
	// there is nothing in it to link. Defaults to the union of adapter
	// extensions.
	SourceExtensions []string `json:"source_extensions"`

	// Adapters resolve an import statement in a test file to the production
	// path it exercises. Declarative on purpose: adding a language must not
	// require a Go toolchain.
	Adapters []Adapter `json:"adapters"`

	// StemRules reconstruct the production stem a test file names, as a
	// SECOND source of links for tests that reach their subject through a
	// dispatcher and therefore import it under a different name.
	StemRules []StemRule `json:"stem_rules"`

	// LangFamilies maps an extension to a language family, for the
	// same-language rule on stem matching. ".js" and ".mjs" are one family.
	LangFamilies map[string]string `json:"lang_families"`

	// NotRepoPaths are backticked strings that LOOK like repo paths and
	// genuinely cannot exist in the repo (a cloud-storage object path, an
	// illustrative permalink). Value is the reason, and a reason is required.
	NotRepoPaths map[string]string `json:"not_repo_paths"`

	// GeneratedPaths are real, correct paths that are generated and
	// gitignored, so they exist on a dev machine and not in a fresh checkout.
	// Listed rather than probed: probing the filesystem is what made the
	// original checker pass locally and fail in CI.
	GeneratedPaths map[string]string `json:"generated_paths"`

	// LinkCommands delegate test-link discovery to real tooling. Merged with
	// whatever the regex adapters find, so a repo can use the command for one
	// language and adapters for another.
	LinkCommands []LinkCommand `json:"link_commands"`

	// MaxArticleTokens warns above this estimated size. Advisory: the right
	// length is a judgement, and a build that failed on prose length would
	// just get the threshold raised. Set 0 to disable.
	MaxArticleTokens int `json:"max_article_tokens"`

	// ExtraLinkKinds add repo-specific columns to the reverse index, for
	// relationships no import edge can reveal — an end-to-end suite whose
	// selectors cover a screen flow rather than a file, for instance.
	ExtraLinkKinds []ExtraLinkKind `json:"extra_link_kinds"`
}

// TestRules is a path rule, not a content rule. A file under a test directory
// is a test even if it is a PNG.
type TestRules struct {
	DirSubstrings  []string `json:"dir_substrings"`  // "/test/", "/tests/"
	PathPrefixes   []string `json:"path_prefixes"`   // "test/", "integration_test/"
	BasePrefixes   []string `json:"base_prefixes"`   // "test_"  (pytest)
	BaseSuffixes   []string `json:"base_suffixes"`   // "_test.dart", "_test.py"
	PathSubstrings []string `json:"path_substrings"` // ".spec."
}

// Adapter turns an import into a candidate production path.
//
// Strategy is one of:
//
//	"template"              Pattern's capture groups fill Resolve templates
//	                        ($1, $2). Every template that names a tracked
//	                        file is a hit.
//	"dotted-longest-prefix" The capture is a dotted module path. Try the
//	                        longest prefix first under Root, stop at the first
//	                        tracked hit. Handles a package re-exporting a
//	                        submodule.
//	"relative"              The capture is a relative specifier resolved
//	                        against the importing file's directory.
//	"package"               The capture names a DIRECTORY, not a file. Go,
//	                        Java and other package-based languages import a
//	                        package and get every file in it, so there is no
//	                        single file to resolve to — the link is to every
//	                        source file directly inside that directory.
type Adapter struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
	Pattern    string   `json:"pattern"`
	Strategy   string   `json:"strategy"`
	Resolve    []string `json:"resolve"`

	// Root is the directory a dotted module path is resolved under. Use ""
	// or "." when the import root IS the repo root, which is the normal case
	// for Go, most JS projects, and any Python project whose package sits at
	// the top level.
	Root string `json:"root"`

	// UnderscoreRoot is the directory UnderscoreSplit reconstructs under,
	// defaulting to Root. The two are separate because they answer different
	// questions and genuinely diverge: a project may import `app.core.engine`
	// from the repo root while its flattened test names (`test_core_engine`)
	// are relative to `app/`.
	UnderscoreRoot string `json:"underscore_root"`

	// UnderscoreSplit reconstructs a path from a flattened test stem: pytest
	// turns "app/core/engine.py" into "test_core_engine.py". Try each
	// underscore as the separator and accept ONLY when exactly one candidate
	// is a real tracked path — so this builds a path rather than guessing at
	// a resemblance.
	UnderscoreSplit bool `json:"underscore_split"`

	re *regexp.Regexp
}

// Re returns the compiled pattern.
func (a *Adapter) Re() *regexp.Regexp { return a.re }

// StemRule strips a language's test marker to recover the production stem.
// Exactly one of Suffix, or Prefix+Ext, or Pattern is set.
type StemRule struct {
	Suffix  string `json:"suffix"` // "_test.dart" -> foo_test.dart => foo
	Prefix  string `json:"prefix"` // "test_" with Ext ".py" => foo
	Ext     string `json:"ext"`
	Pattern string `json:"pattern"` // capture group 1 is the stem

	re *regexp.Regexp
}

// Re returns the compiled pattern, or nil for the non-regex forms.
func (s *StemRule) Re() *regexp.Regexp { return s.re }

// ExtraLinkKind reads a repo-specific mapping of source path -> labels out of
// a file the repo already generates, rather than inventing a second contract.
type ExtraLinkKind struct {
	Name    string `json:"name"`    // reverse-index column, e.g. "phases"
	Source  string `json:"source"`  // file to read, repo-root-relative
	Pattern string `json:"pattern"` // group 1 = key, group 2 = labels
	Split   string `json:"split"`   // separator inside group 2
	Missing string `json:"missing"` // advice printed when Source is absent

	// Match says how a key from Source is compared against a repo path:
	//
	//	"exact"      the key IS a repo-root-relative path (default)
	//	"prefix"     the key is a path prefix — "src/widgets/" covers the tree
	//	"substring"  the key appears anywhere in the path
	//
	// Getting this wrong is uniquely nasty, because the failure is a SILENTLY
	// EMPTY column rather than a wrong one, and an empty list reads as "no
	// links needed". A selector file whose keys are patterns, read as though
	// they were paths, yields nothing for exactly the broadest entries — the
	// ones matching everything are the least likely to be spelled as a full
	// path. Check a known-broad key against the generated index before
	// trusting the column.
	Match string `json:"match"`

	re *regexp.Regexp
}

// Matches reports whether a key from the source file applies to a repo path.
func (e *ExtraLinkKind) Matches(key, path string) bool {
	switch e.Match {
	case "prefix":
		return strings.HasPrefix(path, key)
	case "substring":
		return strings.Contains(path, key)
	default:
		return path == key
	}
}

// Re returns the compiled pattern.
func (e *ExtraLinkKind) Re() *regexp.Regexp { return e.re }

// JoinRoot joins a configured root with a path, treating "" and "." as the
// repo root rather than emitting a "./" prefix that matches no tracked file.
func JoinRoot(root, rest string) string {
	if root == "" || root == "." {
		return rest
	}
	return root + "/" + rest
}

// Discover finds the config when none was named explicitly.
//
// Finding more than one is an error rather than a precedence rule. Two configs
// in a repo means one of them is stale, and silently preferring either is how
// a change gets made to the file nobody is reading.
func Discover() (string, error) {
	var found []string
	for _, p := range SearchPaths {
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return "", os.ErrNotExist
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("found %d configs (%s). Keep one: two means "+
			"one is stale, and preferring either silently would let a change "+
			"land in the file nobody reads", len(found), strings.Join(found, ", "))
	}
}

// Load reads and validates a config, applying defaults. YAML or JSON, decided
// by extension; YAML is a superset, so the same decoder handles both.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Convert to JSON first so the `json:` struct tags govern both formats and
	// cannot drift apart, and so unknown-key rejection works identically.
	j, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(j)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.finalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Finalize applies defaults and validates. Exported for tests that construct
// a Config directly rather than loading one.
func (c *Config) Finalize() error { return c.finalize() }

func (c *Config) finalize() error {
	if c.KBDir == "" {
		c.KBDir = "docs/kb"
	}
	c.KBDir = strings.TrimSuffix(c.KBDir, "/")
	if len(c.ProdRoots) == 0 {
		return fmt.Errorf("prod_roots is empty: the orphan ratchet would " +
			"govern nothing and pass vacuously")
	}
	// A prod root is a literal path prefix, not a glob, and the two spellings
	// people reach for first — a trailing slash and a `dir/**` borrowed from
	// `covers:` — match NOTHING. That empties the ratchet's scope and the run
	// still reports OK, which is the vacuous pass the check above exists to
	// refuse. Normalise what is unambiguous, reject what is not.
	for i, r := range c.ProdRoots {
		if strings.ContainsAny(r, "*?[") {
			return fmt.Errorf("prod_roots[%d] = %q: a prod root is a "+
				"directory prefix, not a glob. Write %q", i, r,
				strings.TrimSuffix(strings.TrimRight(r, "*?[]"), "/"))
		}
		n := strings.TrimSuffix(strings.TrimPrefix(r, "./"), "/")
		if n == "" || n == "." {
			return fmt.Errorf("prod_roots[%d] = %q: name a directory, or "+
				"drop the entry", i, r)
		}
		c.ProdRoots[i] = n
	}
	if len(c.ProdExtensions) == 0 {
		// The same vacuous pass as an empty prod_roots, one field over:
		// IsProductionSource requires a matching extension, so an empty list
		// makes it false for every file and `kb graph` reports OK on a repo
		// where nothing is documented.
		return fmt.Errorf("prod_extensions is empty: no file would count as " +
			"production source and the orphan ratchet would pass vacuously")
	}
	// A prod extension missing from path_extensions is invisible to the prose
	// scanner, so an article can cite a production file by name and have that
	// citation silently unchecked — the one guarantee this whole tool sells.
	// The default makes them equal; an explicit narrower list does not, and
	// nothing said so.
	if len(c.PathExtensions) > 0 {
		have := map[string]bool{}
		for _, e := range c.PathExtensions {
			have[e] = true
		}
		var missing []string
		for _, e := range c.ProdExtensions {
			if !have[e] {
				missing = append(missing, e)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("path_extensions is missing %s, which "+
				"prod_extensions declares. The prose scanner only recognises a "+
				"path whose extension is in path_extensions, so an article "+
				"could cite a production file of that type and the citation "+
				"would never be checked — silently, which is the failure this "+
				"tool exists to prevent. path_extensions must be a SUPERSET "+
				"of prod_extensions.", strings.Join(missing, ", "))
		}
	}
	if len(c.PathExtensions) == 0 {
		c.PathExtensions = c.ProdExtensions
	}
	if c.MaxArticleTokens == 0 {
		// Generous on purpose. This should catch the article that has become
		// a subsystem manual, not nudge every article toward a target length.
		c.MaxArticleTokens = 12000
	}
	if c.ExcludeDirs == nil {
		// Committed dependency trees are not this repo's source. Without
		// this a Go repo with a vendor/ directory or a JS repo that commits
		// node_modules starts with thousands of orphans, and a baseline
		// nobody could ever work through is a baseline nobody reads.
		c.ExcludeDirs = []string{
			"node_modules", "vendor", "venv", ".venv",
			"site-packages", "third_party",
		}
	}
	// `link_commands` rejects any edge whose test side `test_rules` does not
	// classify as a test. TestRules has no default and IsTestFile then returns
	// false for everything, so the two together are not merely unhelpful, they
	// cannot both be satisfied: every edge the command emits is refused and
	// the command is reported as broken. Catch it at load, where the fix is
	// obvious, rather than at the first run against a real repo.
	if len(c.LinkCommands) > 0 && c.TestRules.IsEmpty() {
		return fmt.Errorf("link_commands is set but test_rules is empty. A " +
			"link command's output is validated against test_rules, and with " +
			"none configured nothing is a test, so EVERY edge it emits is " +
			"rejected and the command fails as though the toolchain were " +
			"broken. Declare test_rules, or drop the link command.")
	}
	for _, d := range c.ExcludeDirs {
		// The predecessor took substrings, so every existing config spells
		// these with slashes. Silently accepting `vendor/` would match no
		// segment and quietly un-exclude a vendored tree, which reads as
		// thousands of new orphans rather than as a config error.
		if d == "" || strings.ContainsAny(d, "/*") {
			return fmt.Errorf("exclude_dirs[%q] is not a directory name: "+
				"entries are matched against whole path segments, so write "+
				"`vendor`, not `vendor/` or `**/vendor/**`", d)
		}
	}
	for k, v := range c.NotRepoPaths {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("not_repo_paths[%q] has no reason. An allowlist "+
				"entry without a reason is indistinguishable from a mistake", k)
		}
	}
	for k, v := range c.GeneratedPaths {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("generated_paths[%q] has no reason", k)
		}
	}
	for i := range c.Adapters {
		a := &c.Adapters[i]
		re, err := regexp.Compile(a.Pattern)
		if err != nil {
			return fmt.Errorf("adapter %q: bad pattern: %w", a.Name, err)
		}
		a.re = re
		// Every strategy reads capture group 1 as the import path, and the
		// resolver indexes it unconditionally. A pattern with no group is a
		// config mistake, and it used to surface as `panic: index out of
		// range [1]` with a Go stack trace on the first matching file.
		if re.NumSubexp() < 1 {
			return fmt.Errorf("adapter %q: pattern %q has no capture group. "+
				"Group 1 must capture the imported path", a.Name, a.Pattern)
		}
		switch a.Strategy {
		case "template":
			if len(a.Resolve) == 0 {
				return fmt.Errorf("adapter %q: strategy \"template\" needs "+
					"at least one resolve template", a.Name)
			}
		case "package":
			if len(a.Resolve) == 0 {
				return fmt.Errorf("adapter %q: strategy \"package\" needs at "+
					"least one resolve template naming the directory", a.Name)
			}
		case "dotted-longest-prefix":
			// An empty Root is meaningful here (the repo root), so there is
			// nothing to require.
		case "relative":
		default:
			return fmt.Errorf("adapter %q: unknown strategy %q", a.Name, a.Strategy)
		}
	}
	if len(c.SourceExtensions) == 0 {
		seen := map[string]bool{}
		for _, a := range c.Adapters {
			for _, e := range a.Extensions {
				if !seen[e] {
					seen[e] = true
					c.SourceExtensions = append(c.SourceExtensions, e)
				}
			}
		}
	}
	for i := range c.Adapters {
		a := &c.Adapters[i]
		if a.UnderscoreRoot == "" {
			a.UnderscoreRoot = a.Root
		}
	}
	for i := range c.StemRules {
		s := &c.StemRules[i]
		if s.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return fmt.Errorf("stem rule: bad pattern: %w", err)
		}
		s.re = re
	}
	for i := range c.LinkCommands {
		if len(c.LinkCommands[i].Command) == 0 {
			return fmt.Errorf("link_commands[%d] (%q) has no command",
				i, c.LinkCommands[i].Name)
		}
	}
	for i := range c.ExtraLinkKinds {
		e := &c.ExtraLinkKinds[i]
		re, err := regexp.Compile(e.Pattern)
		if err != nil {
			return fmt.Errorf("extra link kind %q: bad pattern: %w", e.Name, err)
		}
		e.re = re
		// ExtraLinks reads groups 1 and 2 unconditionally, so a pattern with
		// fewer panicked the whole gate instead of failing as the config
		// error it is.
		if re.NumSubexp() < 2 {
			return fmt.Errorf("extra link kind %q: pattern %q has %d capture "+
				"group(s), needs 2 (group 1 = key, group 2 = labels)",
				e.Name, e.Pattern, re.NumSubexp())
		}
		if e.Split == "" {
			e.Split = ","
		}
		switch e.Match {
		case "", "exact", "prefix", "substring":
		default:
			return fmt.Errorf("extra link kind %q: unknown match %q "+
				"(exact, prefix, substring)", e.Name, e.Match)
		}
		if e.Name == "articles" || e.Name == "tests" {
			return fmt.Errorf("extra link kind %q collides with a built-in "+
				"reverse-index column", e.Name)
		}
	}
	return nil
}

// ReverseIndexPath, OrphanBaselinePath, DeadEndBaselinePath and ManifestPath
// are derived from KBDir so a repo never names them twice.
func (c *Config) ReverseIndexPath() string    { return c.KBDir + "/.reverse-index.json" }
func (c *Config) OrphanBaselinePath() string  { return c.KBDir + "/.orphans-baseline.txt" }
func (c *Config) DeadEndBaselinePath() string { return c.KBDir + "/.dead-ends-baseline.txt" }
func (c *Config) ManifestPath() string        { return c.KBDir + "/.compiled-sources.json" }
func (c *Config) DigestPath() string          { return c.KBDir + "/DIGEST.md" }
func (c *Config) IndexPath() string           { return c.KBDir + "/INDEX.md" }

// IsTestFile is a PATH rule. Shared by the ratchet's scope and the link
// builder so the two cannot drift.
func (c *Config) IsTestFile(f string) bool {
	base := f[strings.LastIndex(f, "/")+1:]
	t := c.TestRules
	for _, s := range t.DirSubstrings {
		if strings.Contains(f, s) {
			return true
		}
	}
	for _, p := range t.PathPrefixes {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	for _, p := range t.BasePrefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	for _, s := range t.BaseSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	for _, s := range t.PathSubstrings {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// IsProductionSource is the orphan ratchet's scope: under a prod root, with a
// prod extension, and not a test.
func (c *Config) IsProductionSource(f string) bool {
	underRoot := false
	for _, r := range c.ProdRoots {
		if f == r || strings.HasPrefix(f, r+"/") {
			underRoot = true
			break
		}
	}
	if !underRoot {
		return false
	}
	hasExt := false
	for _, e := range c.ProdExtensions {
		if strings.HasSuffix(f, e) {
			hasExt = true
			break
		}
	}
	if !hasExt {
		return false
	}
	if c.ExcludedPath(f) {
		return false
	}
	return !c.IsTestFile(f)
}

// ExcludedPath reports whether a path lies under an excluded directory.
//
// Matching is on whole path SEGMENTS, and on directory segments only. The
// predecessor matched substrings, and its default list had to carry both
// "vendor/" and "/vendor/" to cover the root and the nested case — the
// segment rule, hand-rolled and wrong. The unanchored spelling also matched
// `src/myvendor/x.go` and `pkg/openvendor/a.go`, dropping real source out of
// the inventory so that no gate ever saw it. A file this tool cannot see is
// a file nobody is ever asked to document, which is the one failure mode a
// documentation gate must not have.
func (c *Config) ExcludedPath(f string) bool {
	if len(c.ExcludeDirs) == 0 {
		return false
	}
	segs := strings.Split(f, "/")
	// The basename is not a directory. A file named `vendor` is source.
	for _, seg := range segs[:len(segs)-1] {
		for _, d := range c.ExcludeDirs {
			if seg == d {
				return true
			}
		}
	}
	return false
}

// IsEmpty reports whether no test convention is configured at all, which
// makes IsTestFile false for every path in the repository.
func (t TestRules) IsEmpty() bool {
	return len(t.DirSubstrings) == 0 && len(t.PathPrefixes) == 0 &&
		len(t.BasePrefixes) == 0 && len(t.BaseSuffixes) == 0 &&
		len(t.PathSubstrings) == 0
}

// LangFamily reports which language a path belongs to, for the same-language
// rule on stem matching. Unknown extensions get their own family so they
// never match each other by accident.
func (c *Config) LangFamily(path string) string {
	dot := strings.LastIndex(path, ".")
	if dot < 0 {
		return "other"
	}
	if fam, ok := c.LangFamilies[path[dot:]]; ok {
		return fam
	}
	return "other"
}

// TestStem recovers the production stem a test file names, or "" if the file
// follows no configured convention.
func (c *Config) TestStem(path string) string {
	base := path[strings.LastIndex(path, "/")+1:]
	if len(c.SourceExtensions) == 0 {
		seen := map[string]bool{}
		for _, a := range c.Adapters {
			for _, e := range a.Extensions {
				if !seen[e] {
					seen[e] = true
					c.SourceExtensions = append(c.SourceExtensions, e)
				}
			}
		}
	}
	for i := range c.StemRules {
		s := &c.StemRules[i]
		switch {
		case s.Suffix != "":
			if strings.HasSuffix(base, s.Suffix) {
				return strings.TrimSuffix(base, s.Suffix)
			}
		case s.Prefix != "":
			if strings.HasPrefix(base, s.Prefix) && strings.HasSuffix(base, s.Ext) {
				return base[len(s.Prefix) : len(base)-len(s.Ext)]
			}
		case s.re != nil:
			if m := s.re.FindStringSubmatch(base); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// LinkCommand delegates test-link discovery to the language's own tooling.
//
// The regex adapters approximate a resolver. This asks the real one. `go list
// -deps -json`, `madge`, `pydeps` and friends already know the dependency
// graph exactly, including the cases a regex cannot see: build tags, aliased
// imports, re-exports, generated code.
//
// The command emits one edge per line, tab-separated:
//
//	<test file>\t<production file>
//
// Both repo-root-relative. Nothing else about the language reaches this
// program, which is the point: a repo teaches `kb` about its own toolchain by
// supplying a script, not by waiting for a strategy to be added here.
//
// Two costs, both deliberate. The toolchain has to be installed, and a missing
// one is a hard error rather than an empty column, because a silently empty
// column reads as "no links needed". And the command runs on every `kb graph`,
// so it wants to be seconds, not minutes.
type LinkCommand struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`

	// Optional marks a command whose absence is tolerable. The links it would
	// have produced are simply missing, which will make the committed reverse
	// index look stale. Use it only where that trade is understood.
	Optional bool `json:"optional"`
}
