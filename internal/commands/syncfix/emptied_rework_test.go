package syncfix

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// resumeBoth is a resume's --json object and its summary.
func resumeBoth(t *testing.T, ctx *kit.Context, work string) (syncrun.RunResult, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := SyncResume(ctx, work, opts, &human)
	return synccmdtest.DecodeSyncRun(t, out.Bytes()), human.String(), err
}

// emptiedOf is a participant's emptied list as "subject [layer]" (a layer
// of "-" when none), with each one's unmatched flag.
func emptiedOf(p *syncrun.SyncParticipant) []string {
	out := []string{}
	for _, d := range p.Emptied {
		s := d.Subject + " [" + deref(d.Layer) + "]"
		if d.Unmatched {
			s += " unmatched"
		}
		out = append(out, s)
	}
	return out
}

// handedOverFold is the fold onto trunk of fold_onto_test.go, handed over
// at s3's bump and resolved: a stack with no per-layer step, whose handover
// therefore keeps no layer state.
func handedOverFold(t *testing.T) (*kit.Context, *gittest.StackLine) {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
	foldFixups(t, s)
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	return ctx, s
}

// The resume of a stack that kept no layer state still knows its layers and
// its plan: each commit trunk has is named with its layer, and the fixups
// the fold folded are not named.
func TestSyncResumeOfAStackNamesLayersAndLeavesOutTheFoldedFixups(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, _ := handedOverFold(t)
	res, human, err := resumeBoth(t, ctx, gittest.StackTmp)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	want := []string{"feat(s2): b3.txt [s2]", "feat(s4): d1.txt [s4]", "feat(s4): d2.txt [s4]"}
	if got := emptiedOf(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("emptied %q, want %q\n%s", got, want, human)
	}
	if strings.Contains(human, "fixup!") {
		t.Errorf("a folded fixup is named:\n%s", human)
	}
}

// A run already under way, whose stack record has no plan, is not guessed
// at: the folded fixups are not called drops, they are unmatched, and the
// layers are still named.
func TestSyncResumeOfAnOlderStackRecordReportsFoldedFixupsAsUnmatched(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := handedOverFold(t)
	gitDir, err := wtsync.GitDir(s.Paths[gittest.StackTmp])
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok {
		t.Fatalf("state %v %v", ok, err)
	}
	rec, ok, err := wtsync.ReadStackRecord(ctx.Repo.MainRoot, st.Epoch)
	if err != nil || !ok {
		t.Fatalf("record %v %v", ok, err)
	}
	rec.Replay = nil
	if err := wtsync.WriteStackRecord(ctx.Repo.MainRoot, rec); err != nil {
		t.Fatal(err)
	}
	res, human, _ := resumeBoth(t, ctx, gittest.StackTmp)
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	got := strings.Join(emptiedOf(p), "\n")
	for _, want := range []string{"feat(s2): b3.txt [s2]", "fixup! feat(s2): b1.txt [tmp/stack] unmatched", "fixup! feat(s5): e1.txt [tmp/stack] unmatched"} {
		if !strings.Contains(got, want) {
			t.Errorf("emptied lacks %q:\n%s\n%s", want, got, human)
		}
	}
	for _, d := range p.Emptied {
		if strings.HasPrefix(d.Subject, "fixup! ") && !d.Unmatched {
			t.Errorf("a fixup is called a drop: %+v", d)
		}
	}
}

// The commits an --edit amend empties on purpose are reported by the edit,
// and are not named a second time as drops.
func TestSyncRebaseStackEditDoesNotNameTheCommitsTheAmendEmptied(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, true)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	done, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, human)
	}
	if !strings.Contains(human, "emptied and dropped: ") {
		t.Fatalf("the edit does not name the emptied commit:\n%s", human)
	}
	if strings.Contains(human, "dropped as empty") || strings.Contains(human, "not found after") {
		t.Errorf("the emptied commit is named twice:\n%s", human)
	}
	if p := synccmdtest.Participant(t, done, gittest.EditTmp); len(p.Emptied) != 0 || len(p.TestsOnly) != 0 {
		t.Errorf("emptied %+v testsOnly %+v", p.Emptied, p.TestsOnly)
	}
}

// A layer check that refuses after a rebase finished by hand puts the line
// back: what the discarded rebase did to its commits is not reported.
func TestSyncResumeOfARestoredEditReportsNothingAboutTheDiscardedRebase(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	// The amend carries all of B's fix, so B's replay keeps lib.txt only,
	// which test_paths names, while B itself changed use.txt too.
	c := newEditCaseWith(t, false, logStep+"test_paths: [lib.txt]\n")
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, map[string]string{"use.txt": "use\n"})
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	epoch := c.state(t).Epoch
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	testkit.GitIn(t, c.tmp, "-c", "core.editor=true", "rebase", "--continue")
	testkit.GitIn(t, c.main, "update-ref", wtsync.StackMapRef(epoch, 2), c.tips["e1"])
	res, human, err := c.resume(t)
	if err == nil || !strings.Contains(err.Error(), "no layer moved") {
		t.Fatalf("resume: %v, want no layer moved\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.EditTmp)
	if p.Result != syncrun.ResultRestored {
		t.Fatalf("result %s, want restored\n%s", p.Result, human)
	}
	if strings.Contains(human, "kept only its tests") || len(p.TestsOnly) != 0 || len(p.Emptied) != 0 {
		t.Errorf("a report about a discarded result:\n%s\nemptied %+v testsOnly %+v", human, p.Emptied, p.TestsOnly)
	}
}

// A fixup that conflicts at a handover and is skipped by hand
// (git rebase --skip) was never folded: the plan still lists it, but the
// report names it as unmatched instead of hiding it.
func TestSyncResumeAfterASkippedFixupNamesItAsUnmatched(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
	tmp := s.Paths[gittest.StackTmp]
	// A commit after s5 changes c1.txt, and a fixup for s3's c1.txt commit
	// changes it again: folded back to its target, the fixup does not apply.
	testkit.WriteFile(t, tmp, "c1.txt", "x1\n")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: touch c1")
	testkit.WriteFile(t, tmp, "c1.txt", "fixed\n")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s3): c1.txt")
	fixup := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if out, err := exec.Command("git", "-C", tmp, "rebase", "--skip").CombinedOutput(); err != nil && !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("skip: %v\n%s", err, out)
	}
	// What is left to resolve is s3's bump, as in the other fold tests.
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
		testkit.GitIn(t, tmp, "add", "v.txt")
	}
	res, human, _ := resumeBoth(t, ctx, gittest.StackTmp)
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	var got *syncrun.DroppedJSON
	for i := range p.Emptied {
		if p.Emptied[i].Commit == fixup {
			got = &p.Emptied[i]
		}
	}
	if got == nil || !got.Unmatched {
		t.Fatalf("emptied %+v: the skipped fixup must be named, unmatched\n%s", p.Emptied, human)
	}
}
