// Package kbcompile holds the files `kb install` writes into a repository.
//
// It sits at the module root because go:embed cannot reach above the package
// that declares it, and the plugin needs these same files at these same paths.
// One copy serves both, so the two ways of installing cannot disagree.
package kbcompile

import "embed"

//go:embed SPEC.md hooks/*.sh skills/*/SKILL.md
var Assets embed.FS
