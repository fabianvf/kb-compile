package kb

// The digest exists because of an arithmetic problem.
//
// Measured on a real 35-article KB: the corpus is ~273k tokens, the median
// article ~6k, the largest ~24k. An agent cannot load the corpus. What it had
// instead was a hub file of links, which tells it every article's NAME and
// nothing about what any of them knows. So the cheapest way to answer "where
// does scoring happen" was to guess, open a 24k article, and sometimes guess
// again.
//
// The digest is the missing artifact: every article's identity, edges, size
// and headline claims, at roughly 150 tokens each. The whole KB becomes
// legible for ~5k instead of ~273k, and the agent opens exactly one article on
// purpose rather than two by trial.
//
// It is DERIVED, so it regenerates freely and can never disagree with the
// articles. That is the whole reason it can be trusted as a routing table:
// a hand-maintained summary would drift, and a drifted routing table sends
// agents confidently to the wrong place.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Sections whose contents are worth surfacing in the digest. These are the
// claims an agent is deciding between: what must stay true, what has bitten
// someone, and what was chosen on purpose. FILES and SEE ALSO are covered by
// the frontmatter lines, and STEPS/COMMANDS are only useful in full.
var digestSections = []string{"INVARIANT", "GOTCHA", "DECISION"}

// maxClaimsPerSection caps how much of a long section reaches the digest. An
// article with fifteen invariants would otherwise dominate, which defeats the
// point: the digest routes, it does not replace reading.
const maxClaimsPerSection = 3

// claimChars is the budget for one headline claim. Long enough to carry the
// subject and the consequence, short enough that thirty-five articles still
// fit in a few thousand tokens.
const claimChars = 150

var (
	boldClaimRe = regexp.MustCompile(`^\s*(?:[-*]\s+)?\*\*(.+?)\*\*\s*(.*)$`)
	listItemRe  = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	sentenceRe  = regexp.MustCompile(`^(.*?[.!?])(?:\s|$)`)
	inlineCode  = regexp.MustCompile("`([^`]*)`")
)

// EstimateTokens approximates what an article costs to read.
//
// Four characters per token is the usual rough heuristic and is wrong in the
// third significant figure, which does not matter: the decisions this informs
// are "is this 1k or 20k" and "should this article be split", and both are
// order-of-magnitude questions.
func EstimateTokens(n int) int { return n / 4 }

// ArticleDigest is one article's row.
type ArticleDigest struct {
	ID      string
	Type    string
	Tokens  int
	Covers  []string
	GatedBy []string
	SeeAlso []string
	Claims  map[string][]string // section -> headline claims
	Extra   map[string]int      // section -> claims omitted

	// Sections is the article's own heading list, filled in only when none of
	// the standard sections yielded a claim. An article using its own
	// vocabulary would otherwise appear in the digest as a name and nothing
	// else, which is worse than useless: it reads as "this article says
	// nothing" rather than "this digest cannot see inside it".
	Sections []string
}

// BuildDigest reads every article and extracts its routing information.
func (c *Checker) BuildDigest() []ArticleDigest {
	out := make([]ArticleDigest, 0, len(c.Articles))
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		data, err := os.ReadFile(a.Path)
		if err != nil {
			continue
		}
		d := ArticleDigest{
			ID: id, Type: a.DeclaredType, Tokens: EstimateTokens(len(data)),
			Covers: a.Covers, GatedBy: a.GatedBy, SeeAlso: a.SeeAlso,
			Claims: map[string][]string{}, Extra: map[string]int{},
		}
		// The `## SEE ALSO` section is an edge just as much as the frontmatter
		// key, and most articles use only one of the two. Merge them so the
		// digest shows the real out-edges rather than half of them.
		for target := range a.Links {
			t := strings.TrimSuffix(target, ".md")
			if t != id && t != "INDEX" && !contains(d.SeeAlso, t) {
				d.SeeAlso = append(d.SeeAlso, t)
			}
		}
		sort.Strings(d.SeeAlso)

		for _, sec := range digestSections {
			claims := extractClaims(string(data), sec)
			if len(claims) > maxClaimsPerSection {
				d.Extra[sec] = len(claims) - maxClaimsPerSection
				claims = claims[:maxClaimsPerSection]
			}
			if len(claims) > 0 {
				d.Claims[sec] = claims
			}
		}
		if len(d.Claims) == 0 {
			d.Sections = headings(string(data))
		}
		out = append(out, d)
	}
	return out
}

