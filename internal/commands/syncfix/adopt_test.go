package syncfix

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// adoptYAML is CheckedLine's declaration with a deferred step that logs
// that it ran, after every rebase.
func adoptYAML(deferLog string) string {
	return synccmdtest.StackYAML + "each_commit:\n  - run: bin/check.sh\ndefer:\n  - run: echo ran >> '" + deferLog + "'\n"
}

// adoptCase is the issue's case on a fixture: a --stack run stopped at a
// check that failed, finished by hand with git rebase --continue (the
// conflict wt's strategy would have resolved resolved by hand), then
// fixup! commits made on top. finish is the rebase's own result, head the
// tip with the commits on top.
type adoptCase struct {
	ctx               *kit.Context
	s                 *gittest.StackLine
	tmp, gitDir       string
	st                wtsync.State
	finish, head      string
	logPath, deferLog string
}

func newAdoptCase(t *testing.T) *adoptCase {
	t.Helper()
	deferLog := filepath.Join(t.TempDir(), "deferred.log")
	ctx, s, logPath := synccmdtest.CheckedLineYAML(t, adoptYAML(deferLog))
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	c := &adoptCase{ctx: ctx, s: s, tmp: s.Paths[gittest.StackTmp], logPath: logPath, deferLog: deferLog}
	gitDir, err := wtsync.GitDir(c.tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok || st.Check == nil {
		t.Fatalf("ReadState = %+v, %v, %v; the run did not hand over at a check", st, ok, err)
	}
	c.gitDir, c.st = gitDir, st
	finishByHand(t, c.tmp)
	c.finish = testkit.GitOut(t, c.tmp, "rev-parse", "HEAD")
	return c
}

// finishByHand is a person's own git rebase --continue to the end,
// resolving the v.txt bump the strategy would have.
func finishByHand(t *testing.T, dir string) {
	t.Helper()
	for range 10 {
		testkit.GitTry(t, dir, "rebase", "--continue")
		busy, err := wtsync.RebaseInProgress(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !busy {
			return
		}
		testkit.WriteFile(t, dir, "v.txt", "1.0.6\n")
		testkit.GitIn(t, dir, "add", "v.txt")
	}
	t.Fatal("the rebase did not finish by hand")
}

// addFixups makes the adaptation commits on top of the finished rebase, as
// the real case's 13: the fix the bottom layers lack, and another.
func (c *adoptCase) addFixups(t *testing.T) {
	t.Helper()
	testkit.WriteFile(t, c.tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, c.tmp, "add", "fix.txt")
	testkit.GitIn(t, c.tmp, "commit", "-q", "--no-verify", "-m", "fixup! feat(s2): b1.txt")
	testkit.WriteFile(t, c.tmp, "adapt.txt", "adapted\n")
	testkit.GitIn(t, c.tmp, "add", "adapt.txt")
	testkit.GitIn(t, c.tmp, "commit", "-q", "--no-verify", "-m", "fixup! feat(s1): a2.txt")
	c.head = testkit.GitOut(t, c.tmp, "rev-parse", "HEAD")
}

// still is what a refused resume must leave: every branch, the worktree,
// the stash, the handover, the safety ref and no kept ref.
type still struct {
	refs, status, stash, s3 string
}

func (c *adoptCase) snapshot(t *testing.T) still {
	t.Helper()
	main := c.ctx.Repo.MainRoot
	return still{
		refs:   testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)"),
		status: testkit.GitOut(t, c.tmp, "status", "--porcelain", "--untracked-files=all"),
		stash:  testkit.GitOut(t, main, "stash", "list"),
		s3:     testkit.GitOut(t, c.s.Paths["s3"], "status", "--porcelain"),
	}
}

func (c *adoptCase) assertStill(t *testing.T, before still) {
	t.Helper()
	after := c.snapshot(t)
	if after.refs != before.refs {
		t.Errorf("refs moved:\nbefore\n%s\nafter\n%s", before.refs, after.refs)
	}
	if after.status != before.status {
		t.Errorf("the worktree changed: %q, was %q", after.status, before.status)
	}
	if after.stash != before.stash {
		t.Errorf("the stash changed: %q, was %q", after.stash, before.stash)
	}
	if after.s3 != before.s3 {
		t.Errorf("s3's worktree changed: %q, was %q", after.s3, before.s3)
	}
	if _, ok, err := wtsync.ReadState(c.gitDir); err != nil || !ok {
		t.Errorf("ReadState = %v, %v; the refusal removed the handover", ok, err)
	}
	if got := testkit.GitOut(t, c.ctx.Repo.MainRoot, "rev-parse", c.st.Safety); got != c.s.Tips[gittest.StackTmp] {
		t.Errorf("the safety ref is at %s, want %s", got, c.s.Tips[gittest.StackTmp])
	}
	if kept := testkit.GitOut(t, c.ctx.Repo.MainRoot, "for-each-ref", wtsync.EditKeptPrefix); kept != "" {
		t.Errorf("a refusal kept a ref:\n%s", kept)
	}
}

// resumeRaw is resume --json with the object as decoded and as raw JSON.
func resumeRaw(t *testing.T, ctx *kit.Context, opts ResumeOptions) (syncrun.RunResult, map[string]any, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := SyncResume(ctx, gittest.StackTmp, opts, &human)
	var raw map[string]any
	if jerr := json.Unmarshal(out.Bytes(), &raw); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out.String())
	}
	return synccmdtest.DecodeSyncRun(t, out.Bytes()), raw, human.String(), err
}

