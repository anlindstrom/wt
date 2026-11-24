package synccmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
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

// Without --stack the branch is one layer: tips checks it once, at its tip.
func TestEachCommitTipsChecksAPlainBranchAtItsTipOnly(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.CheckedLine(t)
	testkit.GitIn(t, s.Paths["s3"], "checkout", "-q", "--", "c1.txt") // the fixture's dirt
	if err := os.WriteFile(logPath+".pass", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := eachCommit("tips")
	var human strings.Builder
	err := synccmd.SyncRebase(ctx, []string{gittest.StackTmp}, opts, &human)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human.String())
	}
	// Each branch of the chain is rebased on its own: its tip, and no commit
	// below it. tmp/stack is the fourth, at the tip of s5.
	want := []string{"feat(s1): a2.txt", "chore(s3): bump to 1.0.1", "feat(s5): e3.txt", "feat(s5): e3.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human.String())
	}
	if !strings.Contains(human.String(), "the tip of the branch only (--each-commit=tips)") {
		t.Errorf("the summary does not name the mode:\n%s", human.String())
	}
}

// Tips with --autosquash --onto: the fixups are folded into their targets,
// and the checks run at layer tips alone, never at a fixup.
func TestEachCommitTipsWithAutosquashOntoChecksLayerTipsOnly(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.CheckedLine(t)
	if err := os.WriteFile(logPath+".pass", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	opts := foldOnto("main")
	opts.EachCommit = "tips"
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	for _, subject := range synccmdtest.CheckedSubjects(t, logPath) {
		if strings.HasPrefix(subject, "fixup!") {
			t.Errorf("a fixup was checked: %q", subject)
		}
	}
	got := synccmdtest.CheckedSubjects(t, logPath)
	if len(got) == 0 || len(got) > 5 {
		t.Errorf("checked %q: want one check per layer tip, at most five", got)
	}
}

// An edit run under tips is refused before a ref is written.
func TestEachCommitTipsRefusesAnEditRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.CheckedLine(t)
	opts := eachCommit("tips")
	opts.Stack = &synccmd.StackOptions{Edit: s.Tips["s2"]}
	_, _, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "--each-commit=tips and --edit") {
		t.Fatalf("err %v", err)
	}
	for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
		if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("%s moved to %s", b, got)
		}
	}
	if refs := testkit.GitOut(t, ctx.Repo.MainRoot, "for-each-ref", "refs/wt-sync/"); refs != "" {
		t.Errorf("a safety ref was written:\n%s", refs)
	}
}

// passing makes the check script of a CheckedLine pass everywhere.
func passing(t *testing.T, logPath string) {
	t.Helper()
	if err := os.WriteFile(logPath+".pass", nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Only a fixup, folded into a commit in the middle of layer 2, touches the
// step's paths: the layer is checked once, at its tip, and nowhere else.
func TestEachCommitTipsChecksTheLayerAFixupFoldsInto(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n    paths: [zz.txt]\n")
	passing(t, logPath)
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "zz.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "zz.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fixup! feat(s2): b1.txt")
	opts := foldOnto("main")
	opts.EachCommit = "tips"
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	want := []string{"feat(s2): b2.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
}

// A layer's tip that git drops as empty on the target (trunk has the same
// content in a commit with another patch) leaves the layer checked at its
// last commit that stays.
func TestEachCommitTipsChecksTheSurvivingCommitWhenTheTipComesOutEmpty(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, logPath := synccmdtest.CheckedLine(t)
	passing(t, logPath)
	main := ctx.Repo.MainRoot
	testkit.WriteFile(t, main, "e3.txt", "s5 e3.txt\n")
	testkit.WriteFile(t, main, "t.txt", "t3\n")
	testkit.GitIn(t, main, "add", "-A")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: e3 by another patch")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, eachCommit("tips"))
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	want := []string{"feat(s1): a2.txt", "feat(s2): b2.txt", "chore(s3): bump to 1.0.1", "feat(s5): e2.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
}

// Under off a step's need is not asked: a disabled check does not hold the
// run back, and a deferred step's need still would.
func TestEachCommitOffDoesNotAskForTheNeedsOfDisabledChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, logPath := synccmdtest.CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n    needs: [docker]\n")
	docker := synccmdtest.DockerDown()
	opts := eachCommit("off")
	opts.Probes = docker.Probes()
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil && (strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "needs you")) {
		t.Fatalf("refused: %v\n%s", err, human)
	}
	if strings.Contains(human, "Docker") || strings.Contains(human, "docker") {
		t.Errorf("the run speaks of docker:\n%s", human)
	}
	if got := synccmdtest.CheckedSubjects(t, logPath); len(got) != 0 {
		t.Errorf("checks ran at %q", got)
	}
	if docker.Calls() != 0 {
		t.Errorf("docker was probed %d times", docker.Calls())
	}
}

// In place, git keeps the leading picks of a fold as they were, without a
// rewrite to record. Only layer 1 calls for a check, but the run leaves layer
// 1 as it was, so it is not checked.
func TestEachCommitTipsSkipsAFirstLayerGitKeptAsItWas(t *testing.T) {
	t.Parallel()
	skipShort(t)
	logPath := filepath.Join(t.TempDir(), "checked.log")
	yaml := synccmdtest.StackYAML + "each_commit:\n  - run: git log -1 --format=%s >> '" + logPath + "'\n    paths: [a1.txt]\n"
	ctx, s := synccmdtest.StackLine(t, yaml)
	fixup(t, s.Paths[gittest.StackTmp], "d1.txt", "feat(s4): d1.txt")
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "tips"
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	want := []string{}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
}

// B-C-D with a layer at C, onto C-E above B: git empties C, which is still
// reachable from HEAD because the target carries it. The emptied layer has no
// commit of its own, so it is not checked at the target's tip.
func TestEachCommitTipsDoesNotCheckALayerTheTargetAlreadyCarries(t *testing.T) {
	t.Parallel()
	gittest.Need(t, "2.56") // git 2.54 replays the target's own commit and checks it too
	skipShort(t)
	ctx, bump := provisiontest.RunFixture(t, false)
	logPath := filepath.Join(t.TempDir(), "checked.log")
	synccmdtest.DeclareSteps(t, ctx, "each_commit:\n  - run: git log -1 --format=%s >> '"+logPath+"'\n")
	main := ctx.Repo.MainRoot
	b := testkit.GitOut(t, bump, "rev-parse", "HEAD")
	commit := func(file string) string {
		gittest.WriteFile(t, filepath.Join(bump, file), file+"\n")
		testkit.GitOut(t, bump, "add", "-A")
		testkit.GitOut(t, bump, "commit", "-q", "-m", file)
		return testkit.GitOut(t, bump, "rev-parse", "HEAD")
	}
	c := commit("c.txt")
	testkit.GitOut(t, main, "branch", "lc", c)
	commit("d.txt")
	synccmdtest.Branch(t, ctx, "integ", c, map[string]string{"e.txt": "e\n"})
	opts := synccmdtest.Onto("integ", b)
	opts.Stack = &synccmd.StackOptions{Layers: []string{"lc"}}
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, "bump", opts); err != nil {
		t.Fatalf("err %v\n%s", err, human)
	}
	want := []string{"d.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q", got, want)
	}
}
