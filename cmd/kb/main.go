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
	"runtime/debug"
	"sort"
	"strings"

	"github.com/fabianvf/kb-compile/internal/config"
	"github.com/fabianvf/kb-compile/internal/imports"
	"github.com/fabianvf/kb-compile/internal/kb"
	"github.com/fabianvf/kb-compile/internal/repo"
)

const usage = `kb — keep an LLM knowledge base honest.

USAGE
  kb graph [--write]     Validate the KB graph; --write regenerates the
                         reverse index and the two ratchet baselines.
  kb fresh               Fail if KB-owned source has changed since the last
                         recorded compile.
  kb compiled [ARTICLE...] Record that a compile just reviewed the current
                         content. With article names, records only what those
                         articles own, so reviewing three of five is sayable.
                         Never run this from a hook.
  kb read ID[#SECTION]   Print an article, or one section of it. Reading one
                         section of a large article costs a few hundred tokens
                         instead of tens of thousands.
  kb eval                Score the article boundaries against git history: do
                         the files that change together share an article?
  kb owners FILE...      What do I need to know before changing this file?
                         The owning articles and their invariants, from the
                         reverse index. One command instead of a jq expression.
  kb imports LANG        Print tab-separated test/source edges for a language,
                         by resolving its real import graph. Meant for a
                         link_commands entry rather than for reading.
  kb version

FLAGS
  --config PATH          Config file (default: ` + config.DefaultPath + `,
                         falling back to .yml or .json)
  --root PATH            Repository root (default: current directory)
  --init                 graph only, with --write: record a first ratchet
                         baseline that starts non-empty. Adoption only; it is
                         refused once a baseline exists, so it cannot widen
                         one.
`

// version is stamped by the release workflow with -ldflags. It is deliberately
// empty by default rather than a plausible-looking number: kb-init tells teams
// to pin the version, and a binary that reports one it cannot substantiate
// makes that instruction unfollowable.
var version = ""

