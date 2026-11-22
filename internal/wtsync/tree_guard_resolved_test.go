package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// resolvedAt is the resolution of t.txt a person recorded at the stop of the
// old line's commit at: content, stored as a blob.
func (g guardRepo) resolvedAt(t *testing.T, at, content string) StopResolution {
	t.Helper()
	f := filepath.Join(t.TempDir(), "resolution")
	gittest.WriteFile(t, f, content)
	blob := gittest.Git(t, g.dir, "hash-object", "-w", f)
	return StopResolution{Commit: at, Path: "t.txt", Mode: "100644", Blob: blob}
}

// A path a person resolved at a stop is theirs whole: the guard takes the
// content they resolved to where the merge keeps a line outside the
// conflict that the resolution dropped, and nothing else.
func TestTreeGuardTakesWhatAStopResolved(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	resolution := tText("trunk mine", "last")
	rec := g.resolvedAt(t, g.oldTip, resolution)
	in := func(tip string, r ...StopResolution) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: tip, Stopped: []string{"t.txt"}, Resolved: r}
	}
	good := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution})

	t.Run("the resolution is taken", func(t *testing.T) {
		res := TreeGuard(g.dir, in(good, rec))
		if res.Why != "" || len(res.AtStop) != 1 || res.AtStop[0] != "t.txt" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("without the record the merge's lines hold, as before", func(t *testing.T) {
		if res := TreeGuard(g.dir, in(good)); !strings.Contains(res.Why, "t.txt") {
			t.Fatalf("a resolution with no record passed: %+v", res)
		}
	})
	t.Run("a path no stop resolved is still held to the merge", func(t *testing.T) {
		tip := g.rewritten(t, g.onto, map[string]string{"t.txt": resolution, "gen/out.txt": "other\n"})
		if res := TreeGuard(g.dir, in(tip, rec)); !strings.Contains(res.Why, "gen/out.txt") {
			t.Fatalf("a changed path no stop had passed: %+v", res)
		}
	})
	t.Run("a resolution changed by hand after the stop is refused", func(t *testing.T) {
		tip := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk mine, edited", "last")})
		res := TreeGuard(g.dir, in(tip, rec))
		if !strings.Contains(res.Why, "t.txt") || !strings.Contains(res.Why, "resolved it to") {
			t.Fatalf("a hand edit after the stop passed: %+v", res)
		}
	})
	t.Run("a record for another path takes nothing for this one", func(t *testing.T) {
		other := rec
		other.Path = "gen/out.txt"
		if res := TreeGuard(g.dir, in(good, other)); res.Why == "" {
			t.Fatalf("a record for gen/out.txt took t.txt: %+v", res)
		}
	})
}

// What the old line changes in a resolved path after the stop's commit still
// comes onto the person's resolution; a new line that lacks it is refused.
func TestTreeGuardCarriesLaterChangesOntoAStopsResolution(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	// A later commit of the old line changes a line far from the conflict.
	later := midChanged(tText("mine", "feature"))
	gittest.Git(t, g.dir, "checkout", "-q", "line")
	gittest.WriteFile(t, g.dir+"/t.txt", later)
	gittest.Git(t, g.dir, "commit", "-q", "-a", "-m", "later")
	tip := gittest.Git(t, g.dir, "rev-parse", "HEAD")
	rec := g.resolvedAt(t, g.oldTip, tText("trunk mine", "last"))
	in := func(newTip string) GuardInput {
		return GuardInput{Base: g.base, Onto: g.onto, OldTip: tip, NewTip: newTip, Stopped: []string{"t.txt"}, Resolved: []StopResolution{rec}}
	}
	carried := g.rewritten(t, g.onto, map[string]string{"t.txt": midChanged(tText("trunk mine", "last"))})
	if res := TreeGuard(g.dir, in(carried)); res.Why != "" || len(res.AtStop) != 1 {
		t.Errorf("the later change on the resolution is refused: %+v", res)
	}
	dropped := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk mine", "last")})
	if res := TreeGuard(g.dir, in(dropped)); !strings.Contains(res.Why, "t.txt") {
		t.Errorf("a resolution that lost the old line's later change passed: %+v", res)
	}
}

// midChanged changes a line in the middle of t.txt, far from its first and
// last lines, so it merges with changes to either.
func midChanged(text string) string {
	lines := strings.Split(text, "\n")
	lines[5] = "later"
	return strings.Join(lines, "\n")
}

// A recorded path whose expected content cannot be rebuilt is refused, never
// handed to the looser check: here the old line's later change sits next to
// the conflict, so it does not merge onto the resolution cleanly.
func TestTreeGuardRefusesWhenAResolutionCannotBeRebuilt(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	adjacent := strings.Replace(tText("mine", "feature"), "mid\n", "later\n", 1)
	gittest.Git(t, g.dir, "checkout", "-q", "line")
	gittest.WriteFile(t, g.dir+"/t.txt", adjacent)
	gittest.Git(t, g.dir, "commit", "-q", "-a", "-m", "later")
	tip := gittest.Git(t, g.dir, "rev-parse", "HEAD")
	rec := g.resolvedAt(t, g.oldTip, tText("trunk mine", "last"))
	// Keeps every line of the merge outside the conflict, so the looser
	// check would pass it.
	newTip := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("anything", "feature")})
	res := TreeGuard(g.dir, GuardInput{Base: g.base, Onto: g.onto, OldTip: tip, NewTip: newTip, Stopped: []string{"t.txt"}, Resolved: []StopResolution{rec}})
	if !strings.Contains(res.Why, "t.txt") || !strings.Contains(res.Why, "rebuilt") {
		t.Fatalf("a resolution that cannot be rebuilt passed: %+v", res)
	}
}

