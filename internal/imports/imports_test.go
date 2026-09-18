package imports

// The import resolvers are the part of this tool most likely to rot quietly.
// They are shell and Python rather than Go, they only run in repositories that
// configure them, and their failure mode is an empty tests column — which
// reads as "this repo has no tests" rather than as a broken resolver. Each
// case below is a repository laid out the way that language actually lays
// repositories out, with the resolution rules that are easy to get wrong made
// explicit.
//
// The script under test is the EMBEDDED one, so a script that stops being
// embedded fails here rather than silently shipping a binary that cannot
// resolve anything.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type resolverCase struct {
	lang  string
	files map[string]string
	want  []string // "<test>\t<production>", sorted
	// notWant names edges the script must NOT produce. A link command that
	// over-reports is worse than one that under-reports: the reverse index is
	// what an agent reads to decide what to open, and noise there costs
	// context on every lookup.
	notWant []string
}

func TestEmbeddedResolvers(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; every resolver needs it")
	}

	cases := map[string]resolverCase{
		"python": {
			lang: "python",
			files: map[string]string{
				// A src/ layout, which is where the naive "path is the module
				// path" heuristic fails: the module is `app.core.engine`, not
				// `src.app.core.engine`.
				"src/app/__init__.py":         "",
				"src/app/core/__init__.py":    "",
				"src/app/core/engine.py":      "def run(): return 1\n",
				"src/app/core/util.py":        "def aid(): return 2\n",
				"src/app/core/unused.py":      "def no(): return 0\n",
				"tests/__init__.py":           "",
				"tests/test_engine.py":        "from app.core.engine import run\nfrom app.core import util\n",
				"src/app/core/engine_test.py": "from app.core.util import aid\n",
			},
			want: []string{
				"src/app/core/engine_test.py\tsrc/app/core/util.py",
				"tests/test_engine.py\tsrc/app/core/engine.py",
				"tests/test_engine.py\tsrc/app/core/util.py",
			},
			notWant: []string{"tests/test_engine.py\tsrc/app/core/unused.py"},
		},

		"typescript": {
			lang: "typescript",
			files: map[string]string{
				"tsconfig.json": `{
					// a comment, because tsconfig is JSONC
					"compilerOptions": { "baseUrl": ".", "paths": { "@app/*": ["src/*"] } },
				}`,
				"src/engine.ts":     "export const run = () => 1;\n",
				"src/util/index.ts": "export const aid = () => 2;\n",
				"src/unused.ts":     "export const no = () => 0;\n",
				// Three resolutions in one file: a relative specifier needing
				// an extension, a directory needing /index, a tsconfig alias,
				// and a `.js` specifier that is `.ts` on disk.
				"src/engine.test.ts": "import { run } from './engine';\n" +
					"import { aid } from './util';\n" +
					"import { run as r2 } from '@app/engine.js';\n" +
					"import 'react';\n" +
					"// import { no } from './unused';\n",
			},
			want: []string{
				"src/engine.test.ts\tsrc/engine.ts",
				"src/engine.test.ts\tsrc/util/index.ts",
			},
			// The commented-out import must not become an edge.
			notWant: []string{"src/engine.test.ts\tsrc/unused.ts"},
		},

		"java": {
			lang: "java",
			files: map[string]string{
				"src/main/java/com/ex/core/Engine.java":  "package com.ex.core;\npublic class Engine {}\n",
				"src/main/java/com/ex/core/Helper.java":  "package com.ex.core;\npublic class Helper {}\n",
				"src/main/java/com/ex/core/Unused.java":  "package com.ex.core;\npublic class Unused {}\n",
				"src/main/java/com/ex/util/Strings.java": "package com.ex.util;\npublic class Strings {}\n",
				"src/main/java/com/ex/util/Numbers.java": "package com.ex.util;\npublic class Numbers {}\n",
				// Explicit import, wildcard import, and same-package with no
				// import at all — the three ways a Java test reaches its
				// subject. Plus `Unused` named only in a comment and a string.
				"src/test/java/com/ex/core/EngineTest.java": "package com.ex.core;\n" +
					"import com.ex.util.Strings;\n" +
					"import com.ex.util.*;\n" +
					"public class EngineTest {\n" +
					"  // Unused is named here only\n" +
					"  String s = \"Unused\";\n" +
					"  void t() { new Engine(); new Helper(); Strings.class.getName(); Numbers.class.getName(); }\n" +
					"}\n",
			},
			want: []string{
				"src/test/java/com/ex/core/EngineTest.java\tsrc/main/java/com/ex/core/Engine.java",
				"src/test/java/com/ex/core/EngineTest.java\tsrc/main/java/com/ex/core/Helper.java",
				"src/test/java/com/ex/core/EngineTest.java\tsrc/main/java/com/ex/util/Numbers.java",
				"src/test/java/com/ex/core/EngineTest.java\tsrc/main/java/com/ex/util/Strings.java",
			},
			notWant: []string{
				"src/test/java/com/ex/core/EngineTest.java\tsrc/main/java/com/ex/core/Unused.java",
			},
		},

		"dart": {
			lang: "dart",
			files: map[string]string{
				"pubspec.yaml":         "name: myapp\n",
				"lib/engine.dart":      "part 'engine_impl.dart';\nimport 'util.dart';\n",
				"lib/engine_impl.dart": "part of 'engine.dart';\n",
				"lib/util.dart":        "int aid() => 2;\n",
				// `package:` resolves through the pubspec to lib/, and the
				// part file comes along because it IS the library.
				"test/engine_test.dart": "import 'package:myapp/engine.dart';\n" +
					"import 'package:flutter_test/flutter_test.dart';\n",
			},
			want: []string{
				"test/engine_test.dart\tlib/engine.dart",
				"test/engine_test.dart\tlib/engine_impl.dart",
			},
			// engine.dart imports util.dart, but the test did not touch it.
			// Following every directive rather than only `part` multiplied a
			// real Flutter app's index by four.
			notWant: []string{"test/engine_test.dart\tlib/util.dart"},
		},

		"rust": {
			lang: "rust",
			files: map[string]string{
				"Cargo.toml": "[workspace]\nmembers = [\"my-engine\"]\n",
				// A hyphenated package name is `my_engine` in a `use` path.
				"my-engine/Cargo.toml":          "[package]\nname = \"my-engine\"\nversion = \"0.1.0\"\n",
				"my-engine/src/lib.rs":          "pub mod core;\npub mod db;\nmod internal;\n",
				"my-engine/src/core.rs":         "pub mod scoring;\n",
				"my-engine/src/core/scoring.rs": "pub fn score() -> i32 { 2 }\n",
				"my-engine/src/db/mod.rs":       "pub fn open() -> bool { true }\n",
				"my-engine/src/internal.rs":     "pub fn secret() -> i32 { 3 }\n",
				"my-engine/tests/engine_test.rs": "use my_engine::core::scoring::score;\n" +
					"use my_engine::db::open;\n",
			},
			want: []string{
				"my-engine/tests/engine_test.rs\tmy-engine/src/core/scoring.rs",
				"my-engine/tests/engine_test.rs\tmy-engine/src/db/mod.rs",
			},
			notWant: []string{
				"my-engine/tests/engine_test.rs\tmy-engine/src/internal.rs",
				// The LONGEST matching module path wins. `core::scoring::score`
				// names scoring.rs; core.rs merely declares the module and
				// holds nothing this test exercises, so an edge to it would be
				// a file the agent opens for nothing.
				"my-engine/tests/engine_test.rs\tmy-engine/src/core.rs",
			},
		},
	}

	// Materialise the embedded script once per case, the same way Run does.
	// The test cannot call Run itself: Run executes in the process's working
	// directory, and each case needs its own repository.
	scriptFor := func(t *testing.T, lang string) string {
		t.Helper()
		src, err := Script(lang)
		if err != nil {
			t.Fatalf("%s is not embedded: %v", lang, err)
		}
		p := filepath.Join(t.TempDir(), "resolve.sh")
		if err := os.WriteFile(p, src, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := resolverRepo(t, tc.files)
			script := scriptFor(t, tc.lang)
			cmd := exec.Command("sh", script)
			cmd.Dir = dir
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s failed: %v\nstderr: %s", tc.lang, err, stderr.String())
			}

			var got []string
			for _, l := range strings.Split(string(out), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					got = append(got, l)
				}
			}
			sort.Strings(got)

			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("%s edges differ.\n got:\n  %s\nwant:\n  %s\nstderr: %s",
					tc.lang, strings.Join(got, "\n  "),
					strings.Join(tc.want, "\n  "), stderr.String())
			}
			for _, bad := range tc.notWant {
				for _, g := range got {
					if g == bad {
						t.Errorf("%s produced an edge it must not: %q", tc.lang, bad)
					}
				}
			}
			// Every script must be safe to run twice: kb regenerates the
			// index and compares it byte for byte, so a command that is not a
			// pure function of the committed files fails CI on a second run
			// having passed locally on the first.
			second := exec.Command("sh", script)
			second.Dir = dir
			out2, err := second.Output()
			if err != nil {
				t.Fatalf("%s failed on a second run: %v", tc.lang, err)
			}
			if string(out2) != string(out) {
				t.Errorf("%s is not deterministic; the committed reverse "+
					"index would differ between runs", tc.lang)
			}
		})
	}
}

