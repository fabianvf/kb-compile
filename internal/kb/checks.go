package kb

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/fabianvf/kb-compile/internal/config"
)

// Checker accumulates problems. Errors fail the build; warnings surface drift
// without holding the build hostage to a citation style.
type Checker struct {
	Cfg      *config.Config
	Articles map[string]*Article
	Tracked  []string
	trackSet map[string]bool
	byDir    map[string][]string

	Errors   []string
	Warnings []string
}

// New builds a Checker over the given article set and tracked-file inventory.
func New(cfg *config.Config, articles map[string]*Article, tracked []string) *Checker {
	ts := make(map[string]bool, len(tracked))
	for _, f := range tracked {
		ts[f] = true
	}
	return &Checker{Cfg: cfg, Articles: articles, Tracked: tracked, trackSet: ts}
}

func (c *Checker) errf(format string, a ...any) {
	c.Errors = append(c.Errors, fmt.Sprintf(format, a...))
}

func (c *Checker) warnf(format string, a ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, a...))
}

// sortedIDs gives every walk a deterministic order, so output is identical on
// any machine. A gate whose output depends on map iteration order is a gate
// that fails differently in CI than it does locally.
func (c *Checker) sortedIDs() []string {
	ids := make([]string, 0, len(c.Articles))
	for id := range c.Articles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// PathIsKnown reports whether the repo actually contains p.
func (c *Checker) PathIsKnown(p string) bool {
	if c.trackSet[p] {
		return true
	}
	_, gen := c.Cfg.GeneratedPaths[p]
	return gen
}

// CheckFrontmatter enforces that every article identifies itself when read in
// isolation, and that its declared type matches its filename prefix.
func (c *Checker) CheckFrontmatter() {
	prefixes := make([]string, 0, len(c.Cfg.ArticleTypes))
	for p := range c.Cfg.ArticleTypes {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		if !a.SawFrontmatter {
			c.errf("%s:1: missing YAML frontmatter. Every article must open "+
				"with `---` and declare `id` and `type` so it identifies "+
				"itself when read in isolation.", a.Path)
			continue
		}
		if a.DeclaredID != a.ID {
			c.errf("%s: frontmatter `id: %s` does not match the filename "+
				"(expected `id: %s`).", a.Path, a.DeclaredID, a.ID)
		}
		prefix := a.ID
		if i := strings.Index(prefix, "-"); i >= 0 {
			prefix = prefix[:i]
		}
		expected, ok := c.Cfg.ArticleTypes[prefix]
		if !ok {
			c.errf("%s: filename prefix %q is not a known article type (%s).",
				a.Path, prefix, strings.Join(prefixes, ", "))
		} else if a.DeclaredType != expected {
			c.errf("%s: frontmatter `type: %s` does not match the %q filename "+
				"prefix (expected `type: %s`).", a.Path, a.DeclaredType, prefix+"-", expected)
		}
	}
}

// CheckArticleEdges validates every article-to-article edge resolves.
func (c *Checker) CheckArticleEdges() {
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		for _, pair := range []struct {
			key     string
			targets []string
		}{{"gated_by", a.GatedBy}, {"see_also", a.SeeAlso}} {
			for _, target := range pair.targets {
				tid := strings.TrimSuffix(target, ".md")
				switch {
				case tid == a.ID:
					c.errf("%s: `%s` lists itself.", a.Path, pair.key)
				case c.Articles[tid] == nil:
					c.errf("%s: `%s: %s` points at an article that does not exist.",
						a.Path, pair.key, target)
				}
			}
		}
		for _, target := range sortedKeys(a.Links) {
			tid := strings.TrimSuffix(target, ".md")
			if tid == "INDEX" {
				continue
			}
			if c.Articles[tid] == nil {
				c.errf("%s:%d: link to `%s`, which is not an article in %s.",
					a.Path, a.Links[target], target, c.Cfg.KBDir)
			}
		}
	}
}

