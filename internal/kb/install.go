package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	kbcompile "github.com/fabianvf/kb-compile"
)

// InstallVersionPath records the build that wrote a repository's hooks, so the
// commit gate can give a teammate without `kb` the matching install command.
const InstallVersionPath = ".claude/hooks/kb-version"

// Install writes the hooks, skills and spec into .claude/ for committing.
//
// A plugin is the wrong way to reach a team: enabling one in a committed
// settings file does not download it, so every collaborator has to run an
// install they do not know about, and until then the gate is silently absent.
// Committed hooks arrive with the clone. Re-running this after a version bump
// replaces the copies, which is the answer to them drifting.
//
// It writes no KB artifact and never wires `kb compiled` into anything.
//
// The returned lines report each path, so a replaced file that somebody edited
// by hand shows up here as well as in `git diff`.
func Install(version string) ([]string, error) {
	const settingsPath = ".claude/settings.json"
	// Before anything is written: a settings file that cannot be merged must not
	// leave hooks on disk that nothing runs.
	settings, err := mergedHookSettings(settingsPath)
	if err != nil {
		return nil, err
	}

	var report []string
	put := func(dest string, data []byte, mode os.FileMode) error {
		old, err := os.ReadFile(dest)
		state := "wrote"
		if err == nil {
			state = "updated"
			if bytes.Equal(old, data) {
				report = append(report, "unchanged "+dest)
				return nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return err
		}
		// WriteFile keeps an existing file's mode, and a hook that lost its
		// executable bit stops gating with no message.
		if err := os.Chmod(dest, mode); err != nil {
			return err
		}
		report = append(report, state+" "+dest)
		return nil
	}

	err = fs.WalkDir(kbcompile.Assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := kbcompile.Assets.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case p == "SPEC.md":
			return put(".claude/kb/SPEC.md", data, 0o644)
		case strings.HasPrefix(p, "hooks/"):
			return put(".claude/hooks/"+path.Base(p), data, 0o755)
		default:
			// The skills link to the spec relative to this repository's layout.
			data = bytes.ReplaceAll(data, []byte("../../SPEC.md"), []byte("../../kb/SPEC.md"))
			return put(".claude/"+p, data, 0o644)
		}
	})
	if err != nil {
		return report, err
	}
	if version != "" {
		if err := put(InstallVersionPath, []byte(version+"\n"), 0o644); err != nil {
			return report, err
		}
	}
	return report, put(settingsPath, settings, 0o644)
}

// mergedHookSettings adds the two hooks to a settings file and leaves everything
// else in it alone. Refusing an unparseable file is what keeps that promise.
func mergedHookSettings(p string) ([]byte, error) {
	root := map[string]any{}
	data, err := os.ReadFile(p)
	if err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON, so it cannot be merged "+
				"without losing what is in it. Fix it and run `kb install` again: %v", p, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		if _, present := root["hooks"]; present {
			return nil, fmt.Errorf(`%s has a "hooks" value that is not an object. `+
				"Fix it and run `kb install` again", p)
		}
		hooks = map[string]any{}
		root["hooks"] = hooks
	}

	add := func(event, matcher, script string, timeout int) {
		command := `"$CLAUDE_PROJECT_DIR"/.claude/hooks/` + script
		entries, _ := hooks[event].([]any)
		for _, e := range entries {
			group, _ := e.(map[string]any)
			list, _ := group["hooks"].([]any)
			for _, h := range list {
				if m, _ := h.(map[string]any); m["command"] == command {
					return
				}
			}
		}
		group := map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": timeout,
		}}}
		if matcher != "" {
			group["matcher"] = matcher
		}
		hooks[event] = append(entries, group)
	}
	add("PreToolUse", "Bash", "kb-fresh-gate.sh", 30)
	add("UserPromptSubmit", "", "kb-context.sh", 15)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
