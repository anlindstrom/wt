package gittest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ContextLine is a stack held on one line whose fixups were written at its
// top, against lines that only a later commit adds: folded behind their
// targets low on the line, they lack their context.
//
//	l1  feat(l1): page size (size: in src/state.ts), test(l1): size test
//	l2  feat(l2): open flag (open: in src/state.ts, and src/flags.ts), test(l2): placement block
//	    (describe('placement writes') in src/page.test.ts)
//	l3  feat(l3): other
//	tmp/ctx  l3 plus "fixup! feat(l1): page size", which changes size: and
//	    open:, and, with both, "fixup! test(l1): size test", which adds a
//	    test inside the placement block; in its own worktree
//
// Trunk moved after the base with a commit of its own. origin is the
// repository itself.
type ContextLine struct {
	Dir    string
	Base   string
	Layers []string          // bottom first
	Tips   map[string]string // each layer's tip, and tmp/ctx's
	Paths  map[string]string // the worktree of a branch that has one
	// Commits is each commit of the line by its subject.
	Commits map[string]string
}

// ContextTmp is the branch at the top of a ContextLine.
const ContextTmp = "tmp/ctx"

// The subjects of a ContextLine's commits.
const (
	CtxSize      = "feat(l1): page size"
	CtxSizeTest  = "test(l1): size test"
	CtxOpen      = "feat(l2): open flag"
	CtxPlacement = "test(l2): placement block"
	CtxOther     = "feat(l3): other"
	CtxFixState  = "fixup! feat(l1): page size"
	CtxFixTest   = "fixup! test(l1): size test"
)

// StateTS is src/state.ts with the lines given after page: and after h:.
func StateTS(afterPage, afterH string) string {
	var b strings.Builder
	b.WriteString("export function useState() {\n  const params = {\n    page: 1,\n")
	if afterPage != "" {
		b.WriteString("    " + afterPage + "\n")
	}
	for i, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		b.WriteString("    " + k + ": " + string(rune('1'+i)) + ",\n")
	}
	if afterH != "" {
		b.WriteString("    " + afterH + "\n")
	}
	b.WriteString("  };\n  return params;\n}\n")
	return b.String()
}

// PageTest is src/page.test.ts with the page block's extra test and the
// placement block's tests, "" leaving the block out.
func PageTest(sizes bool, placement ...string) string {
	var b strings.Builder
	b.WriteString("import { describe, test } from './t';\n\ndescribe('page', () => {\n  test('pages', () => {});\n")
	if sizes {
		b.WriteString("  test('sizes', () => {});\n")
	}
	b.WriteString("});\n")
	if len(placement) > 0 {
		b.WriteString("\ndescribe('placement writes', () => {\n")
		for _, p := range placement {
			b.WriteString("  test('" + p + "', () => {});\n")
		}
		b.WriteString("});\n")
	}
	return b.String()
}

// NewContextLine builds a ContextLine in dir, a repository on main with at
// least one commit. yaml, when not empty, is committed on trunk after the
// base. both adds the second fixup.
func NewContextLine(t testing.TB, dir, wtDir, yaml string, both bool) *ContextLine {
	t.Helper()
	s := &ContextLine{Dir: dir, Layers: []string{"l1", "l2", "l3"}, Tips: map[string]string{},
		Paths: map[string]string{}, Commits: map[string]string{}}
	write := func(rel, content string) { WriteFile(t, filepath.Join(dir, rel), content) }
	commit := func(msg string) string {
		Git(t, dir, "add", "-A")
		Git(t, dir, "commit", "-q", "--no-verify", "-m", msg)
		sha := Git(t, dir, "rev-parse", "HEAD")
		s.Commits[msg] = sha
		return sha
	}
	branch := func(name string) {
		s.Tips[name] = Git(t, dir, "rev-parse", "HEAD")
		Git(t, dir, "branch", name)
	}
	write("src/state.ts", StateTS("", ""))
	write("src/page.test.ts", PageTest(false))
	s.Base = commit("base")
	Git(t, dir, "checkout", "-q", "-b", ContextTmp)
	write("src/state.ts", StateTS("size: 10,", ""))
	commit(CtxSize)
	write("src/page.test.ts", PageTest(true))
	commit(CtxSizeTest)
	branch("l1")
	write("src/state.ts", StateTS("size: 10,", "open: false,"))
	write("src/flags.ts", "export const flags = ['open'];\n")
	commit(CtxOpen)
	write("src/page.test.ts", PageTest(true, "writes"))
	commit(CtxPlacement)
	branch("l2")
	write("other.txt", "other\n")
	commit(CtxOther)
	branch("l3")
	write("src/state.ts", StateTS("size: 20,", "open: true,"))
	commit(CtxFixState)
	if both {
		write("src/page.test.ts", PageTest(true, "writes", "places"))
		commit(CtxFixTest)
	}
	s.Tips[ContextTmp] = Git(t, dir, "rev-parse", "HEAD")
	Git(t, dir, "checkout", "-q", "main")
	if yaml != "" {
		write(".wt-sync.yaml", yaml)
		commit("chore: declare")
	}
	write("trunk.txt", "trunk\n")
	commit("feat: trunk's own")
	Git(t, dir, "remote", "add", "origin", dir)
	Git(t, dir, "fetch", "-q", "origin")
	path := filepath.Join(wtDir, strings.ReplaceAll(ContextTmp, "/", "_"))
	Git(t, dir, "worktree", "add", "-q", path, ContextTmp)
	s.Paths[ContextTmp] = path
	return s
}
