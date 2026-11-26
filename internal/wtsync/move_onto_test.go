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
