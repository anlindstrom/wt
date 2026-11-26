package wtsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A run with a move! commit that moves the line is held to the line replayed
// without it: what a stop resolved differently is not accepted, as it would be
// for a fold with no move.
func TestTreeGuardHoldsAMovedLineWithAMoveToItsReplay(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, false)
	gittest.Git(t, g.dir, "checkout", "-q", "line")
	gittest.Git(t, g.dir, "commit", "-q", "--allow-empty", "-m", "move! feature")
	g.oldTip = gittest.Git(t, g.dir, "rev-parse", "HEAD")
	replayed := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature")})
	in := func(tip string, resolved ...StopResolution) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Resolved: resolved}
	}
	if res := TreeGuard(g.dir, in(replayed)); res.Why != "" {
		t.Fatalf("the replay itself is refused: %+v", res)
	}
	other := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "other")})
	rec := g.resolvedAt(t, g.oldTip, tText("trunk", "other"))
	if res := TreeGuard(g.dir, in(other, rec)); !strings.Contains(res.Why, "t.txt") {
		t.Fatalf("a recorded resolution that is not the replay passed: %+v", res)
	}
}

// A blob git cannot read while a moved hunk is placed is a failure, not a
// refusal of where the hunk would land.
func TestPlacingAMovedHunkReportsABlobGitCannotRead(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, false)
	d, err := diffOfPath(g.dir, g.base, g.oldTip, "t.txt")
	if err != nil {
		t.Fatal(err)
	}
	pick, why := pickHunks(d, []LineRange{{Path: "t.txt", From: d.hunks[0].first, To: d.hunks[0].last}})
	if why != "" {
		t.Fatal(why)
	}
	blob := gittest.Git(t, g.dir, "rev-parse", g.onto+":t.txt")
	if err := os.Remove(filepath.Join(g.dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	picks := map[string]linesPick{"t.txt": pick}
	if ok, err := landsAsSourced(g.dir, g.oldTip, picks, g.onto+"^{tree}", g.oldTip+"^{tree}"); err == nil {
		t.Fatalf("an unreadable blob answered %v with no error", ok)
	}
}

// A content check that cannot run is an error, still failing the fold: a blob
// git cannot read is not a difference in the line.
func TestTreeGuardReportsAMergeItCannotMakeAsAnError(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	resolution := tText("trunk mine", "last")
	rec := g.resolvedAt(t, g.oldTip, resolution)
	rec.Order = 1
	stopped := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution})
	fixed := g.rewritten(t, stopped, map[string]string{"t.txt": strings.Replace(resolution, "mid", "fixed", 1)})
	fix := g.fixAt(t, stopped, fixed, 2)
	in := GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: fixed, Stopped: []string{"t.txt"},
		Resolved: []StopResolution{rec}, Fixes: []CheckFix{fix}}
	// The fix starts from other content than the resolution, so the two are
	// merged; the resolution's blob is then gone.
	in.Resolved[0].Blob = strings.Repeat("1", 40)
	if res := TreeGuard(g.dir, in); res.Err == nil || res.Why == "" {
		t.Fatalf("%+v", res)
	}
}

// A rebase takes --each-commit=targets onto another commit only for a line
// with a move! commit: any other is refused before anything is read or written.
func TestRebaseRefusesTargetsOntoAnotherCommitWithoutAMove(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, false)
	line := Line{Branch: "line", Base: g.base, Head: g.oldTip, Commits: []string{g.oldTip}}
	_, err := Rebase(g.dir, &Config{}, Request{Path: g.dir, Branch: "line", Onto: g.onto, EachCommit: EachTargets,
		Stack: &StackRequest{Line: line, Autosquash: true}}, nil)
	if err == nil || err.Error() != TargetsRefusal(false) {
		t.Fatalf("err %v", err)
	}
	if got := gittest.Git(t, g.dir, "for-each-ref", "refs/wt-sync/"); got != "" {
		t.Errorf("refs written: %s", got)
	}
}
