package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// contextLine is gittest's ContextLine in a plain repository, and the line's
// commits, oldest first.
func contextLine(t *testing.T, both bool) (*gittest.ContextLine, []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	s := gittest.NewContextLine(t, dir, filepath.Join(parent, "wt"), "", both)
	return s, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", s.Base+".."+gittest.ContextTmp))
}

// Both fixups of the case are named, each with its hunk and the first later
// commit that carries its context; the hunk of the first fixup whose context
// its target has (size:) is not.
func TestContextGapsNameEveryFixupAheadOfItsContext(t *testing.T) {
	t.Parallel()
	s, commits := contextLine(t, true)
	gaps, _, err := ContextGaps(s.Dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	want := []ContextGap{
		{Fixup: s.Commits[gittest.CtxFixState], FixupSubject: gittest.CtxFixState, Target: s.Commits[gittest.CtxSize], TargetSubject: gittest.CtxSize,
			Path: "src/state.ts", Lines: "13", Needs: s.Commits[gittest.CtxOpen], NeedsSubject: gittest.CtxOpen},
		{Fixup: s.Commits[gittest.CtxFixTest], FixupSubject: gittest.CtxFixTest, Target: s.Commits[gittest.CtxSizeTest], TargetSubject: gittest.CtxSizeTest,
			Path: "src/page.test.ts", Lines: "10", Needs: s.Commits[gittest.CtxPlacement], NeedsSubject: gittest.CtxPlacement},
	}
	if len(gaps) != len(want) {
		t.Fatalf("gaps %+v, want %+v", gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("gap %d: %+v, want %+v", i, gaps[i], want[i])
		}
	}
	line := gaps[0].Line()
	wantLine := `fixup moved ahead of its context: src/state.ts:13 needs ` + git.ShortID(s.Commits[gittest.CtxOpen], 7) +
		` "feat(l2): open flag", which comes later on the line`
	if line != wantLine {
		t.Errorf("line %q, want %q", line, wantLine)
	}
}

// A fixup whose hunks all apply where autosquash puts it is not named, and
// a line with no fixup has nothing to say.
func TestContextGapsLeaveAFixupThatAppliesAlone(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := gittest.NewFoldLine(t, gittest.NewRepo(t, parent, "r"), filepath.Join(parent, "wt"), "")
	commits := strings.Fields(gitIn(t, s.Dir, "rev-list", "--reverse", s.Base+".."+gittest.FoldTmp))
	if gaps, _, err := ContextGaps(s.Dir, commits); err != nil || len(gaps) != 0 {
		t.Errorf("fold line: gaps %+v, err %v; want none", gaps, err)
	}
	if gaps, _, err := ContextGaps(s.Dir, commits[:len(commits)-1]); err != nil || len(gaps) != 0 {
		t.Errorf("no fixup: gaps %+v, err %v; want none", gaps, err)
	}
}

// twoFixups is a line whose target "feat: a" gets two fixups at the top:
// the first changes x.txt cleanly and adds f1 below c in y.txt, a line only
// the later "feat: c" adds; the second is second, written over the first.
func twoFixups(t *testing.T, second func(write func(path, content, subject string))) (dir string, commits []string, subjects map[string]string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	subjects = map[string]string{}
	write := func(path, content, subject string) {
		gittest.WriteFile(t, filepath.Join(dir, path), content)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject)
		subjects[subject] = gitIn(t, dir, "rev-parse", "HEAD")
	}
	gittest.WriteFile(t, filepath.Join(dir, "x.txt"), "x=0\n")
	write("y.txt", "y\n", "base")
	write("y.txt", "y\na\n", "feat: a")
	write("y.txt", "y\na\nc\n", "feat: c")
	gittest.WriteFile(t, filepath.Join(dir, "x.txt"), "x=1\n")
	write("y.txt", "y\na\nc\nf1\n", "fixup! feat: a")
	second(write)
	return dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", subjects["base"]+"..HEAD")), subjects
}

// The second fixup changes x=1, which the first fixup's clean part makes:
// autosquash puts the first before it, so it lacks nothing, and the first
// is named alone, needing feat: c.
func TestContextGapsKeepTheCleanPartOfAnEarlierFixup(t *testing.T) {
	t.Parallel()
	dir, commits, sha := twoFixups(t, func(write func(string, string, string)) {
		write("x.txt", "x=2\n", "fixup! feat: a")
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Path != "y.txt" || gaps[0].Needs != sha["feat: c"] || len(unchecked) != 0 {
		t.Errorf("gaps %+v, unchecked %q; want the first fixup's y.txt alone, needing feat: c", gaps, unchecked)
	}
}

// The second fixup changes f1, which only the first fixup's conflicting
// part adds: what it lacks is not a later commit's, so it is named as not
// analysed, never as needing the first fixup.
func TestContextGapsCallAFixupOverAConflictingOneUncertain(t *testing.T) {
	t.Parallel()
	dir, commits, sha := twoFixups(t, func(write func(string, string, string)) {
		write("y.txt", "y\na\nc\nf2\n", "fixup! feat: a")
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	second := commits[len(commits)-1]
	for _, g := range gaps {
		if g.Fixup == second {
			t.Errorf("the second fixup is named: %+v", g)
		}
	}
	if len(gaps) != 1 || gaps[0].Needs != sha["feat: c"] {
		t.Errorf("gaps %+v, want the first fixup's alone", gaps)
	}
	if len(unchecked) != 1 || !strings.Contains(unchecked[0], second[:7]) {
		t.Errorf("unchecked %q, want the second fixup named as not analysed", unchecked)
	}
}
