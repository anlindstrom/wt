package wtsync

import (
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// fixAt is a CheckFix for a person who changed the commit stopped, tree for
// tree, into the commit fixed.
func (g guardRepo) fixAt(t *testing.T, stopped, fixed string, order ...int) CheckFix {
	t.Helper()
	tree := func(c string) string { return gittest.Git(t, g.dir, "rev-parse", c+"^{tree}") }
	f := CheckFix{Stop: stopped, Before: tree(stopped), After: tree(fixed)}
	if len(order) > 0 {
		f.Order = order[0]
	}
	return f
}

// wt#216: what a person fixed at a check stop is merged into the line a fold
// onto trunk is compared to, and the comparison then holds as before: the fix
// is accepted and named, and any other difference, in the fixed file or in
// another, is refused.
func TestTreeGuardMergesAFixMadeAtACheckStopIntoWhatItComparesTo(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, false)
	stopped := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature")})
	withFix := map[string]string{"t.txt": strings.Replace(tText("trunk", "feature"), "mid", "fixed", 1), "call.txt": "call(x)\n"}
	fixed := g.rewritten(t, stopped, withFix)
	fix := g.fixAt(t, stopped, fixed)
	in := func(tip string, fixes ...CheckFix) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Fixes: fixes}
	}

	t.Run("the fix is accepted and its paths named", func(t *testing.T) {
		res := TreeGuard(g.dir, in(fixed, fix))
		if res.Why != "" || !slices.Equal(res.AtStop, []string{"call.txt", "t.txt"}) {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("without the record the fix is refused, as before", func(t *testing.T) {
		if res := TreeGuard(g.dir, in(fixed)); !strings.Contains(res.Why, "differs from the old line merged onto the target") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("content lost elsewhere in a fixed file is refused", func(t *testing.T) {
		lost := strings.Replace(withFix["t.txt"], "feature", "last", 1)
		tip := g.rewritten(t, stopped, map[string]string{"t.txt": lost, "call.txt": "call(x)\n"})
		if res := TreeGuard(g.dir, in(tip, fix)); !strings.Contains(res.Why, "t.txt differs") {
			t.Fatalf("lost content in the fixed file passed: %+v", res)
		}
	})
	t.Run("a change in a file the fix did not touch is refused", func(t *testing.T) {
		stray := map[string]string{"t.txt": withFix["t.txt"], "call.txt": "call(x)\n", "stray.txt": "x\n"}
		tip := g.rewritten(t, stopped, stray)
		if res := TreeGuard(g.dir, in(tip, fix)); !strings.Contains(res.Why, "stray.txt differs") {
			t.Fatalf("a stray file passed: %+v", res)
		}
	})
	t.Run("a fix that does not merge into the old line is refused", func(t *testing.T) {
		early := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature0")})
		clash := g.rewritten(t, early, map[string]string{"t.txt": tText("trunk", "fixed")})
		res := TreeGuard(g.dir, in(clash, g.fixAt(t, early, clash)))
		if !strings.Contains(res.Why, "does not merge cleanly into the old line") {
			t.Fatalf("a fix over the line's own change passed: %+v", res)
		}
	})
}

// A check-stop fix to a path a conflict stop resolved composes with that
// resolution: the new line must hold the resolution with the fix on it, and a
// loss anywhere else in the file is still refused.
func TestTreeGuardComposesACheckStopFixWithAnEarlierResolution(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	resolution := tText("trunk mine", "last")
	rec := g.resolvedAt(t, g.oldTip, resolution)
	rec.Order = 1
	stopped := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution})
	fixedText := strings.Replace(resolution, "mid", "fixed", 1)
	fixed := g.rewritten(t, stopped, map[string]string{"t.txt": fixedText})
	in := func(tip string) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Stopped: []string{"t.txt"},
			Resolved: []StopResolution{rec}, Fixes: []CheckFix{g.fixAt(t, stopped, fixed, 2)}}
	}

	t.Run("the resolution with the fix on it passes", func(t *testing.T) {
		if res := TreeGuard(g.dir, in(fixed)); res.Why != "" || !slices.Contains(res.AtStop, "t.txt") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a line lost elsewhere in the file is refused", func(t *testing.T) {
		lost := g.rewritten(t, stopped, map[string]string{"t.txt": strings.Replace(fixedText, "last", "gone", 1)})
		if res := TreeGuard(g.dir, in(lost)); !strings.Contains(res.Why, "t.txt") {
			t.Fatalf("%+v", res)
		}
	})
}

