package kb

import (
	"fmt"
	"sort"
	"strings"
)

// Owners answers "I am about to change this file — what do I need to know?"
//
// The reverse index has always held this, and nothing consumed it. Two agents
// handed a bug report in a repository whose AGENTS.md tells them to look a file
// up before changing it went straight to grep instead, and neither opened the
// KB at all. A knowledge base that is accurate, cheap to query and never
// queried is worth nothing, so the lookup has to be one command rather than a
// jq expression against a generated file.
//
// Deliberately prints the INVARIANTs and GOTCHAs and not the whole article.
// The question at the moment of editing is "what will I break", and an answer
// long enough to skim is an answer that gets skimmed.
func (c *Checker) Owners(ownership map[string][]string, paths []string) string {
	digests := map[string]ArticleDigest{}
	for _, d := range c.BuildDigest() {
		digests[d.ID] = d
	}

	var b strings.Builder
	for _, p := range paths {
		arts := ownership[p]
		if len(arts) == 0 {
			fmt.Fprintf(&b, "%s\n  no article owns this file.\n", p)
			if c.Cfg.IsProductionSource(p) {
				b.WriteString("  It is production source, so it is either in " +
					"the orphan baseline or about to fail the ratchet.\n")
			}
			continue
		}
		fmt.Fprintf(&b, "%s\n", p)
		for _, id := range arts {
			fmt.Fprintf(&b, "  %s  (%s/%s.md)\n", id, c.Cfg.KBDir, id)
			d, ok := digests[id]
			if !ok {
				continue
			}
			// The two sections that say what breaks. DECISION explains why the
			// code is shaped as it is, which is worth reading but is not what
			// stops you making a mistake in the next thirty seconds.
			for _, sec := range []string{"INVARIANT", "GOTCHA"} {
				for _, claim := range d.Claims[sec] {
					fmt.Fprintf(&b, "    %s: %s\n", sec, claim)
				}
				if n := d.Extra[sec]; n > 0 {
					fmt.Fprintf(&b, "    %s: (+%d more — kb read %s#%s)\n",
						sec, n, id, sec)
				}
			}
			if len(d.SeeAlso) > 0 {
				sort.Strings(d.SeeAlso)
				fmt.Fprintf(&b, "    -> %s\n", strings.Join(d.SeeAlso, ", "))
			}
		}
	}
	return b.String()
}
