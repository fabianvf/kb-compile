package kb

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/fabianvf/kb-compile/internal/config"
)

// Article is one KB page: its typed frontmatter, its ownership claims, and
// every edge it points along.
type Article struct {
	ID   string
	Path string

	DeclaredID   string
	DeclaredType string
	Covers       []string
	GatedBy      []string
	SeeAlso      []string

	// FilesBlock holds paths named inside a `## FILES` section — the
	// article's explicit ownership claim. Value is the 1-based line.
	FilesBlock map[string]int

	// CitedPaths holds backticked paths anywhere in the prose. Citations, not
	// ownership: they must resolve, but they claim nothing.
	CitedPaths map[string]int

	// Links holds intra-KB markdown link targets, `](foo.md)`.
	Links map[string]int

	SawFrontmatter bool

	// SeeAlsoSection holds the targets of the `## SEE ALSO` section, which are
	// edges too. They were counted but not collected: the section satisfied
	// HasOutboundEdge while contributing nothing to the graph, so a KB that
	// writes its links the way SPEC.md's own example does had no graph at all.
	// Measured on a 36-article KB: 35 SEE ALSO sections, 0 frontmatter edges,
	// every article an island.
	SeeAlsoSection []string

	// Sections holds every `## ` heading in the body, in order, with the
	// leading `## ` stripped and any `: suffix` kept. Recorded so the
	// vocabulary can be checked: SPEC.md calls the section list fixed, and
	// nothing verified it, so a KB could put most of its content under
	// headings the digest and `kb owners` never read.
	Sections []string

	// HasSeeAlsoSectionLink records whether a `## SEE ALSO` section contains a
	// link to ANOTHER article. A heading with nothing under it is still a dead
	// end, and so is one that only points at itself.
	HasSeeAlsoSectionLink bool

	// Anchors are the addressable targets for `§ Foo` citations: headings AND
	// bolded bullet lead-ins, lowercased.
	Anchors []string
}

var (
	linkRe = regexp.MustCompile(`\]\(([a-z0-9][a-z0-9\-]*\.md)\)`)
	// Zero indent is valid YAML for a block sequence under a key, and is what
	// most people type. Requiring leading whitespace silently dropped every
	// entry of such a list, so a correctly-spelled `covers:` owned nothing.
	fmItemRe   = regexp.MustCompile(`^\s*-\s+(.+?)\s*$`)
	fmKVRe     = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)
	boldLeadRe = regexp.MustCompile(`^\s*(?:[-*]\s*)?\*\*(.+?)\*\*`)
	headingRe  = regexp.MustCompile(`^#+\s*`)
)

// isClaimLine reports whether a line inside `## FILES` is making an ownership
// claim, as opposed to explaining one.
//
// The section runs to the next heading, so a paragraph written under the table
// used to claim every backticked path in it. SPEC §3 gives the block exactly
// two shapes — a list or a table — and prose is neither, but the consequence
// was not cosmetic: a path mentioned in passing was silently added to the
// owned set and thereby ERASED from the orphan ratchet, which is the one
// direction this tool must never move a file without being asked.
func isClaimLine(ln string) bool {
	t := strings.TrimSpace(ln)
	if strings.HasPrefix(t, "|") {
		return true // table row
	}
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")
}

// pathRegexps builds the two path scanners from the configured extensions.
// tokenRe matches a bare path at the start of a fenced line; backtickRe
// matches a `backticked` path anywhere.
func pathRegexps(exts []string) (tokenRe, backtickRe *regexp.Regexp) {
	var bare []string
	for _, e := range exts {
		bare = append(bare, regexp.QuoteMeta(strings.TrimPrefix(e, ".")))
	}
	alt := strings.Join(bare, "|")
	body := `([A-Za-z0-9_][A-Za-z0-9_/.\-]*\.(?:` + alt + `))`
	return regexp.MustCompile(`^\s*` + body + `\b`),
		regexp.MustCompile("`" + body + "`")
}