func adoptOptions() ResumeOptions {
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	opts.Adopt = true
	return opts
}

// subjectAt is the commit on onto..tip with subject: what the test expects a
// layer at. The test may match by subject; the code under test may not.
func subjectAt(t *testing.T, dir, onto, tip, subject string) string {
	t.Helper()
	for _, line := range strings.Split(testkit.GitOut(t, dir, "log", "--format=%H %s", onto+".."+tip), "\n") {
		sha, s, _ := strings.Cut(line, " ")
		if s == subject {
			return sha
		}
	}
	t.Fatalf("no %q on %s..%s", subject, onto, tip)
	return ""
}

// The issue's case end to end: resume refuses the hand-finished line and
// points at --adopt; --adopt keeps the tip, moves the layers by the run's
// markers, names the commits made after the rebase, runs the deferred step
// and the tip checks, and owes the failing ones; undo then puts back the
// line, every layer and the worktree, and the kept ref still holds the
// adopted tip.
func TestSyncResumeAdoptTakesOverALineFinishedByHand(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newAdoptCase(t)
	c.addFixups(t)
	main := c.ctx.Repo.MainRoot
	before := c.snapshot(t)

	plain := noResumeAgents()
	plain.Push = syncrun.PushNever
	var out bytes.Buffer
	err := SyncResume(c.ctx, gittest.StackTmp, plain, &out)
	if err == nil || !strings.Contains(err.Error(), "wt sync resume --adopt tmp/stack") {
		t.Fatalf("err %v, want a refusal that points at --adopt\n%s", err, out.String())
	}
	c.assertStill(t, before)

	if err := os.Remove(c.logPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	res, raw, human, err := resumeRaw(t, c.ctx, adoptOptions())
	if err == nil || !strings.Contains(err.Error(), "not completed") {
		t.Fatalf("err %v, want the failing tip checks owed\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebasedStepFailed {
		t.Fatalf("result %s, want rebasedStepFailed\n%s", p.Result, human)
	}
	// The line keeps the commits made after the rebase.
	if got := testkit.GitOut(t, c.tmp, "rev-list", "--count", c.finish+"..HEAD"); got != "2" {
		t.Errorf("%s commits on top of the rebase, want the 2 fixups", got)
	}
	if got := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD"); got != c.head {
		t.Errorf("the line is at %s, want the adopted tip %s", got, c.head)
	}
	// Kept before anything moved, and named.
	kept := wtsync.AdoptKeptRef(gittest.StackTmp, c.st.Epoch)
	if got := testkit.GitOut(t, main, "rev-parse", kept); got != c.head {
		t.Errorf("%s pins %s, want the adopted tip %s", kept, got, c.head)
	}
	if !strings.Contains(human, kept) {
		t.Errorf("the output does not name %s:\n%s", kept, human)
	}
	// Named as made after the rebase, in the output and in the JSON.
	for _, want := range []string{"adopted: made after the rebase", "fixup! feat(s2): b1.txt", "fixup! feat(s1): a2.txt"} {
		if !strings.Contains(human, want) {
			t.Errorf("the output lacks %q:\n%s", want, human)
		}
	}
	row := raw["worktrees"].([]any)[0].(map[string]any)
	adopted, ok := row["adopted"].(map[string]any)
	if !ok {
		t.Fatalf("no adopted object in %v", row)
	}
	if adopted["keptRef"] != kept || adopted["rebaseTip"] != c.finish {
		t.Errorf("adopted %v", adopted)
	}
	if commits, _ := adopted["commits"].([]any); len(commits) != 2 {
		t.Errorf("adopted commits %v, want the 2 fixups", adopted["commits"])
	}
	// The layers moved by the markers: s1 to its own tip, s2 to its last
	// commit left (trunk carries its tip), s4 emptied onto s3's; s3 and s5
	// are held by their dirty and busy worktrees.
	onto := c.st.Onto
	want := map[string]string{
		"s1": subjectAt(t, main, onto, c.finish, "feat(s1): a2.txt"),
		"s2": subjectAt(t, main, onto, c.finish, "feat(s2): b2.txt"),
		"s4": subjectAt(t, main, onto, c.finish, "chore(s3): bump to 1.0.1"),
	}
	for b, sha := range want {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != sha {
			t.Errorf("%s is at %s, want %s", b, got, sha)
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != c.s.Tips[b] {
			t.Errorf("held %s moved to %s", b, got)
		}
	}
	// The deferred step ran.
	if raw, _ := os.ReadFile(c.deferLog); strings.TrimSpace(string(raw)) != "ran" {
		t.Errorf("the deferred step ran %q, want once", raw)
	}
	// Checked at the tips, and said so: the bottom layers lack the fix
	// that sits in the fixups on top, so their checks are owed.
	checked := synccmdtest.CheckedSubjects(t, c.logPath)
	if len(checked) == 0 {
		t.Fatalf("no tip check ran\n%s", human)
	}
	if !strings.Contains(human, "each layer's tip") {
		t.Errorf("the output does not say the checks ran at the tips:\n%s", human)
	}
	if !strings.Contains(err.Error(), "bin/check.sh") {
		t.Errorf("err %v does not owe the failing check", err)
	}
	failed := strings.Join(p.FailedSteps, "\n")
	if !strings.Contains(failed, "bin/check.sh") || !strings.Contains(failed, "s2") {
		t.Errorf("failed steps %q do not owe s2's check", p.FailedSteps)
	}
	if _, ok, err := wtsync.ReadState(c.gitDir); err != nil || ok {
		t.Errorf("ReadState = %v, %v; the handover survived the adopt", ok, err)
	}

	// Undo puts back the line, every layer and the worktree, and the kept
	// ref still holds the adopted tip.
	stash := testkit.GitOut(t, main, "stash", "list")
	out.Reset()
	if err := SyncUndo(c.ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	undoBack(t, main, c.s, append([]string{gittest.StackTmp}, c.s.Layers...)...)
	if got := testkit.GitOut(t, main, "rev-parse", kept); got != c.head {
		t.Errorf("after the undo %s pins %s, want %s", kept, got, c.head)
	}
	if !strings.Contains(out.String(), kept) {
		t.Errorf("undo does not say where the adopted tip is kept:\n%s", out.String())
	}
	if got := testkit.GitOut(t, c.tmp, "status", "--porcelain"); got != "" {
		t.Errorf("the line's worktree after the undo: %q", got)
	}
	if got := testkit.GitOut(t, c.s.Paths["s3"], "status", "--porcelain"); got != before.s3 {
		t.Errorf("s3's worktree: %q, was %q", got, before.s3)
	}
	if got := testkit.GitOut(t, main, "stash", "list"); got != stash {
		t.Errorf("the stash changed: %q", got)
	}
}

// Each shape --adopt cannot prove is refused, and the refusal moves no ref,
// layer, file, stash or safety ref, and keeps nothing.
func TestSyncResumeAdoptRefusesWhatItCannotProve(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	cases := []struct {
		name, want string
		shape      func(t *testing.T, c *adoptCase)
	}{
		{"a reset after the rebase", "reset: moving", func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			testkit.GitIn(t, c.tmp, "reset", "-q", "--hard", "HEAD~1")
		}},
		{"a rebase of the person's own after it", "the last rebase that finished", func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			testkit.GitIn(t, c.tmp, "-c", "sequence.editor=true", "rebase", "-q", "-i", "--autosquash", c.st.Onto)
		}},
		{"an amend of the rebase's result", "commit (amend)", func(t *testing.T, c *adoptCase) {
			testkit.GitIn(t, c.tmp, "commit", "-q", "--no-verify", "--amend", "-m", "amended")
		}},
		{"no rebase-finish entry", `"rebase (finish)"`, func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			// The entries newest first: the two fixups, then the finish.
			testkit.GitIn(t, c.tmp, "reflog", "delete", "refs/heads/"+gittest.StackTmp+"@{2}")
		}},
		{"a missing marker", "marker", func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			testkit.GitIn(t, c.tmp, "update-ref", "-d", wtsync.StackMapRef(c.st.Epoch, 1))
		}},
		{"a moved marker", "marker", func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			testkit.GitIn(t, c.tmp, "update-ref", wtsync.StackMapRef(c.st.Epoch, 1), c.head)
		}},
		{"a dirty worktree", "not committed", func(t *testing.T, c *adoptCase) {
			c.addFixups(t)
			testkit.WriteFile(t, c.tmp, "adapt.txt", "half done\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newAdoptCase(t)
			tc.shape(t, c)
			before := c.snapshot(t)
			_, _, human, err := resumeRaw(t, c.ctx, adoptOptions())
			if err == nil || !strings.Contains(err.Error(), "nothing is resumed") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want a refusal naming %q\n%s", err, tc.want, human)
			}
			c.assertStill(t, before)
			// A plain resume does not point at an --adopt that would refuse.
			var out bytes.Buffer
			if err := SyncResume(c.ctx, gittest.StackTmp, noResumeAgents(), &out); err != nil && strings.Contains(err.Error(), "--adopt") {
				t.Errorf("a plain resume points at --adopt for a shape it refuses: %v", err)
			}
			c.assertStill(t, before)
		})
	}
}
