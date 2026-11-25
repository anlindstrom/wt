package gittest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// MoveLine is a stack held on one line with a move! commit at its top: part
// of a feature commit belongs two layers down.
//
//	l1  feat(l1): door (door/door.go): the target
//	l2  feat(l2): other (other.txt); with between, it also changes line 3 of
//	    door/refuse.go, the context of the part
//	l3  feat(l3): feature (feature.txt, and line 2 of door/refuse.go): the source
//	tmp/move  l3 plus "move! feat(l1): door", moving door/refuse.go from the source
//
// door/refuse.go is on the base. Trunk has only the declaration after the
// base; origin is the repository itself, and tmp/move has a worktree.
type MoveLine struct {
	Dir    string
	Base   string
	Layers []string          // bottom first
	Tips   map[string]string // each layer's tip, and tmp/move's
	Path   string            // tmp/move's worktree
}

// The branch MoveLine's top is on, and the subjects of its commits.
const (
	MoveTmp     = "tmp/move"
	MoveTarget  = "feat(l1): door"
	MoveBetween = "feat(l2): other"
	MoveSource  = "feat(l3): feature"
	MovePath    = "door/refuse.go"
)

// MoveBody is the body of a move! commit that moves paths from source.
func MoveBody(source string, paths ...string) string {
	body := "Move-From: " + source + "\n"
	for _, p := range paths {
		body += "Move-Path: " + p + "\n"
	}
	return body
}

// refuseGo is door/refuse.go with line n changed to with, for each pair.
func refuseGo(changes map[int]string) string {
	var b strings.Builder
	for n := 1; n <= 10; n++ {
		if c, ok := changes[n]; ok {
			b.WriteString(c + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", n)
	}
	return b.String()
}

// NewMoveLine builds a MoveLine in dir, a repository on main with at least
// one commit. yaml is committed on trunk after the base; between makes l2
// change the context of the part. With move false the line has no move!
// commit, for a test that writes its own.
func NewMoveLine(t testing.TB, dir, wtDir, yaml string, between, move bool) *MoveLine {
	t.Helper()
	s := &MoveLine{Dir: dir, Layers: []string{"l1", "l2", "l3"}, Tips: map[string]string{}}
	write := func(rel, content string) { WriteFile(t, filepath.Join(dir, rel), content) }
	commit := func(msg ...string) string {
		Git(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		Git(t, dir, args...)
		return Git(t, dir, "rev-parse", "HEAD")
	}
	write(MovePath, refuseGo(nil))
	s.Base = commit("base")
	Git(t, dir, "checkout", "-q", "-b", MoveTmp)
	branch := func(name string) {
		s.Tips[name] = Git(t, dir, "rev-parse", "HEAD")
		Git(t, dir, "branch", name)
	}
	write("door/door.go", "package door\n")
	commit(MoveTarget)
	branch("l1")
	write("other.txt", "other\n")
	if between {
		write(MovePath, refuseGo(map[int]string{3: "line 3, from l2"}))
	}
	commit(MoveBetween)
	branch("l2")
	write("feature.txt", "feature\n")
	changes := map[int]string{2: "line 2, refused at the door"}
	if between {
		changes[3] = "line 3, from l2"
	}
	write(MovePath, refuseGo(changes))
	commit(MoveSource)
	branch("l3")
	if move {
		commit("move! "+MoveTarget, MoveBody(MoveSource, MovePath))
	}
	s.Tips[MoveTmp] = Git(t, dir, "rev-parse", "HEAD")
	Git(t, dir, "checkout", "-q", "main")
	if yaml != "" {
		write(".wt-sync.yaml", yaml)
		commit("chore: declare")
	}
	Git(t, dir, "remote", "add", "origin", dir)
	Git(t, dir, "fetch", "-q", "origin")
	s.Path = filepath.Join(wtDir, strings.ReplaceAll(MoveTmp, "/", "_"))
	Git(t, dir, "worktree", "add", "-q", s.Path, MoveTmp)
	return s
}
