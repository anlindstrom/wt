package gittest

import (
	"path/filepath"
	"strings"
	"testing"
)

// EditLine is a stack held on one first-parent line where a low commit has
// a defect that a commit two layers up fixes as part of its own work: the
// case for an edit stop, which moves the fix down to where it belongs.
//
//	e1  one commit
//	e2  A "feat(e2): add the lib" (lib.txt with value = 1, the defect), then two.txt
//	e3  one commit, or with Emptied, "fix(e3): set the value" alone: value = 2
//	e4  B "feat(e4): rename and fix" (value = 2 and name = new, use.txt), then four.txt
//	tmp/edit  e4 plus one commit, in its own worktree
//
// With Emptied, B renames only: e3 fixed the value, and moving the fix down
// to A empties e3's one commit. Trunk is main, origin the repository itself.
type EditLine struct {
	Dir    string
	Base   string
	Layers []string          // bottom first
	Tips   map[string]string // each layer's tip, and tmp/edit's
	Path   string            // tmp/edit's worktree
	A, B   string            // the defect and the commit that fixes it
}

// EditTmp is the branch at the top of an EditLine.
const EditTmp = "tmp/edit"

// EditLib is lib.txt as A writes it, the defect included.
const EditLib = "value = 1\nname = old\n"

// NewEditLine builds an EditLine in dir, a repository on main with at least
// one commit. yaml, when not empty, is committed on trunk after the base.
func NewEditLine(t testing.TB, dir, wtDir, yaml string, emptied bool) *EditLine {
	t.Helper()
	s := &EditLine{Dir: dir, Layers: []string{"e1", "e2", "e3", "e4"}, Tips: map[string]string{}}
	write := func(rel, content string) { WriteFile(t, filepath.Join(dir, rel), content) }
	commit := func(msg string) string {
		Git(t, dir, "add", "-A")
		Git(t, dir, "commit", "-q", "--no-verify", "-m", msg)
		return Git(t, dir, "rev-parse", "HEAD")
	}
	write("readme.txt", "edit line\n")
	s.Base = commit("base")
	Git(t, dir, "checkout", "-q", "-b", EditTmp)
	branch := func(name string) {
		s.Tips[name] = Git(t, dir, "rev-parse", "HEAD")
		Git(t, dir, "branch", name)
	}
	write("one.txt", "one\n")
	commit("feat(e1): one")
	branch("e1")
	write("lib.txt", EditLib)
	s.A = commit("feat(e2): add the lib")
	write("two.txt", "two\n")
	commit("feat(e2): two")
	branch("e2")
	if emptied {
		write("lib.txt", "value = 2\nname = old\n")
		commit("fix(e3): set the value")
	} else {
		write("three.txt", "three\n")
		commit("feat(e3): three")
	}
	branch("e3")
	write("lib.txt", "value = 2\nname = new\n")
	write("use.txt", "use\n")
	subject := "feat(e4): rename and fix"
	if emptied {
		subject = "feat(e4): rename"
	}
	s.B = commit(subject)
	write("four.txt", "four\n")
	commit("feat(e4): four")
	branch("e4")
	write("top.txt", "top\n")
	commit("chore: top")
	s.Tips[EditTmp] = Git(t, dir, "rev-parse", "HEAD")
	Git(t, dir, "checkout", "-q", "main")
	if yaml != "" {
		write(".wt-sync.yaml", yaml)
		commit("chore: declare")
	}
	Git(t, dir, "remote", "add", "origin", dir)
	Git(t, dir, "fetch", "-q", "origin")
	s.Path = filepath.Join(wtDir, strings.ReplaceAll(EditTmp, "/", "_"))
	Git(t, dir, "worktree", "add", "-q", s.Path, EditTmp)
	return s
}
