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
	want := strings.ToUpper(strings.TrimSpace(section))

	// Headings are collected in one pass so the match can be RESOLVED before
	// any text is emitted. Deciding per line meant every heading that matched
	// contributed its body, so `#I` silently returned INVARIANT and
	// IMPLEMENTATION glued together and the agent could not tell it was
	// reading two places at once.
	type heading struct {
		text  string
		start int
	}
	var heads []heading
	inFence := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "```") {
			inFence = !inFence
			continue
		}
		// A `## ` line inside a fenced block is an EXAMPLE. Treated as a real
		// heading it truncated the section actually being read at the fence,
		// and served the code block back as a section that does not exist.
		if inFence || !strings.HasPrefix(ln, "## ") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(ln, "## "))
		if h == "" {
			continue
		}
		available = append(available, h)
		heads = append(heads, heading{h, i})
	}

	// Exact first, then a unique prefix, so `#INVARIANT` still finds
	// `## INVARIANTS` but an ambiguous stem says so instead of concatenating.
	var hit []heading
	for _, h := range heads {
		if strings.EqualFold(h.text, want) {
			hit = []heading{h}
			break
		}
	}
	if len(hit) == 0 {
		for _, h := range heads {
			hu := strings.ToUpper(h.text)
			if strings.HasPrefix(hu, want) || strings.HasPrefix(want, hu) {
				hit = append(hit, h)
			}
		}
	}
	if len(hit) == 0 {
		return "", fmt.Errorf("article %q has no section %q. It has: %s",
			id, section, strings.Join(available, ", "))
	}
	if len(hit) > 1 {
		var names []string
		for _, h := range hit {
			names = append(names, h.text)
		}
		return "", fmt.Errorf("section %q is ambiguous in article %q: it "+
			"matches %s. Name one exactly.", section, id, strings.Join(names, ", "))
	}

	// To the next heading, or to EOF for the last section.
	end := len(lines)
	for _, h := range heads {
		if h.start > hit[0].start && h.start < end {
			end = h.start
		}
	}
	return strings.Join(lines[hit[0].start:end], "\n"), nil
}
