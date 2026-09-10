package kb

// Does the KB carve the system the way the system actually changes?
//
// Everything else here checks that the KB is STRUCTURALLY sound and that
// somebody looked at it. Neither says whether it helps. This does, for the one
// property that can be measured without a model in the loop.
//
// The premise: an agent about to change a file looks it up, gets the owning
// articles, and reads them. If the KB's boundaries are right, those articles
// also describe the OTHER files that change alongside it. Git history says
// which files those are, so the question is answerable from data the repo
// already has:
//
//	for each historical commit touching 2+ KB-owned files,
//	  pick one file, take the articles that own it,
//	  and ask how much of the rest of the commit those articles also own.
//
// RECALL is that fraction: did reading the article tell you about the rest of
// the change?
//
// Recall alone is gamed by one article owning the repo, which scores 1.0 and
// routes nobody, so it is reported against two things that bound it. REACH is
// how many files those articles own, which is the reading cost the recall was
// bought with. And the DIRECTORY BASELINE is the same measurement with each
// directory treated as an article: it is what the repo's own structure already
// tells you for free.
//
// The baseline is the number that makes the rest interpretable. A KB scoring
// at or below it has not carved anything the tree did not already carve, and
// the articles are filing rather than describing. Without a control, "recall
// 0.57" is not a result.
//
// What this cannot measure is whether an article's PROSE is any good. A KB of
// perfectly-bounded articles full of fluent restatements of the code scores
// well here. This measures the carve, not the content, and saying so is part
// of reporting it honestly.

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// EvalResult is the outcome over one repository's history.
type EvalResult struct {
	Commits int
	Files   int
	Recall  float64
	Reach   float64 // mean files reachable from one entry point
	BaseR   float64 // same recall, with directories as articles
	BaseCh  float64 // mean files in the entry file's directory
	Worst   []EvalCase
	Unowned int
}

// EvalCase is one commit whose files the KB scattered across articles.
type EvalCase struct {
	SHA     string
	Subject string
	Recall  float64
	Files   []string
	Missed  []string
}

// Evaluate walks history and scores the article boundaries.
//
// maxCommits bounds the walk; maxFiles drops the sweeping commits (a
// dependency bump, a formatting pass) that no article structure could or
// should predict.
func (c *Checker) Evaluate(ownership map[string][]string, maxCommits, maxFiles int) (*EvalResult, error) {
	// article -> everything it owns, so "what else would I have read about"
	// is a lookup.
	owns := map[string]map[string]bool{}
	for f, arts := range ownership {
		for _, a := range arts {
			if owns[a] == nil {
				owns[a] = map[string]bool{}
			}
			owns[a][f] = true
		}
	}

	commits, err := commitFileSets(maxCommits)
	if err != nil {
		return nil, err
	}

	// The control: each directory treated as an article. Anything the KB
	// scores above this is boundary information the tree did not already
	// carry.
	byDir := map[string]map[string]bool{}
	for f := range ownership {
		d := dirOf(f)
		if byDir[d] == nil {
			byDir[d] = map[string]bool{}
		}
		byDir[d][f] = true
	}

	res := &EvalResult{}
	var sumR, sumReach, sumBaseR, sumBaseCh float64
	for _, cm := range commits {
		var owned []string
		for _, f := range cm.files {
			if !c.Cfg.IsProductionSource(f) {
				continue
			}
			if len(ownership[f]) == 0 {
				res.Unowned++
				continue
			}
			owned = append(owned, f)
		}
		if len(owned) < 2 || len(owned) > maxFiles {
			continue
		}

		// Average over every choice of entry file, rather than picking one.
		// Which file an agent happens to touch first is arbitrary, and a KB
		// should not score differently depending on that.
		var cR, cReach, cBaseR, cBaseCh float64
		worstR, worstMissed := 2.0, []string(nil)
		target := map[string]bool{}
		for _, f := range owned {
			target[f] = true
		}
		for _, entry := range owned {
			reach := map[string]bool{}
			for _, a := range ownership[entry] {
				for f := range owns[a] {
					reach[f] = true
				}
			}
			hit, missed := 0, []string(nil)
			for f := range target {
				if reach[f] {
					hit++
				} else {
					missed = append(missed, f)
				}
			}
			r := float64(hit) / float64(len(target))
			cR += r
			cReach += float64(len(reach))

			// Same question, asked of the directory the file lives in.
			dir := byDir[dirOf(entry)]
			bh := 0
			for f := range target {
				if dir[f] {
					bh++
				}
			}
			cBaseR += float64(bh) / float64(len(target))
			cBaseCh += float64(len(dir))

			if r < worstR {
				sort.Strings(missed)
				worstR, worstMissed = r, missed
			}
		}
		n := float64(len(owned))
		cR, cReach = cR/n, cReach/n
		cBaseR, cBaseCh = cBaseR/n, cBaseCh/n
		sumR += cR
		sumReach += cReach
		sumBaseR += cBaseR
		sumBaseCh += cBaseCh
		res.Commits++
		res.Files += len(owned)

		if worstR < 1.0 {
			sort.Strings(owned)
			res.Worst = append(res.Worst, EvalCase{
				SHA: cm.sha[:8], Subject: cm.subject, Recall: cR,
				Files: owned, Missed: worstMissed,
			})
		}
	}
	if res.Commits == 0 {
		return res, nil
	}
	n := float64(res.Commits)
	res.Recall = sumR / n
	res.Reach = sumReach / n
	res.BaseR = sumBaseR / n
	res.BaseCh = sumBaseCh / n
	sort.Slice(res.Worst, func(i, j int) bool {
		if res.Worst[i].Recall != res.Worst[j].Recall {
			return res.Worst[i].Recall < res.Worst[j].Recall
		}
		return res.Worst[i].SHA < res.Worst[j].SHA
	})
	return res, nil
}