// A conflict resolution made after a check fix is the newer word on its path:
// the fix is not merged onto it, so a resolution that does not carry the fix
// is not refused for lacking it.
func TestTreeGuardAppliesOnlyTheFixesAfterTheResolution(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	resolution := tText("trunk mine", "last")
	rec := g.resolvedAt(t, g.oldTip, resolution)
	rec.Order = 2
	stopped := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature")})
	fixed := g.rewritten(t, stopped, map[string]string{"t.txt": strings.Replace(tText("trunk", "feature"), "mid", "fixed", 1)})
	tip := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution})
	in := GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Stopped: []string{"t.txt"},
		Resolved: []StopResolution{rec}, Fixes: []CheckFix{g.fixAt(t, stopped, fixed, 1)}}
	if res := TreeGuard(g.dir, in); res.Why != "" || !slices.Contains(res.AtStop, "t.txt") {
		t.Fatalf("%+v", res)
	}
}

// A check-stop fix that changes the mode of a resolved path, or deletes it,
// is applied whole where the resolution is exactly what the fix started
// from; no textual merge is needed or asked for.
func TestTreeGuardAppliesAModeOrDeletionFixToAResolvedPath(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	resolution := tText("trunk mine", "last")
	rec := g.resolvedAt(t, g.oldTip, resolution)
	rec.Order = 1
	stopped := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution})
	in := func(tip string, fix CheckFix) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Stopped: []string{"t.txt"},
			Resolved: []StopResolution{rec}, Fixes: []CheckFix{fix}}
	}

	t.Run("a mode change", func(t *testing.T) {
		gittest.Git(t, g.dir, "checkout", "-q", "--detach", stopped)
		gittest.Git(t, g.dir, "update-index", "--chmod=+x", "t.txt")
		gittest.Git(t, g.dir, "commit", "-q", "-m", "mode")
		fixed := gittest.Git(t, g.dir, "rev-parse", "HEAD")
		if res := TreeGuard(g.dir, in(fixed, g.fixAt(t, stopped, fixed, 2))); res.Why != "" || !slices.Contains(res.AtStop, "t.txt") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a deletion", func(t *testing.T) {
		gittest.Git(t, g.dir, "reset", "-q", "--hard")
		gittest.Git(t, g.dir, "checkout", "-q", "--detach", stopped)
		gittest.Git(t, g.dir, "rm", "-q", "t.txt")
		gittest.Git(t, g.dir, "commit", "-q", "-m", "delete")
		fixed := gittest.Git(t, g.dir, "rev-parse", "HEAD")
		if res := TreeGuard(g.dir, in(fixed, g.fixAt(t, stopped, fixed, 2))); res.Why != "" || !slices.Contains(res.AtStop, "t.txt") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a path the fix did not start from is still refused", func(t *testing.T) {
		gittest.Git(t, g.dir, "reset", "-q", "--hard")
		other := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk other", "last")})
		gittest.Git(t, g.dir, "update-index", "--chmod=+x", "t.txt")
		gittest.Git(t, g.dir, "commit", "-q", "-m", "mode")
		fixed := gittest.Git(t, g.dir, "rev-parse", "HEAD")
		if res := TreeGuard(g.dir, in(fixed, g.fixAt(t, other, fixed, 2))); res.Why == "" {
			t.Fatalf("passed: %+v", res)
		}
	})
}
