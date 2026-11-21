package testkit

import (
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// The git and file helpers every command test uses. They wrap gittest, which
// owns the identity and the isolation git runs under.

// GitIn runs git in dir and fails the test when it fails.
func GitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Git(t, dir, args...)
}

// GitOut runs git in dir, fails the test when it fails and returns its
// stdout; GitIn returns nothing.
func GitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gittest.Git(t, dir, args...)
}

// GitTry runs git where a non-zero exit is part of the scenario — a person's
// `rebase --continue` that stops at the next conflict — and never opens an
// editor. The caller asserts the state it was after.
func GitTry(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, _ := gittest.Try(t, dir, args...)
	return out
}

// WriteFile writes content to rel under dir.
func WriteFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	gittest.WriteFile(t, filepath.Join(dir, rel), content)
}
