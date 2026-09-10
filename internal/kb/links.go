package kb

import (
	"os"
	"sort"
	"strings"

	"github.com/fabianvf/kb-compile/internal/config"
)

// BuildOwnership maps source path -> the article ids claiming it.
//
// Two sources, both ownership: an explicit `## FILES` entry and a `covers:`
// glob. Tracked-only, so the generated index is byte-identical on any machine.
func (c *Checker) BuildOwnership() map[string][]string {
	owners := map[string]map[string]bool{}
	add := func(p, id string) {
		if owners[p] == nil {
			owners[p] = map[string]bool{}
		}
		owners[p][id] = true
	}
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		for p := range a.FilesBlock {
			if _, skip := c.Cfg.NotRepoPaths[p]; skip {
				continue
			}
			if !c.trackSet[p] {
				continue
			}
			add(p, id)
		}
		for _, g := range a.Covers {
			for _, f := range c.Tracked {
				if GlobMatches(f, g) {
					add(f, id)
				}
			}
		}
	}
	return flatten(owners)
}

// OwnedForFreshness is the set the freshness gate tracks: files with at least
// one owning ARTICLE, excluding tests.
//
// Both filters matter and neither is arbitrary. A file present in the index
// only for its test links has no article to re-read, so failing on it hands
// the agent a filename and no scope. And test files are out of the KB's
// ownership scope entirely — they answer to their own review discipline — so
// tracking them here would demand a KB compile for a change the KB does not
// document. The reverse index applies the same test-file exclusion, and the
// two must agree or the gate and the lookup describe different repos.
func (c *Checker) OwnedForFreshness(ownership map[string][]string) map[string][]string {
	out := make(map[string][]string, len(ownership))
	for p, arts := range ownership {
		if len(arts) == 0 || c.Cfg.IsTestFile(p) {
			continue
		}
		out[p] = arts
	}
	return out
}

// BuildTestLinks maps production path -> the test files that EXERCISE it.
//
// Derived from real imports, never from filename similarity. An agent changing
// a service should not have to guess which tests to update, and "the file with
// a similar name" is a guess that is wrong exactly when the code has been
// refactored, which is when it matters.
func (c *Checker) BuildTestLinks() map[string][]string {
	links := map[string]map[string]bool{}

	for _, t := range c.Tracked {
		if !c.Cfg.IsTestFile(t) {
			continue
		}
		// `IsTestFile` is a PATH rule, so it also matches non-source parked
		// under a test directory (a binary PNG fixture). Skip anything that
		// cannot carry an import: there is nothing in it to link, by either
		// route.
		if !c.hasExt(t, c.Cfg.SourceExtensions) {
			continue
		}
		ad := c.adapterFor(t)
		stem := c.Cfg.TestStem(t)

		hits := map[string]bool{}

		// ── 1. real imports ──
		if ad != nil {
			data, err := os.ReadFile(t)
			if err == nil {
				for _, cand := range c.resolveImports(ad, t, string(data)) {
					hits[cand] = true
				}
			}
		}

		// ── 2. exact-stem convention, as a SECOND source ──
		//
		// Not filename similarity: the stem must match EXACTLY after stripping
		// the language's test marker. It exists because an import edge goes
		// missing whenever a test reaches its subject through a dispatcher —
		// the test imports the dispatcher, never the file it is really testing.
		if stem != "" {
			for _, cand := range c.Tracked {
				if c.Cfg.IsTestFile(cand) {
					continue
				}
				if !c.stemLinkAllowed(t, cand, stem) {
					continue
				}
				b := cand[strings.LastIndex(cand, "/")+1:]
				if dot := strings.LastIndex(b, "."); dot > 0 && b[:dot] == stem {
					hits[cand] = true
				}
			}
			if ad != nil && ad.UnderscoreSplit {
				if built := c.underscoreSplit(ad.UnderscoreRoot, ad.Extensions[0], stem); built != "" {
					hits[built] = true
				}
			}
		}

		for h := range hits {
			// A test importing a test helper is not a link.
			if c.Cfg.IsTestFile(h) {
				continue
			}
			if links[h] == nil {
				links[h] = map[string]bool{}
			}
			links[h][t] = true
		}
	}
	return flatten(links)
}

// stemLinkAllowed decides whether a stem match may link a test to a candidate.
//
// Same language: always. Across languages: only when the stem is distinctive
// (multi-word). Both halves are load-bearing. Blanket cross-language matching
// links `app.js` to `app_test.dart` and `main.dart` to `test_main.py`, which is
// noise. But forbidding it outright drops the links that carry the most weight:
// a typed-callable client wrapper and its server-side test are two halves of
// ONE contract, and those are exactly the cross-surface edges an agent needs
// and no import can ever reveal.
func (c *Checker) stemLinkAllowed(testPath, candidate, stem string) bool {
	return c.Cfg.LangFamily(candidate) == c.Cfg.LangFamily(testPath) ||
		strings.Contains(stem, "_")
}