// A recorded path is held to its record even where the new line matches the
// ordinary merge, which then holds nothing of the resolution.
func TestTreeGuardChecksARecordedPathThatMatchesTheMerge(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, false)
	rec := g.resolvedAt(t, g.oldTip, tText("trunk mine", "last"))
	merged := g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature")})
	res := TreeGuard(g.dir, GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: merged, Stopped: []string{"t.txt"}, Resolved: []StopResolution{rec}})
	if !strings.Contains(res.Why, "t.txt") {
		t.Fatalf("a recorded path that dropped its resolution passed: %+v", res)
	}
	// A declared step path stays the step's.
	res = TreeGuard(g.dir, GuardInput{Base: g.base, Onto: g.onto, OldTip: g.oldTip, NewTip: merged, Folds: []string{"t.txt"}, Resolved: []StopResolution{rec}})
	if res.Why != "" {
		t.Fatalf("a step path was held to a record: %+v", res)
	}
}

// A path git quotes (non-ASCII) is compared as itself.
func TestTreeGuardTakesAResolutionOfAnUnusualFileName(t *testing.T) {
	t.Parallel()
	g := newGuardRepo(t, true)
	const name = "résumé.txt"
	commit := func(msg, content string) string {
		gittest.WriteFile(t, g.dir+"/"+name, content)
		gittest.Git(t, g.dir, "add", "-A")
		gittest.Git(t, g.dir, "commit", "-q", "-m", msg)
		return gittest.Git(t, g.dir, "rev-parse", "HEAD")
	}
	gittest.Git(t, g.dir, "checkout", "-q", "main")
	base := commit("name base", tText("first", "last"))
	gittest.Git(t, g.dir, "checkout", "-q", "-b", "line2")
	oldTip := commit("name feature", tText("mine", "feature"))
	gittest.Git(t, g.dir, "checkout", "-q", "main")
	onto := commit("name trunk", tText("trunk", "last"))
	f := filepath.Join(t.TempDir(), "resolution")
	resolution := tText("trunk mine", "last")
	gittest.WriteFile(t, f, resolution)
	rec := StopResolution{Commit: oldTip, Path: name, Mode: "100644", Blob: gittest.Git(t, g.dir, "hash-object", "-w", f)}
	newTip := g.rewritten(t, onto, map[string]string{name: resolution})
	res := TreeGuard(g.dir, GuardInput{Base: base, Onto: onto, OldTip: oldTip, NewTip: newTip, Stopped: []string{name}, Resolved: []StopResolution{rec}})
	if res.Why != "" || len(res.AtStop) != 1 || res.AtStop[0] != name {
		t.Fatalf("%+v", res)
	}
}
