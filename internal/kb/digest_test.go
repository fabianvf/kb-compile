package kb

import (
	"os"
	"strings"
	"testing"
)

// TestExtractClaimsHandlesAllThreeShapes pins the parser against the shapes
// real articles use.
//
// The plain-bullet case is here because it shipped broken: reading only
// paragraph starts captured the first bullet of a list and nothing else, with
// its "- " marker attached. A digest that silently drops most of a section is
// worse than no digest, because it reads as "this article says little".
func TestExtractClaimsHandlesAllThreeShapes(t *testing.T) {
	body := "## INVARIANT\n\n" +
		"- **Scores are server-side.** A client recompute drifts across timezones.\n" +
		"- **Clamping happens once.** In util.clamp, nowhere else.\n" +
		"\n## GOTCHA\n\n" +
		"- style.display not hidden: Tailwind loses on specificity\n" +
		"- Duplicate testids: the table and the cards both render\n" +
		"\n## DECISION\n\n" +
		"We use C. A breaks caching. B cannot express partial periods.\n" +
		"\nSecond paragraph leads a second decision. With more after it.\n"

	inv := extractClaims(body, "INVARIANT")
	if len(inv) != 2 || !strings.HasPrefix(inv[0], "Scores are server-side.") {
		t.Errorf("bold bullets: got %d claims %v", len(inv), inv)
	}

	got := extractClaims(body, "GOTCHA")
	if len(got) != 2 {
		t.Fatalf("plain bullets: got %d claims, want 2: %v", len(got), got)
	}
	for _, c := range got {
		if strings.HasPrefix(c, "-") {
			t.Errorf("a list marker leaked into the claim: %q", c)
		}
	}

	dec := extractClaims(body, "DECISION")
	if len(dec) != 2 {
		t.Errorf("paragraphs: got %d claims, want 2: %v", len(dec), dec)
	}
	if dec[0] != "We use C." {
		t.Errorf("paragraph claim = %q, want the first sentence only", dec[0])
	}
}

// TestEditorialAsidesAreSkipped keeps meta-notes out of the routing table.
// "(the decisions above are not repeated here)" costs budget and routes nobody.
func TestEditorialAsidesAreSkipped(t *testing.T) {
	body := "## DECISION\n\n" +
		"(Decisions restated in the prose above are not repeated here.)\n\n" +
		"We use C because A breaks caching.\n"
	got := extractClaims(body, "DECISION")
	if len(got) != 1 || !strings.HasPrefix(got[0], "We use C") {
		t.Errorf("got %v, want only the substantive claim", got)
	}
}

// TestDigestIsSmallerThanTheCorpus is the whole premise, asserted rather than
// assumed. If the digest is not dramatically cheaper than the articles, it has
// no reason to exist.
func TestDigestIsSmallerThanTheCorpus(t *testing.T) {
	var ds []ArticleDigest
	corpus := 0
	for i := 0; i < 30; i++ {
		d := ArticleDigest{
			ID: "arch-thing", Type: "arch", Tokens: 6000,
			Covers: []string{"src/**"},
			Claims: map[string][]string{
				"INVARIANT": {strings.Repeat("a claim about the system ", 5)},
				"GOTCHA":    {strings.Repeat("a trap that was hit ", 5)},
			},
		}
		ds = append(ds, d)
		corpus += d.Tokens
	}
	got := EstimateTokens(len(RenderDigest("docs/kb", ds)))
	if got > corpus/10 {
		t.Errorf("digest is %d tokens against a %d-token corpus; it must be an "+
			"order of magnitude cheaper or there is no point reading it first",
			got, corpus)
	}
}

