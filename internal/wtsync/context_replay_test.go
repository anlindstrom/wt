package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// firstWithRun is the subject of the first commit after target in the line,
// oldest first, whose path holds run: where the real fold first has it.
func firstWithRun(t *testing.T, dir, target, path string, run []string) string {
	t.Helper()
	revs := strings.Fields(gitIn(t, dir, "rev-list", "--reverse", target+"..HEAD"))
	for _, r := range revs {
		out, err := gittest.Try(t, dir, "cat-file", "-p", r+":"+path)
		if err == nil && containsRun(strings.Split(out, "\n"), run) {
			return gitIn(t, dir, "log", "-1", "--format=%s", r)
		}
	}
	return ""
}

// wt#213: a fixup whose context is the fold of a fixup of a later commit plus
// the commits between the two targets is answered, and names the commit the
// real fold first has that context at.
func TestContextGapsReplayAFoldAndTheCommitsBetweenTargets(t *testing.T) {
	t.Parallel()
	list := func(second, third, fourth string) string {
		return "head\n" + second + "\n" + third + "\n" + fourth + "\ntail\n"
	}
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "list.txt", list("one # a", "two # a", "three # a"), "g.txt", "g\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "g.txt", "g\nb\n"},
		{"feat: c", "list.txt", list("one # a", "two # a", "three # c")},
		{"fixup! feat: b", "list.txt", list("one # b", "two # a", "three # c")},
		{"fixup! feat: a", "list.txt", list("one # b", "two", "three # c")},
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchecked) != 0 {
		t.Errorf("unchecked %q, want none", unchecked)
	}
	fixup := commits[len(commits)-1]
	if len(gaps) != 1 || gaps[0].Fixup != fixup || gaps[0].Path != "list.txt" {
		t.Fatalf("gaps %+v, want the last fixup's list.txt", gaps)
	}

	// The real fold: the fixup of feat: a stops, is skipped, and the line
	// is then asked where the fixup's context first is.
	base := gitIn(t, dir, "rev-parse", commits[0]+"^")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", base); err == nil {
		t.Fatalf("the fold did not stop:\n%s", out)
	}
	if out, err := gittest.Try(t, dir, "rebase", "--skip"); err != nil {
		t.Fatalf("skip: %v\n%s", err, out)
	}
	actual := firstWithRun(t, dir, "HEAD~3", "list.txt", []string{"head", "one # b", "two # a", "three # c"})
	if actual != "feat: c" || gaps[0].NeedsSubject != actual || gaps[0].Needs != sha["feat: c"] {
		t.Errorf("gap needs %s %q; the real fold first has the context at %q", git.ShortID(gaps[0].Needs, 7), gaps[0].NeedsSubject, actual)
	}
}

// A fixup whose context is in the fixup of an earlier commit than its own
// target is not predicted: it stays not analysed, with the reason.
func TestContextGapsStayNotAnalysedWhenTheContextIsAnEarlierCommitsFold(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "y.txt", "y\n"},
		{"feat: a", "y.txt", "y\na\n"},
		{"feat: b", "y.txt", "y\na\nb\n"},
		{"fixup! feat: a", "y.txt", "y\na\nb\na2\n"},
		{"fixup! feat: b", "y.txt", "y\na\nb\na2\nb2\n"},
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gaps {
		if g.Fixup == commits[len(commits)-1] {
			t.Errorf("gap %+v: the last fixup is answered, want it not analysed", g)
		}
	}
	if len(unchecked) != 1 || !strings.Contains(unchecked[0], "not analysed") || !strings.Contains(unchecked[0], "whose own fold wt cannot predict") {
		t.Errorf("unchecked %q; want the fixup of feat: b not analysed", unchecked)
	}
}

// An earlier fixup of a commit below the fixup's target changes what the
// target and the commits after it come to, which the replay does not apply:
// here fixup! X restores the line C changes, so the context is already at B
// and C becomes empty. The replay would name C, so the fixup stays not
// analysed.
func TestContextGapsStayNotAnalysedWhenAnAncestorFoldChangesTheReplay(t *testing.T) {
	t.Parallel()
	list := func(first, second, third string) string {
		return "head\n" + first + "\n" + second + "\n" + third + "\ntail\n"
	}
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "list.txt", list("one # a", "two # a", "three # a"), "g.txt", "g\n"},
		{"feat: x", "x.txt", "x\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "g.txt", "g\nb\n"},
		{"feat: c", "list.txt", list("one # a", "two # a", "three # c")},
		{"feat: revert c", "list.txt", list("one # a", "two # a", "three # a")},
		{"fixup! feat: x", "list.txt", list("one # a", "two # a", "three # c")},
		{"fixup! feat: b", "list.txt", list("one # b", "two # a", "three # c")},
		{"fixup! feat: a", "list.txt", list("one # b", "two", "three # c")},
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	fixup := commits[len(commits)-1]
	for _, g := range gaps {
		if g.Fixup == fixup {
			t.Errorf("gap %+v: the last fixup is answered, want it not analysed (feat: c is %s)", g, git.ShortID(sha["feat: c"], 7))
		}
	}
	want := git.ShortID(fixup, 7) + ` "fixup! feat: a": not analysed: 1 file (1 hunk) lack context behind ` + git.ShortID(sha["feat: a"], 7) +
		` that 1 later commit adds; the context is in the fixup ` + git.ShortID(commits[len(commits)-2], 7) + `, whose own fold wt cannot predict`
	if len(unchecked) != 1 || unchecked[0] != want {
		t.Errorf("unchecked %q, want [%q]", unchecked, want)
	}
}

// The replay stops, and says so with a nil result, at an ordinary commit that
// does not merge onto the tree it reaches, and gives each commit's tree when
// every one does.
func TestReplayAfterStopsAtAnOrdinaryCommitThatConflicts(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "f.txt", "1\n2\n3\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: c", "f.txt", "1\nC\n3\n"},
		{"fixup! feat: a", "a.txt", "a\nf\n"},
	})
	all := []lineCommit{{commits[0], "feat: a"}, {commits[1], "feat: c"}, {commits[2], "fixup! feat: a"}}
	isFold := map[int]bool{2: true}
	startA := gitIn(t, dir, "rev-parse", sha["feat: a"]+"^{tree}")

	trees, err := replayAfter(dir, all, isFold, map[int]int{}, map[int]bool{}, startA, 0, 2)
	if err != nil || trees[1] == "" {
		t.Fatalf("trees %v, err %v; want feat: c's tree from the target's", trees, err)
	}
	if got := gitIn(t, dir, "show", trees[1]+":f.txt"); got != "1\nC\n3" {
		t.Errorf("f.txt at feat: c is %q", got)
	}

	gitIn(t, dir, "checkout", "-q", "-b", "side", sha["base"])
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), "1\nS\n3\n")
	gitIn(t, dir, "commit", "-q", "-a", "-m", "side")
	startSide := gitIn(t, dir, "rev-parse", "HEAD^{tree}")
	trees, err = replayAfter(dir, all, isFold, map[int]int{}, map[int]bool{}, startSide, 0, 2)
	if err != nil || trees != nil {
		t.Errorf("trees %v, err %v; want nil, nil at the conflicting feat: c", trees, err)
	}
}
