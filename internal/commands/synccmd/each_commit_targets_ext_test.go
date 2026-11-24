package synccmd_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// targetsFold is --stack --autosquash, in place, under --each-commit=targets.
func targetsFold() synccmd.RunOptions {
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "targets"
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	return opts
}

// The issue's shape: a fixup at the top of the line for a commit in layer 2.
// Under targets the check runs at each layer's tip and at the commit the
// fixup folded into, and nowhere between. Layer 1 lies below that target and
// comes out as it went in, so its tip is not checked either. The check fails
// at that target (b1.txt without fix.txt), so the run stops there, before
// layer 2's tip.
func TestEachCommitTargetsChecksTheFoldTargetAndTheTips(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "")
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	want := []string{"feat(s2): b1.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
	gitDir, gerr := wtsync.GitDir(s.Paths[gittest.StackTmp])
	if gerr != nil {
		t.Fatal(gerr)
	}
	st, ok, rerr := wtsync.ReadState(gitDir)
	if rerr != nil || !ok {
		t.Fatalf("state %v %v", ok, rerr)
	}
	if st.EachCommit != "targets" || st.Check == nil || st.Check.Subject != "feat(s2): b1.txt" {
		t.Errorf("state each-commit %q, check %+v", st.EachCommit, st.Check)
	}
	plan, perr := os.ReadFile(wtsync.PlanPath(gitDir))
	if perr != nil {
		t.Fatal(perr)
	}
	if !strings.Contains(string(plan), "each_commit checks run at the commits a fixup folded into and at the tip of each layer (--each-commit=targets)") {
		t.Errorf("the plan does not name the mode:\n%s", plan)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.EachCommit != "targets" {
		t.Errorf("json eachCommit %q, want targets", p.EachCommit)
	}
}

// rebased asserts the line was rebased, with the two layers the fixture holds
// (s3's dirty worktree, s5's busy one) as the only thing the run did not move.
func rebased(t *testing.T, res syncrun.RunResult, err error, human string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "2 layers held") || strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want only the two held layers\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultRebased || p.EachCommit != "targets" {
		t.Fatalf("participant %s %q, want rebased under targets\n%s", p.Result, p.EachCommit, human)
	}
}

// With every check passing, the run finishes: the checks ran at each layer's
// tip and at the target (b1, which took the fixup), once each, and at no
// other commit: not at b2, and not at layer 1's tip, which the run left as it was.
func TestEachCommitTargetsFinishesWithTheTargetAndTipChecksOnly(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "")
	passing(t, logPath)
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, targetsFold())
	rebased(t, res, err, human)
	want := []string{"feat(s2): b1.txt", "feat(s2): b3.txt", "chore(s3): bump to 1.0.1", "feat(s4): d2.txt", "feat(s5): e3.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
	if !strings.Contains(human, "(--each-commit=targets): the commits between are not checked, and are not owed") {
		t.Errorf("the summary does not name the mode:\n%s", human)
	}
}

// A target whose folded change does not touch the step's paths is not
// checked there. Layer 1's tip touches a1.txt, but the run leaves layer 1 as
// it was, so nothing is checked.
func TestEachCommitTargetsDoesNotCheckATargetOutsideTheStepsPaths(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "[a1.txt]")
	passing(t, logPath)
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, targetsFold())
	rebased(t, res, err, human)
	want := []string{}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
}

// targets is for an in-place fold: --onto and a run without --autosquash are
// refused with the reason, naming tips; an edit run is told to use off, as
// tips would tell it, since tips refuses --edit too. Nothing moves.
func TestEachCommitTargetsRefusesARunThatIsNotAnInPlaceFold(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		name          string
		mk            func(s *gittest.StackLine) synccmd.RunOptions
		advice, never string
	}{
		{"onto", func(*gittest.StackLine) synccmd.RunOptions {
			opts := foldOnto("main")
			opts.EachCommit = "targets"
			return opts
		}, "--each-commit=tips", ""},
		{"no autosquash", func(*gittest.StackLine) synccmd.RunOptions { return eachCommit("targets") }, "--each-commit=tips", ""},
		{"edit", func(s *gittest.StackLine) synccmd.RunOptions {
			opts := eachCommit("targets")
			opts.Stack = &synccmd.StackOptions{Edit: s.Tips["s2"]}
			return opts
		}, "--each-commit=off", "--each-commit=tips"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, s, _ := synccmdtest.InPlaceCheckedLine(t, "")
			_, _, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, tc.mk(s))
			if err == nil || !strings.Contains(err.Error(), "--each-commit=targets") || !strings.Contains(err.Error(), tc.advice) {
				t.Fatalf("err %v, want it to name %s", err, tc.advice)
			}
			if tc.never != "" && strings.Contains(err.Error(), tc.never) {
				t.Errorf("err %v recommends %s, which refuses this run too", err, tc.never)
			}
			for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
				if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b); got != s.Tips[b] {
					t.Errorf("%s moved to %s", b, got)
				}
			}
			if refs := testkit.GitOut(t, ctx.Repo.MainRoot, "for-each-ref", "refs/wt-sync/"); refs != "" {
				t.Errorf("a safety ref was written:\n%s", refs)
			}
		})
	}
}