type commitFiles struct {
	sha, subject string
	files        []string
}

func commitFileSets(max int) ([]commitFiles, error) {
	out, err := exec.Command("git", "log", "--no-merges",
		fmt.Sprintf("-%d", max), "--format=%H%x1f%s", "--name-only").Output()
	if err != nil {
		return nil, fmt.Errorf("git log failed: %w", err)
	}
	var commits []commitFiles
	var cur *commitFiles
	for _, ln := range strings.Split(string(out), "\n") {
		if sha, subj, ok := strings.Cut(ln, "\x1f"); ok {
			if cur != nil {
				commits = append(commits, *cur)
			}
			cur = &commitFiles{sha: sha, subject: subj}
			continue
		}
		if ln = strings.TrimSpace(ln); ln != "" && cur != nil {
			cur.files = append(cur.files, ln)
		}
	}
	if cur != nil {
		commits = append(commits, *cur)
	}
	return commits, nil
}

// Render reports the score and the boundaries most worth revisiting.
func (r *EvalResult) Render(worstN int) string {
	var b strings.Builder
	b.WriteString("kb eval: do the article boundaries match how the code changes?\n\n")
	if r.Commits == 0 {
		b.WriteString("  No commit in range touched 2+ KB-owned files, so there\n" +
			"  is nothing to score. Widen --commits, or the KB owns too little\n" +
			"  of the repo for this to say anything yet.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %d commits scored, %d file touches\n\n", r.Commits, r.Files)
	fmt.Fprintf(&b, "  KB          recall %.2f   reading %.0f files on average\n",
		r.Recall, r.Reach)
	fmt.Fprintf(&b, "  directories recall %.2f   reading %.0f files on average\n\n",
		r.BaseR, r.BaseCh)

	delta := r.Recall - r.BaseR
	switch {
	case delta > 0.10:
		fmt.Fprintf(&b, "  The KB recalls %.0f points more of each change than the\n"+
			"  directory structure does. The articles are carrying boundary\n"+
			"  information the tree does not.\n\n", delta*100)
	case delta < -0.02:
		fmt.Fprintf(&b, "  The KB recalls LESS than plain directories (%.0f points).\n"+
			"  Its boundaries are cutting across the way the code changes, so\n"+
			"  an agent would do better reading the folder.\n\n", -delta*100)
	default:
		b.WriteString("  The KB scores about the same as plain directory structure.\n" +
			"  Its articles are filing rather than describing: an agent gets\n" +
			"  the same routing from `ls`. Look at the worst cases below for\n" +
			"  the merges that would change this.\n\n")
	}
	b.WriteString("  Recall alone is gamed by one article owning the repo, which\n" +
		"  scores 1.00 and routes nobody. Read it against the reach column:\n" +
		"  higher recall for fewer files read is the actual goal.\n\n")

	if r.Unowned > 0 {
		fmt.Fprintf(&b, "  %d file touches were in production source with no owning\n"+
			"  article, and are excluded. A large number here means the score\n"+
			"  describes a small documented corner rather than the repo.\n\n", r.Unowned)
	}

	if len(r.Worst) > 0 {
		n := worstN
		if n > len(r.Worst) {
			n = len(r.Worst)
		}
		b.WriteString("  Worst-scoring changes. Each is a set of files that move\n" +
			"  together and that no single article describes, which is the\n" +
			"  signal for a merge:\n\n")
		for _, w := range r.Worst[:n] {
			fmt.Fprintf(&b, "    %.2f  %s  %s\n", w.Recall, w.SHA, w.Subject)
			for _, f := range w.Missed {
				fmt.Fprintf(&b, "            not reachable: %s\n", f)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("  This measures the CARVE, not the content. A KB of\n" +
		"  perfectly-bounded articles full of restated code scores well here.\n")
	return b.String()
}
