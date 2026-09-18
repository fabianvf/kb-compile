// Package imports ships the per-language import-graph resolvers inside the
// binary.
//
// They began as scripts in `contrib/`, which meant a repo adopting kb-compile
// had to copy one in and keep it up to date. That is backwards twice over: it
// puts this tool's code in someone else's repository, and it decouples the
// resolver from the checker that consumes its output, so a repo could sit on a
// year-old copy and see edges quietly differ from what the current kb expects.
// Embedded, the resolver is versioned with the binary that reads it, and CI
// needs no step to fetch it.
//
// They stay SHELL rather than being rewritten in Go. Each one is mostly a
// Python program, and Python is the one interpreter present on every machine
// that has the toolchains these resolve for. Rewriting six language resolvers
// in Go would be a rewrite of the part most likely to need per-repo tweaking,
// into the part hardest for an adopter to tweak.
package imports

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed scripts/*.sh
var scripts embed.FS

// Languages maps the name used on the command line to its script.
//
// The keys are what a config file writes, so they are the obvious word for the
// language rather than a file extension: `kb imports python`, not `kb imports
// py`. Aliases exist because "js" and "typescript" are the same resolver and
// guessing wrong should not be an error a reader has to look up.
var Languages = map[string]string{
	"go":         "go.sh",
	"python":     "py.sh",
	"typescript": "ts.sh",
	"javascript": "ts.sh",
	"java":       "java.sh",
	"dart":       "dart.sh",
	"rust":       "rs.sh",
}

// Names lists the languages, sorted, for help text and error messages.
func Names() []string {
	seen := map[string]bool{}
	var out []string
	for n := range Languages {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Script returns the shell source for a language.
func Script(lang string) ([]byte, error) {
	name, ok := Languages[strings.ToLower(lang)]
	if !ok {
		return nil, fmt.Errorf("no import resolver for %q. Available: %s.\n"+
			"A language not listed here is not a gap you have to wait on: a "+
			"link command is any program emitting `<test>\\t<source>` lines, "+
			"so point `command:` at your own script instead.",
			lang, strings.Join(Names(), ", "))
	}
	return scripts.ReadFile("scripts/" + name)
}

// Header returns the leading comment block of a language's script.
//
// kb-init tells an adopter to "check its header before wiring it in: each says
// which test_rules it assumes". Before this there was no way to do that — the
// scripts moved inside the binary, so the only copy of that text was in this
// tool's own source tree, which an adopting repository does not have.
func Header(lang string) (string, error) {
	src, err := Script(lang)
	if err != nil {
		return "", err
	}
	var out []string
	for _, ln := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(ln, "#!") {
			continue
		}
		if !strings.HasPrefix(ln, "#") {
			break // the comment block ends at the first line of shell
		}
		out = append(out, strings.TrimPrefix(strings.TrimPrefix(ln, "#"), " "))
	}
	return strings.Join(out, "\n"), nil
}

// Run executes the resolver for lang in the current directory, streaming its
// output to stdout and its diagnostics to stderr.
//
// Written to a temp file rather than piped to `sh -s`. Every script feeds a
// Python program to a heredoc, and a heredoc inside a script read from stdin
// competes with the interpreter for that same stream — the failure is a
// truncated program, which surfaces as a syntax error in generated code
// nobody can see.
func Run(lang string, stdout, stderr *os.File) error {
	src, err := Script(lang)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kb-imports")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "resolve.sh")
	if err := os.WriteFile(path, src, 0o700); err != nil {
		return err
	}
	cmd := exec.Command("sh", path)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}
