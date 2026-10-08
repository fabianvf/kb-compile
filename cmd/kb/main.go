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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

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
  kb compiled [ARTICLE...]
                         Record that a compile just reviewed the current
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
  kb version             The release stamped in at build time, else the module
                         version Go embedded, else "dev". A version that cannot
                         identify a build on its own carries a hash of the
                         binary's own bytes.

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
// toolchain embeds. The second is what a `go install ...@<tag>` build has,
// and without it every such binary would claim to be a dev build.
func kbVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v + buildDigest(v)
		}
	}
	return "dev" + buildDigest("dev")
}

// buildDigest appends a short hash of the binary's own bytes to a version that
// cannot identify a build on its own.
//
// The Go toolchain derives `+dirty` from the presence of uncommitted changes,
// not from their content, so every build from the same working tree reports an
// identical string however much the tree moved in between. That makes "does
// this binary look stale?" unanswerable except by running it and recognising
// the behaviour you were trying to check for — which is only available after
// you have relied on it.
//
// Hashing the executable rather than the tree deliberately: it needs no
// cooperation from whoever built it, so a plain `go build` gets the same
// guarantee as a scripted one, and it answers the question actually being
// asked, which is whether these are the same bytes as last time.
func buildDigest(v string) string {
	if !strings.HasSuffix(v, "+dirty") && v != "dev" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "." + hex.EncodeToString(h.Sum(nil))[:12]
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

	// Production source git does not track yet is invisible to everything
	// below, because ownership is built from the tracked list. That is the
	// right scope for a gate and the wrong scope for the human: a new
	// subsystem is untracked until someone stages it, so its `covers:` glob
	// matches nothing and a compile marker records a narrower set than the
	// author believes. Warn, never fail — the tracked list still decides.
	//
	// Computed on demand, and only the three gates ask. `git ls-files
	// --others` stats the working tree, and `kb owners` runs from the prompt
	// hook on every message a human sends: making the one command advertised
	// as a cheap lookup walk the tree would be a real cost paid on the hot
	// path for an answer it never reads.
	//
	// A failure here is reported and then dropped, never fatal. This scan is
	// advisory by construction — arch-links.md states as an invariant that it
	// "feeds no ratchet and no ownership map, so it cannot turn a pass into a
	// fail or back" — and `die` broke exactly that: `git ls-files --others`
	// exits 128 on a `core.excludesFile` pointing at something unreadable,
	// which `git ls-files` alone tolerates, so `kb fresh` went from 0 to 2 and
	// the commit hook denied every commit in the checkout with "the knowledge
	// base is stale" over a git config error. An advisory that can fail the
	// gate is not an advisory.
	untrackedAll := sync.OnceValue(func() []string {
		u, err := repo.UntrackedSource(cfg.IsProductionSource)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kb: WARN  cannot list untracked files, so "+
				"the reports about them are omitted: %v\n", err)
			return nil
		}
		return u
	})
	warnUntracked := func() {
		if u := untrackedAll(); len(u) > 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"%d untracked production file(s) are outside every check "+
					"here, because ownership is built from what git tracks: "+
					"%s. `git add` them first, or they are not in the KB's "+
					"view.", len(u), briefly(u)))
		}
	}

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
		warnUntracked()
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
		// Untracked production source that no article claims is the one cell
		// nothing else covers: the orphan ratchet walks c.Tracked, so it
		// cannot see the file, and the ownership map cannot either. `kb graph`
		// warns about every untracked path, but `kb fresh` is the command
		// agents are instructed to run before committing, and this is the same
		// question it already exists to ask.
		//
		// It is also the normal birth condition of a subsystem rather than an
		// edge case: an article and the code it covers land in one commit, so
		// during the window when the code is untracked, no article claims it.
		var unclaimed []string
		for _, p := range untrackedAll() {
			owned := false
			for _, a := range c.Articles {
				if a.Owns(p) {
					owned = true
					break
				}
			}
			if !owned {
				unclaimed = append(unclaimed, p)
			}
		}
		runFresh(cfg, c.OwnedForFreshness(ownership), unclaimed)

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
		// Resolve the names BEFORE anything is printed. A bad name used to be
		// caught below, after the untracked warning had already gone to
		// stderr, so a refused command explained a record it never wrote.
		//
		// `flag` stops parsing at the first positional, so a flag typed after
		// an article name is not parsed and lands here instead. Left to the
		// generic path it became `no article "--root"`, which names neither
		// the problem nor the fix, AND the flag was silently ignored — for
		// `--root` that means the command ran against the wrong repository.
		named := map[string]bool{}
		var ids []string
		for _, raw := range fs.Args() {
			if strings.HasPrefix(raw, "-") {
				die("%q is a flag, and flags must come before the article "+
					"names: `kb compiled %s ARTICLE...`. Parsing stops at the "+
					"first article name, so a flag after one is not applied.",
					raw, raw)
			}
			id := strings.TrimSuffix(raw, ".md")
			if c.Articles[id] == nil {
				die("no article %q in %s. `kb compiled` with no arguments "+
					"records every KB-owned file.", id, cfg.KBDir)
			}
			if !named[id] {
				named[id] = true
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)

		// `kb graph` carries the same warning, but this is the command that
		// writes the narrowing down as a judgement, so it has to say so here
		// rather than rely on the author having run graph first.
		//
		// Scoped to the articles being recorded. Under `kb compiled arch-nas`,
		// an untracked path that arch-nas would not own belongs to somebody
		// else's work in progress: not reviewable, not actionable, and the
		// fastest way to teach a reader that this line can be skipped.
		//
		// A path NO article claims is reported whichever form is used. It is
		// nobody's work in progress by definition, it is the birth condition
		// of a new subsystem, and the scoped form is the one `kb fresh` tells
		// people to paste — so suppressing it there would hide the case the
		// warning exists for behind the most-recommended command.
		untracked := untrackedAll()
		if len(ids) > 0 {
			untracked = nil
			for _, p := range untrackedAll() {
				claimed, mine := false, false
				for _, a := range c.Articles {
					if !a.Owns(p) {
						continue
					}
					claimed = true
					if named[a.ID] {
						mine = true
						break
					}
				}
				if mine || !claimed {
					untracked = append(untracked, p)
				}
			}
		}
		if len(untracked) > 0 {
			fmt.Fprintf(os.Stderr, "kb compiled: WARN  %d untracked "+
				"production file(s) will not be recorded, because git does "+
				"not see them yet: %s.\n  \"Reviewed\" is about to mean "+
				"\"reviewed what git tracks\", which is narrower than you "+
				"think if this is a new subsystem.\n",
				len(untracked), briefly(untracked))
		}
		owned := c.OwnedForFreshness(ownership)
		stale, current, err := kb.Freshness(cfg.ManifestPath(), owned)
		if err != nil && !errors.Is(err, kb.ErrNoManifest) {
			die("%v", err)
		}
		// With article names, record only what those articles own. `kb fresh`
		// names articles one at a time, so the record has to be able to answer
		// at the same grain — otherwise an agent who reviewed three of five
		// can only stay silent, and silence is what a careless one emits too.
		//
		// A file is recordable only when EVERY article that owns it was named.
		// The manifest is keyed by path and nothing else, so writing a hash
		// attests that file to all of its owners at once: recording a file
		// shared with an article you did not name clears that article too.
		// That turned the honest act into the damaging one — `kb fresh` prints
		// the full list and says "drop the ones you did not read", and
		// dropping a co-owner was exactly what silently attested it. Held
		// back, the shared file stays stale and both articles stay red, which
		// is the truth: the only evidence either way is a file you have not
		// finished reviewing.
		if len(ids) > 0 {
			var files []string
			held := map[string][]string{}
			for f, arts := range ownership {
				mine := false
				var others []string
				for _, a := range arts {
					if named[a] {
						mine = true
					} else {
						others = append(others, a)
					}
				}
				if !mine {
					continue
				}
				if len(others) > 0 {
					sort.Strings(others)
					held[f] = others
					continue
				}
				files = append(files, f)
			}
			n, err := kb.MergeManifest(cfg.ManifestPath(), current, files)
			if err != nil {
				die("%v", err)
			}
			// Derived from the stale list already in hand rather than by
			// re-reading and re-hashing every KB-owned file. The second pass
			// also discarded its error, so a manifest that had become
			// unreadable printed "0 file(s) still unreviewed" — the one
			// sentence that reads as "you are done".
			recorded := make(map[string]bool, len(files))
			for _, f := range files {
				recorded[f] = true
			}
			remaining := 0
			for _, st := range stale {
				if !recorded[st.Path] {
					remaining++
				}
			}
			fmt.Printf("kb compiled: recorded %d file(s) for %s. %d file(s) "+
				"still unreviewed.\n", n, strings.Join(ids, ", "), remaining)

			// Only the stale ones. A shared file nobody has touched since the
			// last compile is held back too, but it needs no review and saying
			// so every time is how a real warning gets skipped.
			var blocked []string
			for _, st := range stale {
				if _, ok := held[st.Path]; ok {
					blocked = append(blocked, st.Path)
				}
			}
			if len(blocked) > 0 {
				sort.Strings(blocked)
				co := map[string]bool{}
				for _, p := range blocked {
					for _, a := range held[p] {
						co[a] = true
					}
				}
				var coIDs []string
				for a := range co {
					coIDs = append(coIDs, a)
				}
				sort.Strings(coIDs)
				fmt.Fprintf(os.Stderr, "kb compiled: HELD  %d changed file(s) "+
					"also belong to article(s) you did not name (%s), and the "+
					"manifest records a file for all of its owners at once: "+
					"%s.\n  Recording them would attest %s as well. Read those "+
					"too and name them, or leave these files stale.\n",
					len(blocked), strings.Join(coIDs, ", "), briefly(blocked),
					strings.Join(coIDs, ", "))
			}
			return
		}
		if err := kb.WriteManifest(cfg.ManifestPath(), current); err != nil {
			die("%v", err)
		}
		// The total is bookkeeping and reads as bookkeeping. What this form
		// actually asserts is that every stale article was reviewed, which is
		// the only part an agent can get wrong, so report that instead. One
		// article read and eight cleared is visible here or not at all: the
		// next `kb fresh` passes, which is the failure being described.
		if len(stale) == 0 {
			fmt.Printf("kb compiled: %s already matched all %d KB-owned "+
				"source file(s); nothing had changed to review.\n",
				cfg.ManifestPath(), len(current))
			return
		}
		arts := map[string]bool{}
		for _, st := range stale {
			for _, a := range st.Articles {
				arts[a] = true
			}
		}
		fmt.Printf("kb compiled: recorded %d KB-owned source file(s) in %s, "+
			"attesting that %d article(s) were reviewed against %d changed "+
			"file(s).\n", len(current), cfg.ManifestPath(), len(arts), len(stale))
		if len(arts) > 1 {
			fmt.Printf("  If you read only some of them, that record is " +
				"false for the rest. `kb compiled ARTICLE...` records just " +
				"the ones you read.\n")
		}
	}
}

