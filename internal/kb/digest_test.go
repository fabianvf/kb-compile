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