// ParseArticle reads one article. Errors are appended to errs rather than
// returned, so one malformed article does not hide the rest.
func ParseArticle(cfg *config.Config, path, id string, errs *[]string) (*Article, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	tokenRe, backtickRe := pathRegexps(cfg.PathExtensions)

	a := &Article{
		ID:         id,
		Path:       path,
		FilesBlock: map[string]int{},
		CitedPaths: map[string]int{},
		Links:      map[string]int{},
	}

	// ── frontmatter ──
	body := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		a.SawFrontmatter = true
		listKey := ""
		for i := 1; i < len(lines); i++ {
			ln := lines[i]
			if strings.TrimSpace(ln) == "---" {
				body = i + 1
				break
			}
			if m := fmItemRe.FindStringSubmatch(ln); m != nil {
				switch listKey {
				case "covers":
					a.Covers = append(a.Covers, m[1])
				case "gated_by":
					a.GatedBy = append(a.GatedBy, m[1])
				case "see_also":
					a.SeeAlso = append(a.SeeAlso, m[1])
				}
				continue
			}
			m := fmKVRe.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			key, val := m[1], strings.TrimSpace(m[2])
			listKey = key
			switch key {
			case "id":
				a.DeclaredID = val
			case "type":
				a.DeclaredType = val
			case "covers", "gated_by", "see_also":
				// Three valid spellings: a bare `key:` opens a block list,
				// `key: []` is the explicit empty form, and `key: [a, b]` is
				// the inline form. Dropping the inline one meant a
				// correctly-spelled `covers:` owned nothing and its files
				// landed in the orphan baseline — the same silent no-op that
				// rejecting unknown keys exists to prevent. A scalar is not a
				// list and now says so rather than vanishing.
				if items, ok := parseFlowSeq(val); ok {
					for _, it := range items {
						switch key {
						case "covers":
							a.Covers = append(a.Covers, it)
						case "gated_by":
							a.GatedBy = append(a.GatedBy, it)
						case "see_also":
							a.SeeAlso = append(a.SeeAlso, it)
						}
					}
				} else if val != "" {
					*errs = append(*errs, fmt.Sprintf(
						"%s:%d: `%s:` must be a list — either `%s: [a, b]` or "+
							"a `- ` item per line. A bare value here is read "+
							"as nothing at all.", path, i+1, key, key))
				}
			default:
				*errs = append(*errs, fmt.Sprintf(
					"%s:%d: unknown frontmatter key %q (allowed: id, type, covers, gated_by, see_also).",
					path, i+1, key))
			}
		}
	}

	// ── body ──
	inFiles, inSeeAlso, inFence := false, false, false
	for i := body; i < len(lines); i++ {
		ln := lines[i]
		if strings.HasPrefix(ln, "```") {
			inFence = !inFence
			continue
		}
		// A heading inside a fence is an EXAMPLE, not a section, and reading
		// it as one is a free pass past both ratchets: a fenced `## SEE ALSO`
		// sample clears the dead-end gate, and a fenced `## FILES` sample
		// claims ownership of the paths it shows, erasing them from the
		// orphan gate. Any heading level CLOSES the section it was in; only
		// `## ` opens one, so a `### ` subhead no longer leaves `## FILES`
		// open over the prose below it.
		if !inFence && isHeading(ln) {
			t := strings.TrimSpace(ln)
			inFiles = t == "## FILES"
			inSeeAlso = t == "## SEE ALSO"
			if h, ok := strings.CutPrefix(t, "## "); ok {
				a.Sections = append(a.Sections, strings.TrimSpace(h))
			}
		}

		// A `## FILES` section is written in ONE of two shapes and both are
		// ownership claims: a fenced block of bare paths, or a markdown table
		// whose cells hold backticked paths. Reading only the fence silently
		// dropped every table-shaped article's claims, and those files landed
		// in the orphan baseline as if nothing documented them.
		if inFiles {
			if inFence {
				if m := tokenRe.FindStringSubmatch(ln); m != nil {
					putIfAbsent(a.FilesBlock, m[1], i+1)
				}
			} else if isClaimLine(ln) {
				for _, m := range backtickRe.FindAllStringSubmatch(ln, -1) {
					// A bare filename in a table cell is shorthand relative to
					// the path in the same cell. It cannot be resolved, so it
					// is not an ownership claim; the `covers:` glob is.
					if strings.Contains(m[1], "/") {
						putIfAbsent(a.FilesBlock, m[1], i+1)
					}
				}
			}
		}
		if inSeeAlso {
			for _, m := range linkRe.FindAllStringSubmatch(ln, -1) {
				// A link to SELF is not an outbound edge. Without this, the
				// cheapest way past the dead-end ratchet is a `## SEE ALSO`
				// pointing at the article it is already in — which clears the
				// gate while leaving the reader exactly where they started.
				if t := strings.TrimSuffix(m[1], ".md"); t != a.ID {
					a.HasSeeAlsoSectionLink = true
					a.SeeAlsoSection = append(a.SeeAlsoSection, t)
				}
			}
		}

		for _, m := range backtickRe.FindAllStringSubmatch(ln, -1) {
			putIfAbsent(a.CitedPaths, m[1], i+1)
		}
		for _, m := range linkRe.FindAllStringSubmatch(ln, -1) {
			putIfAbsent(a.Links, m[1], i+1)
		}
	}

	// Anchors span the WHOLE file, frontmatter included, matching the original.
	for _, l := range lines {
		if strings.HasPrefix(l, "#") {
			a.Anchors = append(a.Anchors, strings.ToLower(headingRe.ReplaceAllString(l, "")))
			continue
		}
		if m := boldLeadRe.FindStringSubmatch(l); m != nil {
			a.Anchors = append(a.Anchors, strings.ToLower(m[1]))
		}
	}
	return a, nil
}

