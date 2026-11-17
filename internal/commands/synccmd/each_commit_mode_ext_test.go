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

func eachCommit(mode string) (opts synccmd.RunOptions) {
	opts = synccmdtest.NoAgents()
	opts.EachCommit = mode
	return opts
}

// The line a CheckedLine run stops on, with what the stop left: the
// handover's state and plan are read from the line's worktree.
func stoppedAt(t *testing.T, mode string) (subjects []string, human string, st wtsync.State, plan string, p *syncrun.SyncParticipant) {
	t.Helper()
	ctx, s, logPath := synccmdtest.CheckedLine(t)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, eachCommit(mode))
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("mode %q: err %v\n%s", mode, err, human)
	}
	gitDir, gerr := wtsync.GitDir(s.Paths[gittest.StackTmp])
	if gerr != nil {
		t.Fatal(gerr)
	}
	st, ok, rerr := wtsync.ReadState(gitDir)
	if rerr != nil || !ok {
		t.Fatalf("state %v %v", ok, rerr)
	}
	planBytes, perr := os.ReadFile(wtsync.PlanPath(gitDir))
	if perr != nil {
		t.Fatal(perr)
	}
	plan = string(planBytes)
	return synccmdtest.CheckedSubjects(t, logPath), human, st, plan, synccmdtest.Participant(t, res, gittest.StackTmp)
}

// Under all, the check stops at the second commit of layer 2: the commit
// that adds b1.txt, before the line's own fix could have come.
func TestEachCommitAllStopsAtTheFirstCommitThatFails(t *testing.T) {
	t.Parallel()
	skipShort(t)
	subjects, human, _, _, _ := stoppedAt(t, "all")
	want := []string{"feat(s1): a1.txt", "feat(s1): a2.txt", "feat(s2): b1.txt"}
	if !slices.Equal(subjects, want) {
		t.Errorf("checked %q, want %q\n%s", subjects, want, human)
	}
}

// Under tips, the check runs at the end of each layer: layer 1's tip, and at
// layer 2 the last commit of it the rebase kept (its own tip is on trunk).
// The first commit that fails is not looked at.
func TestEachCommitTipsStopsAtTheLayersTip(t *testing.T) {
	t.Parallel()
	skipShort(t)
	subjects, human, st, plan, p := stoppedAt(t, "tips")
	want := []string{"feat(s1): a2.txt", "feat(s2): b2.txt"}
	if !slices.Equal(subjects, want) {
		t.Errorf("checked %q, want %q\n%s", subjects, want, human)
	}
	if st.Check == nil || st.Check.Subject != "feat(s2): b2.txt" {
		t.Errorf("state check %+v", st.Check)
	}
	if st.EachCommit != "tips" {
		t.Errorf("state each-commit %q, want tips", st.EachCommit)
	}
	if !strings.Contains(plan, "each_commit checks run at the tip of each layer only (--each-commit=tips)") {
		t.Errorf("plan does not name the mode:\n%s", plan)
	}
	if p.EachCommit != "tips" {
		t.Errorf("json eachCommit %q, want tips", p.EachCommit)
	}
}

// Under off nothing is checked, the run finishes, and it says so.
func TestEachCommitOffRunsNoCheck(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, logPath := synccmdtest.CheckedLine(t)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, eachCommit("off"))
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	if got := synccmdtest.CheckedSubjects(t, logPath); len(got) != 0 {
		t.Errorf("checks ran at %q", got)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebased || p.Checked != 0 || p.EachCommit != "off" {
		t.Errorf("participant %+v", p)
	}
	if !strings.Contains(human, "each_commit checks off (--each-commit=off)") {
		t.Errorf("the summary does not say the checks were off:\n%s", human)
	}
}

// A layer where only a middle commit touches the check's paths is still
// checked at its tip.
func TestEachCommitTipsChecksAtTheTipWhenOnlyAMiddleCommitTouchesThePaths(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, logPath := synccmdtest.CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n    paths: [b1.txt]\n")
	if err := os.WriteFile(logPath+".pass", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, eachCommit("tips"))
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	// b1.txt is layer 2's first commit; layer 2's check is at its tip, b2.
	want := []string{"feat(s2): b2.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
}

// A mode no run knows is refused before anything is touched.
func TestEachCommitRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.CheckedLine(t)
	_, _, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, eachCommit("sometimes"))
	if err == nil || !strings.Contains(err.Error(), "--each-commit") {
		t.Fatalf("err %v", err)
	}
	if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", gittest.StackTmp); got != s.Tips[gittest.StackTmp] {
		t.Error("the line moved")
	}
}
