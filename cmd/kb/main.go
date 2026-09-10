// Command kb enforces that a repository's LLM knowledge base stays true.
//
// Two gates, asking different questions:
//
//	kb graph      do the KB's edges RESOLVE, and is every source file owned?
//	kb fresh      has anyone LOOKED at the KB since the code moved?
//
// Failure output is written as INSTRUCTIONS, not diagnostics. Not every agent
// reading these messages will have loaded the KB's own conventions, so the
// error has to carry the fix.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fabianvf/kb-compile/internal/config"
	"github.com/fabianvf/kb-compile/internal/kb"
	"github.com/fabianvf/kb-compile/internal/repo"
)

const usage = `kb — keep an LLM knowledge base honest.

USAGE
  kb graph [--write]     Validate the KB graph; --write regenerates the
                         reverse index and the two ratchet baselines.
  kb fresh               Fail if KB-owned source has changed since the last
                         recorded compile.
  kb compiled            Record that a compile just reviewed the current
                         content. Never run this from a hook.
  kb version

FLAGS
  --config PATH          Config file (default ` + config.DefaultPath + `)
  --root PATH            Repository root (default: current directory)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath, "config file")
	root := fs.String("root", "", "repository root")
	write := fs.Bool("write", false, "regenerate derived artifacts")
	quiet := fs.Bool("quiet", false, "suppress the OK line")
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "version":
		fmt.Println("kb 0.1.0-dev")
		return
	case "graph", "fresh", "compiled":
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "kb: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if *root != "" {
		if err := os.Chdir(*root); err != nil {
			die("cannot enter --root: %v", err)
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			die("no config at %s. Run the kb-init skill to create one, or "+
				"pass --config.", *cfgPath)
		}
		die("%v", err)
	}

	tracked, err := repo.TrackedFiles(cfg.ExcludeSubstrings)
	if err != nil {
		die("%v", err)
	}

	articles, parseErrs, err := loadArticles(cfg)
	if err != nil {
		die("%v", err)
	}

	c := kb.New(cfg, articles, tracked)
	c.Errors = append(c.Errors, parseErrs...)

	// The graph pass always runs: `fresh` needs its ownership map, and
	// deriving that twice from two code paths is how the two gates would come
	// to disagree about what the KB owns.
	c.CheckFrontmatter()
	c.CheckArticleEdges()
	c.CheckFileEdges()
	c.CheckGlobsAreLive()
	c.CheckSectionAnchors()
	c.CheckIndex()
	ownership := c.BuildOwnership()

	switch cmd {
	case "graph":
		c.CheckDeadEndRatchet(*write)
		c.CheckOrphanRatchet(ownership, *write)
		payload := c.BuildReverseIndex(ownership, c.BuildTestLinks(), c.ExtraLinks())
		c.CheckReverseIndex(payload, *write)
		report(c, cfg, ownership, *quiet)

	case "fresh":
		// A broken graph makes the ownership map untrustworthy, and a
		// freshness verdict computed from a wrong ownership map is worse than
		// no verdict: it would report a clean KB while articles point nowhere.
		if len(c.Errors) > 0 {
			report(c, cfg, ownership, *quiet)
			return
		}
		runFresh(cfg, c.OwnedForFreshness(ownership))

	case "compiled":
		if len(c.Errors) > 0 {
			report(c, cfg, ownership, *quiet)
			return
		}
		_, current, err := kb.Freshness(cfg.ManifestPath(), c.OwnedForFreshness(ownership))
		if err != nil && !errors.Is(err, kb.ErrNoManifest) {
			die("%v", err)
		}
		if err := kb.WriteManifest(cfg.ManifestPath(), current); err != nil {
			die("%v", err)
		}
		fmt.Printf("kb compiled: recorded %d KB-owned source file(s) in %s.\n",
			len(current), cfg.ManifestPath())
	}
}

func runFresh(cfg *config.Config, ownership map[string][]string) {
	stale, _, err := kb.Freshness(cfg.ManifestPath(), ownership)
	if errors.Is(err, kb.ErrNoManifest) {
		fmt.Fprintf(os.Stderr,
			"kb fresh: FAIL — %s is missing.\n\nIt is the record of which "+
				"source content the KB has been checked against, so its "+
				"absence is not \"nothing to check\", it is \"nothing is "+
				"known\". Initialise it with `kb compiled` as part of a "+
				"compile pass.\n", cfg.ManifestPath())
		os.Exit(1)
	}
	if err != nil {
		die("%v", err)
	}
	if len(stale) == 0 {
		fmt.Printf("kb fresh: OK — every KB-owned source file matches the "+
			"content reviewed at the last compile (%d files).\n", len(ownership))
		return
	}
	if os.Getenv("KB_COMPILE_OK") == "1" {
		fmt.Printf("kb fresh: WAIVED via KB_COMPILE_OK (%d stale file(s)). "+
			"Use this for a revert or a pure rename; if you are typing it "+
			"often, the check is wrong and should be fixed.\n", len(stale))
		return
	}
	fmt.Fprintf(os.Stderr,
		"kb fresh: %d KB-owned source file(s) have changed since the last "+
			"compile.\n\nRe-read these articles against these files, then "+
			"record it with `kb compiled`:\n\n%s\n"+
			"A stale KB is worse than no KB: agents are told to read it first, "+
			"so they act on a confidently-wrong article instead of reading the "+
			"code.\n\nEscape hatch: KB_COMPILE_OK=1 for a revert or a pure "+
			"rename.\n",
		len(stale), kb.GroupByArticle(stale))
	os.Exit(1)
}

// loadArticles reads every .md in the KB dir except INDEX.md, which is the hub
// rather than an article and is validated separately.
func loadArticles(cfg *config.Config) (map[string]*kb.Article, []string, error) {
	entries, err := os.ReadDir(cfg.KBDir)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", cfg.KBDir, err)
	}
	var errs []string
	articles := map[string]*kb.Article{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		if id == "INDEX" {
			continue
		}
		a, err := kb.ParseArticle(cfg, filepath.Join(cfg.KBDir, name), id, &errs)
		if err != nil {
			return nil, nil, err
		}
		articles[id] = a
	}
	if len(articles) == 0 {
		return nil, nil, fmt.Errorf("no articles found in %s", cfg.KBDir)
	}
	return articles, errs, nil
}

func report(c *kb.Checker, cfg *config.Config, ownership map[string][]string, quiet bool) {
	sort.Strings(c.Warnings)
	for _, w := range c.Warnings {
		fmt.Printf("kb graph: WARN  %s\n", w)
	}
	if len(c.Errors) == 0 {
		if !quiet {
			fmt.Printf("kb graph: OK — %d articles, %d source files owned, "+
				"%d warning(s).\n", len(c.Articles), len(ownership), len(c.Warnings))
		}
		return
	}
	for _, e := range c.Errors {
		fmt.Fprintf(os.Stderr, "kb graph: %s\n", e)
	}
	fmt.Fprintf(os.Stderr, "\nkb graph: %d problem(s). The KB is a contract an "+
		"agent acts on — a dead edge silently drops context.\n", len(c.Errors))
	os.Exit(1)
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "kb: "+format+"\n", a...)
	os.Exit(2)
}
