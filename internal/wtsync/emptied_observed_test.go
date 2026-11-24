package wtsync

import (
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A plan wt watched from its start names the folds it saw run, and only
// those are left out of the report: the fixup's change being in the result
// does not show its fold ran, since a person's skip leaves the same change
// where another commit put it.
func TestAWatchedPlanHidesOnlyTheFoldsItSawRun(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	fixup := gitIn(t, tmp, "rev-parse", "HEAD")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := rebaseLine(t, s, true, true, 64)
	if res.Replay == nil || !res.Replay.Observed {
		t.Fatalf("plan %+v: want a plan wt watched", res.Replay)
	}
	report := func(plan *ReplayPlan) string {
		rep, err := ReportReplay(tmp, ReplayInput{Cutoff: "origin/main", Old: line.Head, Onto: "origin/main", New: res.NewTip, Line: &line, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range rep.Emptied {
			if d.Commit == fixup {
				return map[bool]string{true: "unmatched", false: "emptied"}[d.Unmatched]
			}
		}
		return "hidden"
	}
	// The fixup's change is in the result in all three.
	if got := report(&ReplayPlan{Folds: res.Replay.Folds, Observed: true}); got != "unmatched" {
		t.Errorf("a watched fold wt did not see run: %s, want unmatched", got)
	}
	if got := report(&ReplayPlan{Folds: res.Replay.Folds, Observed: true, Folded: res.Replay.Folds}); got != "hidden" {
		t.Errorf("a watched fold wt saw run: %s, want hidden", got)
	}
	if got := report(&ReplayPlan{Folds: res.Replay.Folds}); got != "unmatched" {
		t.Errorf("a plan without evidence, the change in the result: %s, want unmatched", got)
	}
}
