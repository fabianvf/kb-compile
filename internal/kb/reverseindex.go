package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// The reverse index is DERIVED from the KB, so stale means wrong and it may be
// regenerated freely — by a pre-commit hook, by CI, by anyone. That is exactly
// the opposite of the freshness manifest beside it, which records a judgement.
// Getting the two confused is how a gate stops meaning anything: regenerate a
// judgement automatically and it passes always.

const indexWhat = "source path -> {articles that document it, tests that " +
	"exercise it}. Answers \"I touched this file: what do I read, and where " +
	"do the test updates go?\" `tests` is derived from real imports and exact " +
	"stem reconstruction, never filename similarity. `articleTests` lists the " +
	"subsystem-level guards each article names, which no single file imports. " +
	"Generated: do not hand-edit."

// BuildReverseIndex assembles the payload. Column order inside each entry is
// articles, tests, then each configured extra kind in config order.
func (c *Checker) BuildReverseIndex(
	ownership, testLinks map[string][]string,
	extra map[string]map[string][]string,
) map[string]any {
	paths := map[string]bool{}
	for p := range ownership {
		paths[p] = true
	}
	for p := range testLinks {
		paths[p] = true
	}
	for _, kind := range c.Cfg.ExtraLinkKinds {
		for p := range extra[kind.Name] {
			paths[p] = true
		}
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		if !c.Cfg.IsTestFile(p) {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)

	owners := newOrdered()
	for _, p := range keys {
		e := newOrdered()
		e.set("articles", orEmpty(ownership[p]))
		e.set("tests", orEmpty(testLinks[p]))
		for _, kind := range c.Cfg.ExtraLinkKinds {
			e.set(kind.Name, orEmpty(extra[kind.Name][p]))
		}
		owners.set(p, e)
	}

	// Per-article: the tests that article itself NAMES. These are the
	// subsystem-level guards — contract tests, integration phases — that no
	// single source file imports, so they are one hop further out than the
	// per-file list and cannot be derived from it.
	byBasename := map[string][]string{}
	for _, f := range c.Tracked {
		b := f[strings.LastIndex(f, "/")+1:]
		byBasename[b] = append(byBasename[b], f)
	}
	articleTests := newOrdered()
	for _, id := range c.sortedIDs() {
		a := c.Articles[id]
		named := map[string]bool{}
		cands := map[string]bool{}
		for p := range a.FilesBlock {
			cands[p] = true
		}
		for p := range a.CitedPaths {
			cands[p] = true
		}
		for p := range cands {
			if !c.Cfg.IsTestFile(p) {
				continue
			}
			b := p[strings.LastIndex(p, "/")+1:]
			if contains(byBasename[b], p) {
				named[p] = true
				continue
			}
			// Prose cites test files by bare name. Resolve it only when
			// exactly one tracked file carries that basename, so an ambiguous
			// name is dropped rather than guessed.
			if !strings.Contains(p, "/") && len(byBasename[p]) == 1 {
				named[byBasename[p][0]] = true
			}
		}
		if len(named) > 0 {
			list := make([]string, 0, len(named))
			for p := range named {
				list = append(list, p)
			}
			sort.Strings(list)
			articleTests.set(id, list)
		}
	}

	root := newOrdered()
	root.set("_generated_by", "kb graph --write")
	root.set("_what", indexWhat)
	root.set("owners", owners)
	root.set("articleTests", articleTests)
	return map[string]any{"root": root}
}

// EncodeIndex renders a payload exactly as it is written to disk, so a test
// can compare against a committed golden without reimplementing the encoding
// — which would let the two drift and prove nothing.
func EncodeIndex(payload map[string]any) ([]byte, error) {
	return encodeIndented(payload["root"])
}

// CheckReverseIndex writes or verifies the generated index.
func (c *Checker) CheckReverseIndex(payload map[string]any, write bool) {
	encoded, err := EncodeIndex(payload)
	if err != nil {
		c.errf("encoding reverse index: %v", err)
		return
	}
	path := c.Cfg.ReverseIndexPath()
	if write {
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			c.errf("writing %s: %v", path, err)
			return
		}
		fmt.Printf("kb graph: wrote %s.\n", path)
		return
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		c.errf("%s is missing. Run `kb graph --write`.", path)
		return
	}
	want := strings.TrimSpace(string(encoded))
	have := strings.TrimSpace(string(onDisk))
	if want == have {
		return
	}
	// Say WHAT is stale. A bare "stale" is useless when the committed index
	// and the regenerated one differ only on a machine you cannot inspect —
	// which is exactly how this first surfaced: green locally, red in CI.
	wl, hl := strings.Split(want, "\n"), strings.Split(have, "\n")
	var diffs []string
	for i := 0; i < len(wl) || i < len(hl); i++ {
		w, h := "<end of file>", "<end of file>"
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(hl) {
			h = hl[i]
		}
		if w == h {
			continue
		}
		diffs = append(diffs, fmt.Sprintf(
			"    line %d:\n      committed: %s\n      regenerated: %s", i+1, h, w))
		if len(diffs) == 5 {
			break
		}
	}
	c.errf("%s is stale (committed %d lines, regenerated %d). Run `kb graph "+
		"--write` and commit the result.\n  First differences:\n%s",
		path, len(hl), len(wl), strings.Join(diffs, "\n"))
}

// ── ordered JSON ───────────────────────────────────────────────────────────
//
// Go maps have no order and encoding/json sorts map keys, which is almost
// right but loses the deliberate `articles, tests, <extras>` column order
// inside each entry. ordered preserves insertion order.

type ordered struct {
	keys []string
	vals map[string]any
}

func newOrdered() *ordered { return &ordered{vals: map[string]any{}} }

func (o *ordered) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := marshalRaw(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshalRaw(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalRaw encodes without Go's default HTML escaping of < > &, which would
// mangle any path or prose containing them and has nothing to do with JSON.
func marshalRaw(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// encodeIndented renders with a single-space indent, matching the format the
// committed indexes were generated in.
func encodeIndented(v any) ([]byte, error) {
	raw, err := marshalRaw(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", " "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