// extractClaims pulls the headline sentences out of one section.
//
// Three shapes are common and all are handled: `- **Subject.** explanation`
// bullets, where the bold lead is the claim; plain `- item` bullets, where
// each item is its own claim; and prose paragraphs, where the first sentence
// of each paragraph is.
//
// The plain-bullet case is not hypothetical. Reading only paragraph starts
// captured the FIRST bullet of a plain list and nothing else, with its "- "
// marker still attached, which is both wrong and obviously wrong once seen.
func extractClaims(body, section string) []string {
	lines := strings.Split(body, "\n")
	var claims []string
	in, paraStart := false, true

	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(ln, "## ")) == section
			paraStart = true
			continue
		}
		if !in {
			continue
		}
		if strings.TrimSpace(ln) == "" {
			paraStart = true
			continue
		}
		if strings.HasPrefix(ln, "```") || strings.HasPrefix(ln, "|") {
			continue
		}

		if m := boldClaimRe.FindStringSubmatch(ln); m != nil {
			claims = appendClaim(claims, clean(m[1]+" "+m[2]))
			paraStart = false
			continue
		}
		if m := listItemRe.FindStringSubmatch(ln); m != nil {
			claims = appendClaim(claims, clean(firstSentence(m[1])))
			paraStart = false
			continue
		}
		if paraStart {
			claims = appendClaim(claims, clean(firstSentence(ln)))
			paraStart = false
		}
	}
	return claims
}

// appendClaim adds a claim unless it is editorial rather than substantive.
//
// A parenthetical opening is almost always a note to the next writer ("(the
// decisions above are not repeated here)") rather than something an agent is
// choosing between. Those cost digest budget and route nobody.
func appendClaim(claims []string, s string) []string {
	if s == "" || strings.HasPrefix(s, "(") {
		return claims
	}
	return append(claims, truncate(s))
}

func firstSentence(s string) string {
	if m := sentenceRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// clean strips markdown that costs tokens without carrying meaning, but keeps
// backticked identifiers: a claim about `ProviderCondition` is useless without
// the name.
func clean(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "*", "")
	s = inlineCode.ReplaceAllString(s, "`$1`")
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string) string {
	if len(s) <= claimChars {
		return s
	}
	cut := strings.LastIndex(s[:claimChars], " ")
	if cut < claimChars/2 {
		cut = claimChars
	}
	return strings.TrimRight(s[:cut], " .,;:") + "..."
}

