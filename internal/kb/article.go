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

	// HasSeeAlsoSectionLink records whether a `## SEE ALSO` section contains
	// an actual link. A heading with nothing under it is still a dead end.
	HasSeeAlsoSectionLink bool

	// Anchors are the addressable targets for `§ Foo` citations: headings AND
	// bolded bullet lead-ins, lowercased.
	Anchors []string
}

var (
	linkRe     = regexp.MustCompile(`\]\(([a-z0-9][a-z0-9\-]*\.md)\)`)
	fmItemRe   = regexp.MustCompile(`^\s+-\s+(.+?)\s*$`)
	fmKVRe     = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)
	boldLeadRe = regexp.MustCompile(`^\s*(?:[-*]\s*)?\*\*(.+?)\*\*`)
	sectLinkRe = regexp.MustCompile(`\]\([^)]+\.md`)
	headingRe  = regexp.MustCompile(`^#+\s*`)
)

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
				// `key: []` is the explicit empty form; a bare `key:` opens a list.
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
		if strings.HasPrefix(ln, "## ") {
			t := strings.TrimSpace(ln)
			inFiles = t == "## FILES"
			inSeeAlso = t == "## SEE ALSO"
		}
		if strings.HasPrefix(ln, "```") {
			inFence = !inFence
			continue
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
			} else {
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
		if inSeeAlso && sectLinkRe.MatchString(ln) {
			a.HasSeeAlsoSectionLink = true
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

// HasOutboundEdge reports whether the article points OUT at another article.
//
// Both forms count: the frontmatter `see_also:` is the typed edge this checker
// validates, the `## SEE ALSO` section is the one a reader actually follows.
func (a *Article) HasOutboundEdge() bool {
	return len(a.SeeAlso) > 0 || a.HasSeeAlsoSectionLink
}

func putIfAbsent(m map[string]int, k string, v int) {
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}