// kbVersion prefers the stamped value, then the module version the Go
// toolchain embeds. The second is what a `go install ...@v0.2.0` build has,
// and without it every such binary would claim to be a dev build.
func kbVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "", "config file (default: .kb/config.yaml)")
	root := fs.String("root", "", "repository root")
	write := fs.Bool("write", false, "regenerate derived artifacts")
	ratchetInit := fs.Bool("init", false, "graph: with --write, record a first "+
		"ratchet baseline that starts non-empty")
	helpFlag := fs.Bool("help", false, "imports: print a resolver's notes")
	quiet := fs.Bool("quiet", false, "suppress the OK line")
	evalCommits := fs.Int("commits", 500, "eval: how much history to walk")
	evalMaxFiles := fs.Int("max-files", 15, "eval: skip commits touching more than this")
	evalWorst := fs.Int("worst", 5, "eval: how many low-scoring changes to show")
	_ = fs.Parse(os.Args[2:])

	// A bootstrap that silently does nothing is worse than one that refuses:
	// the adopter sees exit 0, assumes the baselines were recorded, and finds
	// out at the next commit. Both spellings that cannot bootstrap say so.
	if *ratchetInit {
		if cmd != "graph" {
			die("%s only applies to `kb graph`.", kb.BootstrapFlag)
		}
		if !*write {
			die("%s has no effect without --write: it relaxes what --write may "+
				"create, and on its own there is nothing to relax. Run "+
				"`kb graph --write %s`.", kb.BootstrapFlag, kb.BootstrapFlag)
		}
	}

	if cmd == "imports" {
		// `kb imports <lang> --help` prints the resolver's own notes, which
		// say what it resolves and which test_rules it assumes. kb-init tells
		// adopters to read that before configuring one, and the scripts live
		// inside this binary, so this is the only way to reach it.
		if fs.NArg() != 1 {
			die("usage: kb imports <%s> [--help]\n"+
				"  `kb imports <lang> --help` prints what that resolver does "+
				"and which test_rules it assumes.",
				strings.Join(imports.Names(), "|"))
		}
		if *helpFlag {
			h, err := imports.Header(fs.Arg(0))
			if err != nil {
				die("%v", err)
			}
			fmt.Println(h)
			return
		}
		// Deliberately ahead of config discovery. This reads the repository,
		// not the KB, and its first use is while writing the config that does
		// not exist yet.
		if err := imports.Run(fs.Arg(0), os.Stdout, os.Stderr); err != nil {
			die("%v", err)
		}
		return
	}

	switch cmd {
	case "version":
		fmt.Println("kb " + kbVersion())
		return
	case "graph", "fresh", "compiled", "read", "eval", "owners":
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

	path := *cfgPath
	if path == "" {
		var err error
		if path, err = config.Discover(); err != nil {
			if os.IsNotExist(err) {
				die("no config found (looked for %s). Run the kb-init "+
					"skill to create one, or pass --config.",
					strings.Join(config.SearchPaths, ", "))
			}
			die("%v", err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			die("no config at %s", path)
		}
		die("%v", err)
	}

	if cmd == "read" {
		if fs.NArg() != 1 {
			die("usage: kb read <article>[#SECTION]")
		}
		id, section, _ := strings.Cut(fs.Arg(0), "#")
		id = strings.TrimSuffix(id, ".md")
		text, err := kb.ReadArticle(cfg.KBDir, id, section)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(strings.TrimRight(text, "\n"))
		return
	}

	// The full tracked set, INCLUDING excluded directories.
	//
	// `exclude_dirs` means "these files need no article", not "these files do
	// not exist". Filtering here conflated the two, because this one set
	// answers both "what must be documented" and "is this cited path real".
	// On a repo excluding its test directories, 154 articles that cite a test
	// file from a VERIFY section failed with "not tracked by git" - the KB was
	// being told its own correct references were dangling.
	//
	// Ownership is filtered where ownership is built instead: the orphan
	// ratchet already goes through IsProductionSource, which applies the
	// exclusion, and BuildOwnership applies it to `covers:` globs.
	tracked, err := repo.TrackedFiles(nil)
	if err != nil {
		die("%v", err)
	}

	articles, parseErrs, err := loadArticles(cfg)
	if err != nil {
		die("%v", err)
	}

	c := kb.New(cfg, articles, tracked)
	c.AllowRatchetInit = *ratchetInit
	c.Errors = append(c.Errors, parseErrs...)

	// The graph pass always runs: `fresh` needs its ownership map, and
	// deriving that twice from two code paths is how the two gates would come
	// to disagree about what the KB owns.
	c.CheckFrontmatter()
	c.CheckArticleEdges()
	c.CheckFileEdges()
	c.CheckGlobsAreLive()
	c.CheckArticlesAreTracked()
	c.CheckSectionVocabulary()
	c.CheckSectionAnchors()
	c.CheckIndex()
	ownership := c.BuildOwnership()

	switch cmd {
	case "owners":
		if fs.NArg() == 0 {
			die("usage: kb owners <file>...")
		}
		fmt.Print(c.Owners(ownership, fs.Args()))

	case "graph":
		c.CheckDeadEndRatchet(*write)
		c.CheckOrphanRatchet(ownership, *write)
		testLinks := kb.MergeLinks(c.BuildTestLinks(), c.RunLinkCommands())
		payload := c.BuildReverseIndex(ownership, testLinks, c.ExtraLinks())
		c.CheckReverseIndex(payload, *write)
		digest := c.BuildDigest()
		c.CheckArticleSize(digest)
		c.CheckDigest(digest, *write)
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

	case "eval":
		if len(c.Errors) > 0 {
			report(c, cfg, ownership, *quiet)
			return
		}
		res, err := c.Evaluate(ownership, *evalCommits, *evalMaxFiles)
		if err != nil {
			die("%v", err)
		}
		fmt.Print(res.Render(*evalWorst))

	case "compiled":
		if len(c.Errors) > 0 {
			report(c, cfg, ownership, *quiet)
			return
		}
		owned := c.OwnedForFreshness(ownership)
		_, current, err := kb.Freshness(cfg.ManifestPath(), owned)
		if err != nil && !errors.Is(err, kb.ErrNoManifest) {
			die("%v", err)
		}
		// With article names, record only what those articles own. `kb fresh`
		// names articles one at a time, so the record has to be able to answer
		// at the same grain — otherwise an agent who reviewed three of five
		// can only stay silent, and silence is what a careless one emits too.
		if fs.NArg() > 0 {
			var files []string
			for _, id := range fs.Args() {
				id = strings.TrimSuffix(id, ".md")
				if c.Articles[id] == nil {
					die("no article %q in %s. `kb compiled` with no arguments "+
						"records every KB-owned file.", id, cfg.KBDir)
				}
				for f, arts := range ownership {
					for _, a := range arts {
						if a == id {
							files = append(files, f)
						}
					}
				}
			}
			n, err := kb.MergeManifest(cfg.ManifestPath(), current, files)
			if err != nil {
				die("%v", err)
			}
			stale, _, _ := kb.Freshness(cfg.ManifestPath(), owned)
			fmt.Printf("kb compiled: recorded %d file(s) for %s. %d file(s) "+
				"still unreviewed.\n", n, strings.Join(fs.Args(), ", "), len(stale))
			return
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
		// INDEX and DIGEST live in the KB directory and are not articles:
		// one is the hub, the other is generated from the articles.
		if id == "INDEX" || id == "DIGEST" {
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
