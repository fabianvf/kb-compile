package kb

// Sub-article retrieval.
//
// An agent that needs one invariant out of a 24k-token article pays 24k for
// it. The `§ SECTION` citation convention already existed as prose, pointing
// readers at one part of an article, but nothing could act on it: the only
// available operation was "open the file".
//
// `kb read <article>#SECTION` makes the citation executable and turns that
// 24k read into a few hundred tokens.

import (
	"fmt"
	"os"
	"strings"
)

// ReadArticle returns an article, or one section of it.
//
// A missing section lists what is available rather than failing bare. An agent
// that guessed a section name should be able to correct itself from the error
// without a second round trip to read the whole file, which would defeat the
// point of asking for a section.
func ReadArticle(kbDir, id, section string) (string, error) {
	path := kbDir + "/" + id + ".md"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no article %q in %s", id, kbDir)
	}
	if section == "" {
		return string(data), nil
	}

	lines := strings.Split(string(data), "\n")
	var available []string
	var out []string
	want := strings.ToUpper(strings.TrimSpace(section))
	in := false

	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			h := strings.TrimSpace(strings.TrimPrefix(ln, "## "))
			available = append(available, h)
			// Prefix match, so `#INVARIANT` finds `## INVARIANTS` and a
			// citation written against a since-expanded heading still lands.
			hu := strings.ToUpper(h)
			in = hu == want || strings.HasPrefix(hu, want) || strings.HasPrefix(want, hu)
			if in {
				out = append(out, ln)
			}
			continue
		}
		if in {
			out = append(out, ln)
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("article %q has no section %q. It has: %s",
			id, section, strings.Join(available, ", "))
	}
	return strings.Join(out, "\n"), nil
}