// RenderDigest writes the routing table an agent reads first.
func RenderDigest(kbDir string, ds []ArticleDigest) string {
	var body strings.Builder
	total := 0
	for _, d := range ds {
		total += d.Tokens
	}

	for _, d := range ds {
		typ := d.Type
		if typ == "" {
			typ = "article"
		}
		fmt.Fprintf(&body, "## %s\n\n`%s` · ~%s tokens\n\n", d.ID, typ, human(d.Tokens))
		if len(d.Covers) > 0 {
			fmt.Fprintf(&body, "- owns: %s\n", strings.Join(d.Covers, ", "))
		}
		if len(d.GatedBy) > 0 {
			fmt.Fprintf(&body, "- gated by: %s\n", strings.Join(d.GatedBy, ", "))
		}
		if len(d.SeeAlso) > 0 {
			fmt.Fprintf(&body, "- -> %s\n", strings.Join(d.SeeAlso, ", "))
		}
		body.WriteString("\n")
		for _, sec := range digestSections {
			for _, cl := range d.Claims[sec] {
				fmt.Fprintf(&body, "- **%s**: %s\n", sec, cl)
			}
			if n := d.Extra[sec]; n > 0 {
				fmt.Fprintf(&body, "- **%s**: (+%d more, in the article)\n", sec, n)
			}
		}
		if len(d.Sections) > 0 {
			fmt.Fprintf(&body, "- sections: %s\n", strings.Join(d.Sections, ", "))
		}
		body.WriteString("\n")
	}

	// The header quotes the digest's own size, so it can only be written once
	// the body exists. Computing it from an estimate produced a number that
	// was wrong by half, in the one place a reader is deciding whether to
	// trust the rest of the numbers.
	header := fmt.Sprintf("# KB digest\n\n"+
		"GENERATED by `kb graph --write`. Do not hand-edit.\n\n"+
		"Every article's identity, edges, size and headline claims. Read this "+
		"to decide what to open: it is ~%s tokens against ~%s for the corpus, "+
		"so guessing wrong here is cheap and guessing wrong there is not.\n\n"+
		"Then read the article, or one section of it:\n\n"+
		"    kb read <article>[#SECTION]\n\n"+
		"Sizes are estimates (4 chars/token). `->` is an outbound edge; "+
		"`gated by` must be read first.\n\n"+
		"%d articles, ~%s tokens total.\n\n---\n\n",
		human(EstimateTokens(len(body.String())+600)), human(total),
		len(ds), human(total))

	return header + body.String()
}

// headings lists the article's own `##` sections, minus the structural ones a
// reader already expects.
func headings(body string) []string {
	skip := map[string]bool{"FILES": true, "SEE ALSO": true}
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		if !strings.HasPrefix(ln, "## ") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(ln, "## "))
		if !skip[h] {
			out = append(out, h)
		}
	}
	return out
}

func human(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// CheckDigest writes or verifies the digest, exactly like the reverse index.
func (c *Checker) CheckDigest(ds []ArticleDigest, write bool) {
	path := c.Cfg.DigestPath()
	rendered := RenderDigest(c.Cfg.KBDir, ds)
	if write {
		if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
			c.errf("writing %s: %v", path, err)
			return
		}
		fmt.Printf("kb graph: wrote %s (%d articles, ~%s tokens).\n",
			path, len(ds), human(EstimateTokens(len(rendered))))
		return
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		c.errf("%s is missing. Run `kb graph --write`.", path)
		return
	}
	if strings.TrimSpace(string(onDisk)) != strings.TrimSpace(rendered) {
		c.errf("%s is stale. Run `kb graph --write` and commit the result. "+
			"It is the routing table agents read before choosing an article, "+
			"so a stale one sends them confidently to the wrong place.", path)
	}
}

// CheckArticleSize warns when an article has grown past the point where it
// gets read in full.
//
// Advisory, never fatal: the right length is a judgement, and a build that
// fails on prose length would just get the threshold raised. But an agent
// under context pressure reads the first third of a long article and silently
// misses the rest, so the author wants to hear about it at write time rather
// than at the next compression pass.
func (c *Checker) CheckArticleSize(ds []ArticleDigest) {
	budget := c.Cfg.MaxArticleTokens
	if budget <= 0 {
		return
	}
	for _, d := range ds {
		if d.Tokens > budget {
			c.warnf("%s/%s.md is ~%s tokens, over the %s budget. Long "+
				"articles get skimmed, and what is past the skim is "+
				"functionally not in the KB while still costing a review on "+
				"every compile. Consider splitting it, or raise "+
				"max_article_tokens if this one earns its length.",
				c.Cfg.KBDir, d.ID, human(d.Tokens), human(budget))
		}
	}
}