// isHeading reports whether a line is an ATX heading of any level, which
// requires a space after the `#` run — `#foo` is a word, not a heading.
func isHeading(ln string) bool {
	h := strings.TrimLeft(ln, "#")
	return len(h) != len(ln) && strings.HasPrefix(h, " ")
}

// parseFlowSeq parses a YAML inline sequence, `[a, b]`, reporting whether the
// value was one at all. `[]` is a sequence with no items, not a non-sequence.
func parseFlowSeq(val string) ([]string, bool) {
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return nil, true
	}
	var out []string
	for _, p := range strings.Split(inner, ",") {
		if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
			out = append(out, p)
		}
	}
	return out, true
}

// HasOutboundEdge reports whether the article points OUT at another article.
//
// Both forms count: the frontmatter `see_also:` is the typed edge this checker
// validates, the `## SEE ALSO` section is the one a reader actually follows.
func (a *Article) HasOutboundEdge() bool {
	return len(a.SeeAlso) > 0 || a.HasSeeAlsoSectionLink
}

// OutboundEdges is every article this one points at, from either form.
//
// Use this for anything that WALKS the graph - retrieval, the digest, eval's
// one-hop expansion. `SeeAlso` alone is the frontmatter contract and is the
// right field for VALIDATING declared edges; it is the wrong field for asking
// where a reader can get to, because most real articles declare their edges in
// the section instead. Targets are returned without the `.md` suffix and
// de-duplicated, in first-seen order so callers that sort get a stable result.
func (a *Article) OutboundEdges() []string {
	seen := map[string]bool{a.ID: true}
	out := make([]string, 0, len(a.SeeAlso)+len(a.SeeAlsoSection))
	for _, t := range append(append([]string(nil), a.SeeAlso...), a.SeeAlsoSection...) {
		t = strings.TrimSuffix(t, ".md")
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func putIfAbsent(m map[string]int, k string, v int) {
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}

// Owns reports whether this article's stated ownership covers path, without
// consulting git.
//
// The ownership map everywhere else is built from the tracked file list, so it
// cannot answer this for a file git does not know about — which is precisely
// the file a warning about untracked source is talking about. This reads the
// claim rather than the result of applying it.
func (a *Article) Owns(path string) bool {
	if _, ok := a.FilesBlock[path]; ok {
		return true
	}
	for _, g := range a.Covers {
		if GlobMatches(path, g) {
			return true
		}
	}
	return false
}