func runFresh(cfg *config.Config, ownership map[string][]string, unclaimed []string) {
	stale, _, err := kb.Freshness(cfg.ManifestPath(), ownership)

	// Printed on every path that returns a verdict, the OK one included. A
	// fresh KB plus unclaimed untracked code is precisely the silent state:
	// everything the checker can see is in order, and a whole subsystem is
	// outside what it can see.
	//
	// Capped like the other two untracked reports rather than listed in full.
	// On the failure path this text is captured by the commit hook and handed
	// back as the deny reason, so an unstaged vendored tree would push the
	// actual verdict out of the window it lands in. The count stays exact.
	reportUnclaimed := func(w *os.File) {
		if len(unclaimed) == 0 {
			return
		}
		fmt.Fprintf(w, "\nAlso: %d untracked production file(s) that no "+
			"article claims:\n\n  %s\n", len(unclaimed), briefly(unclaimed))
		fmt.Fprintf(w, "\nInvisible to the ownership map and to the orphan "+
			"ratchet alike, because both are built from what git tracks. "+
			"These need an article, not a review.\n")
	}

	// Before the early exit, not after it. A missing manifest is first
	// adoption, which is simultaneously the likeliest moment for unstaged
	// subsystems and the moment the report used to be skipped — the caller
	// had already paid for the working-tree walk and the answer was dropped.
	if errors.Is(err, kb.ErrNoManifest) {
		fmt.Fprintf(os.Stderr,
			"kb fresh: FAIL — %s is missing.\n\nIt is the record of which "+
				"source content the KB has been checked against, so its "+
				"absence is not \"nothing to check\", it is \"nothing is "+
				"known\". Initialise it with `kb compiled` as part of a "+
				"compile pass.\n", cfg.ManifestPath())
		reportUnclaimed(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		die("%v", err)
	}

	if len(stale) == 0 {
		fmt.Printf("kb fresh: OK — every KB-owned source file matches the "+
			"content reviewed at the last compile (%d files).\n", len(ownership))
		reportUnclaimed(os.Stdout)
		return
	}
	if os.Getenv("KB_COMPILE_OK") == "1" {
		fmt.Printf("kb fresh: WAIVED via KB_COMPILE_OK (%d stale file(s)). "+
			"Use this for a revert or a pure rename; if you are typing it "+
			"often, the check is wrong and should be fixed.\n", len(stale))
		reportUnclaimed(os.Stdout)
		return
	}
	// Spell out the scoped command for exactly what was just listed. Bare
	// `kb compiled` is the shortest thing to type in the whole tool, so the
	// careless path was also the cheapest one. Pasting this line attests to
	// what you read; attesting to less means deleting article names from it,
	// which is an act you have to perform and watch yourself perform. Neither
	// is a correctness guarantee — it puts the cost on the wrong answer
	// instead of the right one.
	seen := map[string]bool{}
	var arts []string
	for _, st := range stale {
		for _, a := range st.Articles {
			if !seen[a] {
				seen[a] = true
				arts = append(arts, a)
			}
		}
	}
	sort.Strings(arts)
	fmt.Fprintf(os.Stderr,
		"kb fresh: %d KB-owned source file(s) have changed since the last "+
			"compile.\n\nRe-read these articles against these files:\n\n%s\n"+
			"When you have read them:\n\n  kb compiled %s\n\n"+
			"Drop the ones you did not read. Recording an article you did not "+
			"open is worse than leaving it stale, because the next agent gets "+
			"no warning where one was due.\n\n"+
			"A stale KB is worse than no KB: agents are told to read it first, "+
			"so they act on a confidently-wrong article instead of reading the "+
			"code.\n\nEscape hatch: KB_COMPILE_OK=1 for a revert or a pure "+
			"rename.\n",
		len(stale), kb.GroupByArticle(stale), strings.Join(arts, " "))
	reportUnclaimed(os.Stderr)
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

// briefly names a path list without letting a very dirty working tree bury
// the sentence around it. The count is always exact; only the names are cut.
func briefly(paths []string) string {
	const max = 8
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, and %d more",
		strings.Join(paths[:max], ", "), len(paths)-max)
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "kb: "+format+"\n", a...)
	os.Exit(2)
}