// underscoreSplit reconstructs a package path from a flattened test stem.
//
// pytest turns `app/core/engine.py` into `test_core_engine.py`.
// Try each underscore as the separator and accept ONLY when exactly one
// candidate is a real tracked path — so this builds a path rather than
// guessing at a resemblance. Ambiguity is dropped, not resolved.
func (c *Checker) underscoreSplit(root, ext, stem string) string {
	parts := strings.Split(stem, "_")
	var built []string
	for i := 1; i < len(parts); i++ {
		cand := config.JoinRoot(root, strings.Join(parts[:i], "_")+"/"+
			strings.Join(parts[i:], "_")+ext)
		if c.trackSet[cand] {
			built = append(built, cand)
		}
	}
	if len(built) == 1 {
		return built[0]
	}
	return ""
}

func (c *Checker) adapterFor(path string) *config.Adapter {
	for i := range c.Cfg.Adapters {
		if c.hasExt(path, c.Cfg.Adapters[i].Extensions) {
			return &c.Cfg.Adapters[i]
		}
	}
	return nil
}

func (c *Checker) hasExt(path string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(path, e) {
			return true
		}
	}
	return false
}

// resolveImports runs one adapter's strategy over a test file's source.
func (c *Checker) resolveImports(a *config.Adapter, testPath, src string) []string {
	var out []string
	for _, m := range a.Re().FindAllStringSubmatch(src, -1) {
		switch a.Strategy {
		case "template":
			for _, tmpl := range a.Resolve {
				cand := expand(tmpl, m)
				if c.trackSet[cand] {
					out = append(out, cand)
				}
			}
		case "dotted-longest-prefix":
			// `from scoring.base import X` may mean scoring/base.py OR
			// scoring.py re-exporting it. Longest prefix first, stop at the
			// first real hit.
			parts := strings.Split(m[1], ".")
			ext := a.Extensions[0]
			for n := len(parts); n >= 1; n-- {
				cand := config.JoinRoot(a.Root, strings.Join(parts[:n], "/")+ext)
				if c.trackSet[cand] {
					out = append(out, cand)
					break
				}
			}
		case "relative":
			dir := ""
			if slash := strings.LastIndex(testPath, "/"); slash >= 0 {
				dir = testPath[:slash]
			}
			if cand := normalizeRelative(dir, m[1]); cand != "" && c.trackSet[cand] {
				out = append(out, cand)
			}
		}
	}
	return out
}

// expand substitutes $1..$9 in a resolve template from the match groups.
func expand(tmpl string, m []string) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] == '$' && i+1 < len(tmpl) && tmpl[i+1] >= '1' && tmpl[i+1] <= '9' {
			g := int(tmpl[i+1] - '0')
			if g < len(m) {
				b.WriteString(m[g])
			}
			i++
			continue
		}
		b.WriteByte(tmpl[i])
	}
	return b.String()
}

// normalizeRelative resolves a relative specifier against dir.
//
// A tracked file at the repo ROOT has no '/', so its relative imports resolve
// against an empty prefix. Escaping above the root returns "" rather than a
// nonsense path.
func normalizeRelative(dir, rel string) string {
	var parts []string
	if dir != "" {
		parts = strings.Split(dir, "/")
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case ".", "":
			continue
		case "..":
			if len(parts) == 0 {
				return ""
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/")
}

// ExtraLinks reads each configured extra link kind out of the file the repo
// already generates, rather than inventing a second contract.
func (c *Checker) ExtraLinks() map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for i := range c.Cfg.ExtraLinkKinds {
		e := &c.Cfg.ExtraLinkKinds[i]
		out[e.Name] = map[string][]string{}
		_ = i
		data, err := os.ReadFile(e.Source)
		if err != nil {
			c.warnf("%s is missing, so the reverse index carries no %s links. %s",
				e.Source, e.Name, e.Missing)
			continue
		}
		// Collect key -> labels, then expand each key across the paths it
		// matches. Expansion runs over the TRACKED file list rather than the
		// keys themselves, because under prefix/substring matching a key is
		// not a path and must never appear in the index as one.
		keyed := map[string][]string{}
		for _, m := range e.Re().FindAllStringSubmatch(string(data), -1) {
			var labels []string
			for _, p := range strings.Split(m[2], e.Split) {
				if p = strings.TrimSpace(p); p != "" {
					labels = append(labels, p)
				}
			}
			if len(labels) > 0 {
				keyed[m[1]] = append(keyed[m[1]], labels...)
			}
		}
		acc := map[string]map[string]bool{}
		for key, labels := range keyed {
			for _, f := range c.Tracked {
				if !e.Matches(key, f) {
					continue
				}
				if acc[f] == nil {
					acc[f] = map[string]bool{}
				}
				for _, l := range labels {
					acc[f][l] = true
				}
			}
		}
		out[e.Name] = flatten(acc)
	}
	return out
}

func flatten(m map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, set := range m {
		vals := make([]string, 0, len(set))
		for v := range set {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		out[k] = vals
	}
	return out
}