// TestReadArticleSection pins sub-article retrieval, including the part that
// makes a wrong guess cheap: the error lists what is actually there, so an
// agent corrects itself without falling back to reading the whole file.
func TestReadArticleSection(t *testing.T) {
	dir := t.TempDir()
	body := "---\nid: arch-x\n---\n# X\n\n## FILES\n\nsrc/a.go\n\n" +
		"## INVARIANT\n\nThe thing holds.\n\n## SEE ALSO\n\n- [y](arch-y.md)\n"
	if err := writeFile(dir+"/arch-x.md", body); err != nil {
		t.Fatal(err)
	}

	sec, err := ReadArticle(dir, "arch-x", "INVARIANT")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sec, "The thing holds.") {
		t.Errorf("section body missing: %q", sec)
	}
	if strings.Contains(sec, "src/a.go") || strings.Contains(sec, "arch-y") {
		t.Errorf("section bled into its neighbours: %q", sec)
	}

	full, err := ReadArticle(dir, "arch-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= len(sec) {
		t.Error("the whole article was not larger than one section of it")
	}

	_, err = ReadArticle(dir, "arch-x", "INVARIENT")
	if err == nil {
		t.Fatal("a misspelled section was accepted")
	}
	for _, want := range []string{"INVARIANT", "FILES", "SEE ALSO"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not list %q, so a wrong guess costs a "+
				"second round trip: %v", want, err)
		}
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

// A bold lead that wraps is still one claim.
//
// The single-line case was fixed by taking firstSentence of the joined groups.
// This is the other half and it failed worse: an unterminated `**` matched
// neither claim pattern, so the line fell through to the paragraph branch and
// became a claim cut at the physical line end. Four claims in this repo's own
// DIGEST were truncated that way, in the routing table agents read to decide
// what to open, which is the one artifact where a confident half-sentence
// costs a wrong choice.
func TestExtractClaimsJoinsAWrappedBoldLead(t *testing.T) {
	body := "## INVARIANT\n\n" +
		"**The digest is DERIVED, and that is what makes it trustworthy as a\n" +
		"routing table.** A hand-maintained summary drifts.\n"

	got := extractClaims(body, "INVARIANT")
	want := "The digest is DERIVED, and that is what makes it trustworthy as a routing table."
	if len(got) != 1 || got[0] != want {
		t.Errorf("extractClaims = %q, want [%q]", got, want)
	}
}

// The wrap must not swallow what follows it. A claim that ran past a blank
// line would eat the rest of the section, turning one malformed article into
// a digest entry nobody can read.
func TestExtractClaimsStopsAWrappedLeadAtABoundary(t *testing.T) {
	// What must not happen is one claim spanning the boundary. Text after it
	// being picked up as its OWN claim is correct and unrelated.
	for name, tc := range map[string]struct{ body, beyond string }{
		// The later paragraph carries its own `**` on purpose. Without it the
		// join finds no close, falls back to the single line, and the test
		// passes whether the blank-line boundary is there or not.
		"blank line": {
			"## INVARIANT\n\n**Never closed\n\nA separate **paragraph**.\n",
			"separate",
		},
		"next heading": {
			"## INVARIANT\n\n**Never closed\n## GOTCHA\n\n**Closed here.** trailing\n",
			"Closed here",
		},
		"fence": {
			"## INVARIANT\n\n**Never closed\n```\n**not a claim**\n```\n",
			"not a claim",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := extractClaims(tc.body, "INVARIANT")
			if len(got) == 0 {
				t.Fatal("the unterminated lead vanished entirely")
			}
			if strings.Contains(got[0], tc.beyond) {
				t.Errorf("the wrapped lead swallowed what follows it: %q", got[0])
			}
			if got[0] != "Never closed" {
				t.Errorf("claim = %q, want %q", got[0], "Never closed")
			}
		})
	}
}

// A single-line bold lead must keep working exactly as before.
func TestExtractClaimsStillHandlesTheUnwrappedCase(t *testing.T) {
	body := "## GOTCHA\n\n**One line.** And the explanation after it.\n"
	got := extractClaims(body, "GOTCHA")
	if len(got) != 1 || got[0] != "One line." {
		t.Errorf("extractClaims = %q, want [\"One line.\"]", got)
	}
}

