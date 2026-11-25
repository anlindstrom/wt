package wtsync

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// moveLine is a gittest MoveLine with no move! commit, and a function that
// commits one at its top with subject and body and returns the line's
// commits, oldest first.
func moveLine(t *testing.T, between bool) (*gittest.MoveLine, func(subject, body string) []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	s := gittest.NewMoveLine(t, dir, parent, "", between, false)
	return s, func(subject, body string) []string {
		args := []string{"commit", "-q", "--no-verify", "--allow-empty", "-m", subject}
		if body != "" {
			args = append(args, "-m", body)
		}
		gitIn(t, s.Path, args...)
		return strings.Fields(gitIn(t, dir, "rev-list", "--reverse", s.Base+".."+gittest.MoveTmp))
	}
}

func TestPlanMovesResolvesTheSourceByIDOrSubject(t *testing.T) {
	t.Parallel()
	for _, by := range []string{"id", "subject"} {
		t.Run(by, func(t *testing.T) {
			t.Parallel()
			s, move := moveLine(t, false)
			from := gittest.MoveSource
			if by == "id" {
				from = gitIn(t, s.Dir, "rev-parse", "--short", "l3")
			}
			commits := move("move! "+gittest.MoveTarget, gittest.MoveBody(from, gittest.MovePath))
			moves, why, err := PlanMoves(s.Dir, commits)
			if err != nil || why != "" {
				t.Fatalf("refused %q, err %v", why, err)
			}
			if len(moves) != 1 {
				t.Fatalf("moves %+v, want one", moves)
			}
			m := moves[0]
			if m.Source != s.Tips["l3"] || m.Target != s.Tips["l1"] || m.Commit != commits[len(commits)-1] || !slices.Equal(m.Paths, []string{gittest.MovePath}) {
				t.Errorf("move %+v, want %s of l3 %s into l1 %s", m, gittest.MovePath, s.Tips["l3"], s.Tips["l1"])
			}
			if got := gitIn(t, s.Dir, "rev-parse", m.Part+"^^"); got != s.Tips["l1"] {
				t.Errorf("the part's parent's parent is %s, want the target %s", got, s.Tips["l1"])
			}
			if got, want := gitIn(t, s.Dir, "rev-parse", m.Part+"^^{tree}"), gitIn(t, s.Dir, "rev-parse", "l1^{tree}"); got != want {
				t.Errorf("the part's parent has tree %s, want the target's, which has no fixup, %s", got, want)
			}
			if got, want := gitIn(t, s.Dir, "rev-parse", m.Part+":"+gittest.MovePath), gitIn(t, s.Dir, "rev-parse", "l3:"+gittest.MovePath); got != want {
				t.Errorf("the part has %s of %s, want the source's %s", got, gittest.MovePath, want)
			}
		})
	}
}

func TestPlanMovesRefuses(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, subject, body string
		between, extra      bool
		refuse              []string // what the refusal must say; l1, l2, l3 and extra name those commits' short ids
	}{
		{name: "a subject two commits share", subject: "move! " + gittest.MoveTarget, body: gittest.MoveBody(gittest.MoveSource, gittest.MovePath), extra: true,
			refuse: []string{"names 2 commits", "l3", "extra"}},
		{name: "a source that is nowhere", subject: "move! " + gittest.MoveTarget, body: gittest.MoveBody("feat: nothing like it", gittest.MovePath),
			refuse: []string{"names no commit"}},
		{name: "a path the source does not change", subject: "move! " + gittest.MoveTarget, body: gittest.MoveBody(gittest.MoveSource, "other.txt"),
			refuse: []string{"does not change other.txt"}},
		{name: "the whole of the source", subject: "move! " + gittest.MoveTarget, body: gittest.MoveBody(gittest.MoveSource, gittest.MovePath, "feature.txt"),
			refuse: []string{"every path", "fixup!"}},
		{name: "a target above the source", subject: "move! " + gittest.MoveSource, body: gittest.MoveBody(gittest.MoveTarget, "door/door.go"),
			refuse: []string{"is not below"}},
		{name: "no Move-Path", subject: "move! " + gittest.MoveTarget, body: "Move-From: " + gittest.MoveSource + "\n",
			refuse: []string{"Move-Path"}},
		{name: "a commit between that carries the part's context", subject: "move! " + gittest.MoveTarget, body: gittest.MoveBody(gittest.MoveSource, gittest.MovePath), between: true,
			refuse: []string{"does not apply at", "l2", gittest.MoveBetween}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, move := moveLine(t, c.between)
			names := map[string]string{}
			for _, l := range s.Layers {
				names[l] = gitIn(t, s.Dir, "rev-parse", "--short=7", l)
			}
			if c.extra {
				gitIn(t, s.Path, "commit", "-q", "--no-verify", "--allow-empty", "-m", gittest.MoveSource)
				names["extra"] = gitIn(t, s.Path, "rev-parse", "--short=7", "HEAD")
			}
			commits := move(c.subject, c.body)
			moves, why, err := PlanMoves(s.Dir, commits)
			if err != nil {
				t.Fatal(err)
			}
			if why == "" {
				t.Fatalf("not refused: %+v", moves)
			}
			for _, w := range c.refuse {
				if n, ok := names[w]; ok {
					w = n
				}
				if !strings.Contains(why, w) {
					t.Errorf("refusal %q does not say %q", why, w)
				}
			}
			if out := gitIn(t, s.Dir, "for-each-ref", "refs/wt-sync-kept/"); out != "" {
				t.Errorf("a refused plan wrote refs:\n%s", out)
			}
		})
	}
}

// A line with no move! commit plans none, and refuses nothing.
func TestPlanMovesWithNoMoveCommit(t *testing.T) {
	t.Parallel()
	s, _ := moveLine(t, false)
	commits := strings.Fields(gitIn(t, s.Dir, "rev-list", "--reverse", s.Base+".."+gittest.MoveTmp))
	moves, why, err := PlanMoves(s.Dir, commits)
	if err != nil || why != "" || len(moves) != 0 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
}