// CheckFileEdges validates every path an article names actually resolves.
//
// This is the check the whole KB rests on. Where a project makes reading the
// KB mandatory, every path an article names is a path an agent will open. A renamed file leaves the article pointing at nothing and the next
// agent proceeds WITHOUT the context, silently.
func (c *Checker) CheckFileEdges() {
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		for _, p := range sortedKeys(a.FilesBlock) {
			if _, ok := c.Cfg.NotRepoPaths[p]; ok {
				continue
			}
			if !c.PathIsKnown(p) {
				c.errf("%s:%d: FILES block names `%s`, which git does not "+
					"track. Paths must be repo-root-relative. If it is "+
					"generated and gitignored, add it to `generated_paths` in "+
					"%s with a reason.", a.Path, a.FilesBlock[p], p, config.DefaultPath)
			}
		}
		for _, p := range sortedKeys(a.CitedPaths) {
			if _, ok := c.Cfg.NotRepoPaths[p]; ok {
				continue
			}
			if !strings.Contains(p, "/") {
				continue // a bare filename is prose, not a path
			}
			if c.PathIsKnown(p) {
				continue
			}
			// A path like `scoring/base.py` that resolves under exactly one
			// root is AMBIGUOUS, not merely missing. Say so, because the fix
			// is different: qualify it, don't hunt for the file.
			if sug := c.resolveAmbiguous(p); sug != "" {
				c.errf("%s:%d: `%s` is not repo-root-relative — it resolves to "+
					"`%s`. Qualify it: an unrooted path silently matches "+
					"nothing when checked.", a.Path, a.CitedPaths[p], p, sug)
			} else {
				c.errf("%s:%d: `%s` is not tracked by git. If it is not a repo "+
					"file, add it to `not_repo_paths`; if it is generated and "+
					"gitignored, add it to `generated_paths` — both in %s, "+
					"with a reason.", a.Path, a.CitedPaths[p], p, config.DefaultPath)
			}
		}
	}
}

func (c *Checker) resolveAmbiguous(p string) string {
	for _, root := range c.Cfg.AmbiguityRoots {
		if cand := root + "/" + p; c.PathIsKnown(cand) {
			return cand
		}
	}
	return ""
}

// CheckGlobsAreLive fails a `covers:` glob that matches nothing — the
// subsystem moved or was deleted, and the article now owns nothing.
func (c *Checker) CheckGlobsAreLive() {
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		for _, g := range a.Covers {
			hits := 0
			for _, f := range c.Tracked {
				if GlobMatches(f, g) {
					hits++
				}
			}
			if hits == 0 {
				c.errf("%s: `covers:` glob `%s` matches no tracked file. The "+
					"subsystem moved or was deleted.", a.Path, g)
			}
		}
	}
}

// GlobMatches implements the two glob forms the KB uses: `dir/**` matches
// anything beneath dir; otherwise a simple `*` that does not cross `/`.
func GlobMatches(path, glob string) bool {
	if strings.HasSuffix(glob, "/**") {
		return strings.HasPrefix(path, strings.TrimSuffix(glob, "**"))
	}
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(glob), `\*`, `[^/]*`) + "$")
	return re.MatchString(path)
}

var anchorRe = regexp.MustCompile(`\]\(([a-z0-9][a-z0-9\-]*)\.md\)\s*§\s*([^.,;:()\[\]` + "`" + `\n]+)`)
var leadingNumRe = regexp.MustCompile(`^\d`)

// CheckSectionAnchors is ADVISORY.
//
// `[…](arch-foo.md) § SECTION` is sub-node addressing: it sends the reader to
// one section rather than 500 lines, and those anchors drift when a section is
// renamed. But the `§` convention is written loosely, so hard-failing would
// mean rewriting prose that is doing its job. A warning surfaces the drift
// without holding the build hostage to a citation style.
func (c *Checker) CheckSectionAnchors() {
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		data, err := os.ReadFile(a.Path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range anchorRe.FindAllStringSubmatch(line, -1) {
				target := c.Articles[m[1]]
				anchor := strings.ToLower(strings.TrimSpace(m[2]))
				if target == nil || anchor == "" {
					continue
				}
				// `§ 4` addresses a numbered STEP in a recipe, not a named
				// section. Recipes number their steps: a real citation form.
				if leadingNumRe.MatchString(anchor) {
					continue
				}
				hit := false
				for _, h := range target.Anchors {
					if strings.Contains(h, anchor) || strings.Contains(anchor, h) {
						hit = true
						break
					}
				}
				if !hit {
					c.warnf("%s:%d: \"§ %s\" names no heading in %s.md — the "+
						"section was renamed or removed.",
						a.Path, i+1, strings.TrimSpace(m[2]), m[1])
				}
			}
		}
	}
}

// CheckIndex enforces the hub. INDEX.md is where every agent enters, so a
// missing entry makes an article effectively invisible and a dead entry sends
// the reader nowhere.
func (c *Checker) CheckIndex() {
	path := c.Cfg.IndexPath()
	data, err := os.ReadFile(path)
	if err != nil {
		c.errf("%s is missing.", path)
		return
	}
	linked := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			id := strings.TrimSuffix(m[1], ".md")
			if id == "INDEX" {
				continue
			}
			if c.Articles[id] == nil {
				c.errf("%s:%d: links `%s`, which is not an article in %s.",
					path, i+1, m[1], c.Cfg.KBDir)
			} else {
				linked[id] = true
			}
		}
	}
	for _, id := range c.sortedIDs() {
		if !linked[id] {
			c.errf("%s does not link `%s.md`. Every article needs an entry, "+
				"or nothing routes a reader to it.", path, id)
		}
	}
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