// An unterminated `**` in a bullet list must not eat the bullets under it.
//
// `- **Subject.** explanation` is this KB's dominant claim shape, so the join
// runs inside lists constantly. The boundary set originally stopped at a blank
// line, a heading and a fence but not at the next list item, which made one
// malformed bullet consume the following ones into its own claim AND delete
// them as claims of their own: four bullets came back as two, the first a
// run-on of the first two and the third gone entirely. That is strictly worse
// than the truncation the join was added to fix, and it is silent, because the
// digest is generated and byte-compared against itself.
func TestExtractClaimsWrapDoesNotSwallowLaterBullets(t *testing.T) {
	body := "## GOTCHA\n\n" +
		"- **kwargs is forwarded unchanged\n" +
		"- The second bullet says something else.\n" +
		"- Third bullet with a **bold** word.\n" +
		"- Fourth bullet.\n"

	got := extractClaims(body, "GOTCHA")
	want := []string{
		"kwargs is forwarded unchanged",
		"The second bullet says something else.",
		"Third bullet with a bold word.",
		"Fourth bullet.",
	}
	if len(got) != len(want) {
		t.Fatalf("extractClaims = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("claim %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The extractor already declines to read a `|` row as a claim, so the join
// must not read one as claim text either.
func TestExtractClaimsWrapStopsAtATableRow(t *testing.T) {
	body := "## GOTCHA\n\n**A lead that never closes\n| a | b |\n| c | **d** |\n"
	got := extractClaims(body, "GOTCHA")
	if len(got) != 1 || got[0] != "A lead that never closes" {
		t.Errorf("extractClaims = %q, want [\"A lead that never closes\"]", got)
	}
}

// The wrap detector and the claim extractor must agree on what a bold lead
// looks like. They did not: the extractor accepts `[-*]\s+`, while the
// detector hand-rolled the exact two-character forms "- " and "* ", so an
// alignment-padded or tab-indented bullet was a claim to one and not to the
// other. The line then fell through to the paragraph branch and was cut at
// the physical line end, which is the bug the join exists to prevent.
func TestExtractClaimsJoinsWhateverTheExtractorCallsABoldLead(t *testing.T) {
	for name, body := range map[string]string{
		"one space":  "## X\n\n- **A claim that wraps\n  onto here.** rest\n",
		"two spaces": "## X\n\n-  **A claim that wraps\n   onto here.** rest\n",
		"tab":        "## X\n\n-\t**A claim that wraps\n\tonto here.** rest\n",
		"star":       "## X\n\n* **A claim that wraps\n  onto here.** rest\n",
		"indented":   "## X\n\n  **A claim that wraps\n  onto here.** rest\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := extractClaims(body, "X")
			want := "A claim that wraps onto here."
			if len(got) != 1 || got[0] != want {
				t.Errorf("extractClaims = %q, want [%q]", got, want)
			}
		})
	}
}

// Every claim shape wraps, and for a long time every shape truncated at the
// wrap. The bold branch was fixed first and in isolation, which left the other
// two cutting the tail off their first sentence in the routing table - the
// same silent defect, in the two shapes nobody had looked at.
func TestExtractClaimsJoinsEveryWrappedShape(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"bold lead": {
			"## INVARIANT\n\n**A claim that wraps across\ntwo lines.** Explanation.\n",
			"A claim that wraps across two lines.",
		},
		"plain bullet": {
			"## INVARIANT\n\n- A plain bullet claim that wraps across\n  two lines here.\n",
			"A plain bullet claim that wraps across two lines here.",
		},
		"paragraph": {
			"## INVARIANT\n\nA paragraph claim that wraps across\ntwo lines here.\n",
			"A paragraph claim that wraps across two lines here.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := extractClaims(tc.body, "INVARIANT")
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("extractClaims = %q, want [%q]", got, tc.want)
			}
		})
	}
}

// Joining must not run past what starts something else, in every shape. A
// paragraph that swallowed the next heading would put a section title inside a
// claim; one that swallowed a fence would put code there.
func TestExtractClaimsWrapRespectsBoundariesInEveryShape(t *testing.T) {
	for name, tc := range map[string]struct{ body, beyond string }{
		"paragraph stops at heading": {
			"## INVARIANT\n\nA wrapped paragraph\n### Subhead\ntail\n", "Subhead",
		},
		"paragraph stops at fence": {
			"## INVARIANT\n\nA wrapped paragraph\n```\ncode()\n```\n", "code()",
		},
		"bullet stops at next bullet": {
			"## INVARIANT\n\n- First bullet wraps\n- Second bullet.\n", "Second bullet",
		},
		"paragraph stops at table": {
			"## INVARIANT\n\nA wrapped paragraph\n| a | b |\n", "| a |",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := extractClaims(tc.body, "INVARIANT")
			if len(got) == 0 {
				t.Fatal("the claim vanished")
			}
			if strings.Contains(got[0], tc.beyond) {
				t.Errorf("claim ran past its boundary: %q", got[0])
			}
		})
	}
}