// resolverRepo writes files into a fresh git repository. git, not the
// filesystem, is what every script asks for its inventory, so an uncommitted
// file is deliberately invisible to them.
func resolverRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.invalid")
	run("config", "user.name", "t")
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "fixture")
	return dir
}

// TestEveryLanguageIsEmbedded pins that the map and the embedded files agree.
//
// A name in Languages with no file behind it fails at the moment someone runs
// it against a real repo, which is the worst time to find out: the link
// command is not optional, so the gate turns red and the reason is that this
// tool shipped wrong.
func TestEveryLanguageIsEmbedded(t *testing.T) {
	for _, lang := range Names() {
		if _, err := Script(lang); err != nil {
			t.Errorf("%s is advertised but not embedded: %v", lang, err)
		}
	}
	// And the reverse: a script nobody can reach is dead weight that will
	// rot, because no test and no user ever runs it.
	entries, err := scripts.ReadDir("scripts")
	if err != nil {
		t.Fatal(err)
	}
	reachable := map[string]bool{}
	for _, f := range Languages {
		reachable[f] = true
	}
	for _, e := range entries {
		if !reachable[e.Name()] {
			t.Errorf("scripts/%s is embedded but no language name reaches it", e.Name())
		}
	}
}

// TestUnknownLanguageSuggestsTheEscapeHatch keeps the error from reading as a
// hard limit. A link command is any program emitting the two columns, so a
// language not listed here is not something an adopter has to wait for.
func TestUnknownLanguageSuggestsTheEscapeHatch(t *testing.T) {
	_, err := Script("cobol")
	if err == nil {
		t.Fatal("an unknown language resolved")
	}
	for _, want := range []string{"cobol", "your own script"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
