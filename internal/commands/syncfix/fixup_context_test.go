package syncfix

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// ctxGapLines is the two lines the case's fixups are named with.
func ctxGapLines(s *gittest.ContextLine) []string {
	return []string{
		`fixup moved ahead of its context: src/state.ts:13 needs ` + git.ShortID(s.Commits[gittest.CtxOpen], 7) + ` "feat(l2): open flag", which comes later on the line`,
		`fixup moved ahead of its context: src/page.test.ts:10 needs ` + git.ShortID(s.Commits[gittest.CtxPlacement], 7) + ` "test(l2): placement block", which comes later on the line`,
	}
}

// checkGaps checks a --json stack names both fixups of the case.
func checkGaps(t *testing.T, what string, st *syncrun.Stack, s *gittest.ContextLine) {
	t.Helper()
	if st == nil {
		t.Fatalf("%s: no stack", what)
	}
	want := ctxGapLines(s)
	if len(st.ContextGaps) != len(want) {
		t.Fatalf("%s: gaps %+v, want %d", what, st.ContextGaps, len(want))
	}
	fixups := []string{s.Commits[gittest.CtxFixState], s.Commits[gittest.CtxFixTest]}
	needs := []string{s.Commits[gittest.CtxOpen], s.Commits[gittest.CtxPlacement]}
	for i, g := range st.ContextGaps {
		if g.Message != want[i] || g.Fixup != fixups[i] || g.Needs != needs[i] {
			t.Errorf("%s: gap %d %+v, want %q of %s needing %s", what, i, g, want[i], fixups[i], needs[i])
		}
	}
}

// refsNow is every ref of the repository with its commit.
func refsNow(t *testing.T, main string) string {
	t.Helper()
	return testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)")
}

// worktreeNow is what a refusal must leave as it was in a worktree: HEAD,
// the index, the status and every tracked and untracked file's content.
func worktreeNow(t *testing.T, path string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(testkit.GitOut(t, path, "rev-parse", "HEAD") + "\n")
	b.WriteString(testkit.GitOut(t, path, "ls-files", "--stage") + "\n")
	b.WriteString(testkit.GitOut(t, path, "status", "--porcelain=v1", "--untracked-files=all") + "\n")
	for _, f := range []string{"src/state.ts", "src/page.test.ts", "other.txt", "scratch.txt"} {
		data, _ := os.ReadFile(filepath.Join(path, f))
		b.WriteString(f + ":\n" + string(data))
	}
	gitDir, err := wtsync.GitDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{wtsync.StateName, wtsync.PlanName, "rebase-merge/git-rebase-todo", "rebase-merge/done"} {
		data, _ := os.ReadFile(filepath.Join(gitDir, f))
		b.WriteString(f + ":\n" + string(data))
	}
	return b.String()
}

// undoesExactly checks wt sync undo puts back the line and every layer as
// the fixture left them, with the worktree clean on the line's branch.
func undoesExactly(t *testing.T, ctx *kit.Context, s *gittest.ContextLine, before string) {
	t.Helper()
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.ContextTmp]
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.ContextTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != before {
		t.Errorf("undo: the branches are\n%s\nwant\n%s", got, before)
	}
	if ref := testkit.GitOut(t, tmp, "symbolic-ref", "HEAD"); ref != "refs/heads/"+gittest.ContextTmp {
		t.Errorf("undo: HEAD is %s", ref)
	}
	if st := testkit.GitOut(t, tmp, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("undo: the worktree is not clean:\n%s", st)
	}
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		t.Error("undo: a rebase is still in progress")
	}
}

// The case of #156 end to end: a line of three layers whose two fixups were
// written at its top. The look, with and without --onto, and the run name
// both before anything replays; the run still goes ahead and stops at the
// first, and the stop and its plan say why and how out of it, with the
// commit that carries the missing context; undo puts it all back.
func TestFoldNamesTheFixupsAheadOfTheirContext(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.ContextLine(t, true)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.ContextTmp]
	synccmdtest.Branch(t, ctx, "integ", "main", map[string]string{"i.txt": "i\n"})
	branches := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	want := ctxGapLines(s)

	// The look: wt sync tmp/ctx --stack --autosquash, as JSON and for a person.
	look := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.ContextTmp, Autosquash: true}}
	checkGaps(t, "look", synccmdtest.OverviewJSONWith(t, ctx, look).Stack, s)
	var human bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.ContextTmp, look, &human); err != nil {
		t.Fatalf("look: %v\n%s", err, human.String())
	}
	for _, l := range want {
		if !strings.Contains(human.String(), l) {
			t.Errorf("look: %q not said:\n%s", l, human.String())
		}
	}
	// Without --autosquash nothing is folded, so nothing is said.
	plain := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.ContextTmp}}
	if st := synccmdtest.OverviewJSONWith(t, ctx, plain).Stack; st == nil || len(st.ContextGaps) != 0 {
		t.Errorf("look without --autosquash: %+v, want no gaps", st)
	}
	// --onto another branch: the same fixups, the same commits.
	onto := look
	onto.Onto, onto.Work = "integ", gittest.ContextTmp
	checkGaps(t, "look --onto", synccmdtest.OverviewJSONWith(t, ctx, onto).Stack, s)
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != branches {
		t.Fatalf("the look moved a branch:\n%s\nwant\n%s", got, branches)
	}

	// The run: warned before the first stop, then stopped at the first fixup.
	res, out, err := synccmdtest.StackRun(t, ctx, gittest.ContextTmp, runAt(100, &synccmd.StackOptions{Autosquash: true}))
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want the first fixup handed over\n%s", err, out)
	}
	p := synccmdtest.Participant(t, res, gittest.ContextTmp)
	if p.Result != syncrun.ResultHandedOver {
		t.Fatalf("run: %+v\n%s", p, out)
	}
	checkGaps(t, "run", p.Stack, s)
	stop := strings.Index(out, "stop ")
	for _, l := range want {
		if at := strings.Index(out, l); at < 0 || stop < 0 || at > stop {
			t.Errorf("run: %q not said before the first stop (%d, stop at %d):\n%s", l, at, stop, out)
		}
	}
	retarget := "not a resolution: wt sync undo tmp/ctx, then move that hunk of " + git.ShortID(s.Commits[gittest.CtxFixState], 7) +
		" into a fixup of " + git.ShortID(s.Commits[gittest.CtxOpen], 7)
	if after := out[max(stop, 0):]; !strings.Contains(after, want[0]) || !strings.Contains(after, retarget) {
		t.Errorf("run: the stop does not say why and how out:\n%s", after)
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := os.ReadFile(wtsync.PlanPath(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "src/state.ts") || !strings.Contains(string(plan), want[0]) || !strings.Contains(string(plan), retarget) {
		t.Errorf("plan does not name the missing context:\n%s", plan)
	}
	if strings.Contains(string(plan), "same hunk both sides") {
		t.Errorf("plan still calls it an ordinary conflict:\n%s", plan)
	}
	if strings.Contains(string(plan), want[1]) {
		t.Errorf("plan names the other fixup, which is not this stop's:\n%s", plan)
	}

	undoesExactly(t, ctx, s, branches)
}

// probeUnchecked has the analysis say, besides what it finds, that one
// fixup could not be analysed, for the test's length; it returns the line.
func probeUnchecked(t *testing.T) string {
	t.Helper()
	line := "probe: not analysed"
	was := syncrun.ContextGapsOf
	syncrun.ContextGapsOf = func(dir string, commits []string, cfg *wtsync.Config) ([]wtsync.ContextGap, []string, error) {
		gaps, unchecked, err := was(dir, commits, cfg)
		return gaps, append(unchecked, line), err
	}
	t.Cleanup(func() { syncrun.ContextGapsOf = was })
	return line
}

// A finished fold keeps, in its report, what it could not analyse.
func TestAFinishedFoldKeepsWhatItCouldNotAnalyse(t *testing.T) {
	// Not parallel: it swaps syncrun.ContextGapsOf.
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	probe := probeUnchecked(t)
	ctx, _ := synccmdtest.FoldLine(t, gittest.FoldYAML("max-plus-patch"))
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, runAt(500, &synccmd.StackOptions{Autosquash: true}))
	if err == nil || !strings.Contains(err.Error(), "1 layer held") {
		t.Fatalf("fold: err %v, want f3 held\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.FoldTmp)
	if p.Stack == nil || !slices.Contains(p.Stack.ContextUnchecked, probe) {
		t.Errorf("fold: stack %+v, want %q in contextUnchecked", p.Stack, probe)
	}
}

// handedAtFixup runs the fold of a ContextLine with one fixup, which stops
// at that fixup, and resolves the stop with state, staged; scratch.txt is a
// person's untracked file in the worktree.
func handedAtFixup(t *testing.T, nanos int64, state string) (*kit.Context, *gittest.ContextLine, string) {
	t.Helper()
	ctx, s := synccmdtest.ContextLine(t, false)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.ContextTmp]
	branches := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if _, out, err := synccmdtest.StackRun(t, ctx, gittest.ContextTmp, runAt(nanos, &synccmd.StackOptions{Autosquash: true})); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want the fixup handed over\n%s", err, out)
	}
	gittest.WriteFile(t, filepath.Join(tmp, "src/state.ts"), state)
	testkit.GitIn(t, tmp, "add", "src/state.ts")
	gittest.WriteFile(t, filepath.Join(tmp, "scratch.txt"), "mine\n")
	return ctx, s, branches
}

// A resolution that drops the fixup's open: hunk, which no later commit
// brings back, is refused by resume before anything replays, with the path
// and the lines, leaving HEAD, the index, the worktree, the layers and the
// safety refs exactly as they were. The resolution corrected, resume goes
// on; the next stop is an ordinary conflict, resumed as ever; the fold
// finishes with the line's content what it was, and undo puts it back.
func TestResumeRefusesAResolutionThatDropsAFixupsHunk(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	probe := probeUnchecked(t)
	ctx, s, branches := handedAtFixup(t, 200, gittest.StateTS("size: 20,", ""))
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.ContextTmp]
	refs, wt := refsNow(t, main), worktreeNow(t, tmp)

	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	res, err := syncResumeJSON(t, ctx, gittest.ContextTmp, opts)
	wantRefusal := "the resolution drops 1 line of " + git.ShortID(s.Commits[gittest.CtxFixState], 7) + ` "fixup! feat(l1): page size" that the old tip has: src/state.ts:13`
	if err == nil || !strings.Contains(err.Error(), wantRefusal) {
		t.Fatalf("resume: err %v, want %q", err, wantRefusal)
	}
	if !strings.Contains(err.Error(), "nothing is resumed") || !strings.Contains(err.Error(), "--allow-dropped") {
		t.Errorf("resume: %v, want the way on and the way past it", err)
	}
	if p := synccmdtest.Participant(t, res, gittest.ContextTmp); p.Result != syncrun.ResultRefused || !strings.Contains(deref(p.Reason), wantRefusal) {
		t.Errorf("resume: %s %q, want refused with the drop", p.Result, deref(p.Reason))
	}
	if got := refsNow(t, main); got != refs {
		t.Errorf("resume: the refs moved:\n%s\nwant\n%s", got, refs)
	}
	if got := worktreeNow(t, tmp); got != wt {
		t.Errorf("resume: the worktree changed:\n%s\nwant\n%s", got, wt)
	}

	// Corrected: the hunk is in the resolution.
	gittest.WriteFile(t, filepath.Join(tmp, "src/state.ts"), gittest.StateTS("size: 20,", "open: true,"))
	testkit.GitIn(t, tmp, "add", "src/state.ts")
	if _, err := syncResumeJSON(t, ctx, gittest.ContextTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("corrected resume: err %v, want feat(l2): open flag's own conflict handed over", err)
	}
	// An ordinary conflict: open: is the fixup's now, and the commit that
	// adds it is replayed onto it. Its resume is the resume there always was.
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok, err := wtsync.ReadState(gitDir); err != nil || !ok || len(st.Context) != 0 {
		t.Fatalf("the stop at feat(l2): open flag: state %+v %v, want a handover with no fixup context", st.Context, err)
	}
	if plan, _ := os.ReadFile(wtsync.PlanPath(gitDir)); strings.Contains(string(plan), "moved ahead of its context") {
		t.Errorf("the ordinary stop's plan names a fixup:\n%s", plan)
	}
	gittest.WriteFile(t, filepath.Join(tmp, "src/state.ts"), gittest.StateTS("size: 20,", "open: true,"))
	testkit.GitIn(t, tmp, "add", "src/state.ts")
	res, err = syncResumeJSON(t, ctx, gittest.ContextTmp, opts)
	if err != nil {
		t.Fatalf("last resume: %v", err)
	}
	p := synccmdtest.Participant(t, res, gittest.ContextTmp)
	if p.Result != syncrun.ResultRebased || p.Stack == nil || p.Stack.Refusal != nil {
		t.Fatalf("last resume: %+v %+v", p, p.Stack)
	}
	if len(p.Stack.ContextGaps) != 1 || p.Stack.ContextGaps[0].Needs != s.Commits[gittest.CtxOpen] {
		t.Errorf("last resume: the report has gaps %+v, want the fixup's one", p.Stack.ContextGaps)
	}
	if !slices.Contains(p.Stack.ContextUnchecked, probe) {
		t.Errorf("last resume: the report has unchecked %q, want %q kept", p.Stack.ContextUnchecked, probe)
	}
	line := testkit.GitOut(t, main, "rev-parse", gittest.ContextTmp)
	if testkit.GitOut(t, main, "rev-parse", line+"^{tree}") != testkit.GitOut(t, main, "rev-parse", s.Tips[gittest.ContextTmp]+"^{tree}") {
		t.Error("the fold changed the line's content")
	}
	for _, subject := range subjectsOf(t, main, s.Base+".."+line) {
		if strings.HasPrefix(subject, "fixup! ") {
			t.Errorf("the fold left %q", subject)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(tmp, "scratch.txt")); string(got) != "mine\n" {
		t.Errorf("scratch.txt is %q", got)
	}
	undoesExactly(t, ctx, s, branches)
}

// --allow-dropped takes a drop that is meant past the check: the rebase
// goes on, and the content check at its end still holds the layers, since
// the line no longer has what it had.
func TestResumeAllowDroppedLetsAnIntendedDropThrough(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, branches := handedAtFixup(t, 300, gittest.StateTS("size: 20,", ""))
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	opts.AllowDropped = true
	var out, human bytes.Buffer
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := SyncResume(ctx, gittest.ContextTmp, opts, &human)
	res := synccmdtest.DecodeSyncRun(t, out.Bytes())
	if err != nil && strings.Contains(err.Error(), "the resolution drops") {
		t.Fatalf("resume --allow-dropped: refused all the same: %v", err)
	}
	said := "--allow-dropped: the resolution drops 1 line of " + git.ShortID(s.Commits[gittest.CtxFixState], 7)
	if !strings.Contains(human.String(), said) {
		t.Errorf("resume --allow-dropped: the drop it lets through is not said (%q):\n%s", said, human.String())
	}
	tmp := s.Paths[gittest.ContextTmp]
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		t.Fatalf("resume --allow-dropped: the rebase did not go on: %v", err)
	}
	p := synccmdtest.Participant(t, res, gittest.ContextTmp)
	if p.Stack == nil || p.Stack.Refusal == nil || !strings.Contains(*p.Stack.Refusal, "changed the line's content") {
		t.Errorf("resume --allow-dropped: stack %+v, want the content check to hold the layers", p.Stack)
	}
	undoesExactly(t, ctx, s, branches)
}

// Undo right after a refused resume puts the line and every layer back, and
// the person's untracked file stays.
func TestUndoAfterARefusedResumePutsTheLineBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, branches := handedAtFixup(t, 400, gittest.StateTS("size: 20,", ""))
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, gittest.ContextTmp, opts); err == nil || !strings.Contains(err.Error(), "the resolution drops") {
		t.Fatalf("resume: err %v, want the drop refused", err)
	}
	undoesExactly(t, ctx, s, branches)
	if got, _ := os.ReadFile(filepath.Join(s.Paths[gittest.ContextTmp], "scratch.txt")); string(got) != "mine\n" {
		t.Errorf("scratch.txt is %q after the undo", got)
	}
}

// A look whose analysis fails says so, as a run does, and still finishes;
// --json has the failure in contextUnchecked, not an empty contextGaps
// that reads as nothing found.
func TestTheLookSaysWhenItCannotTellTheContext(t *testing.T) {
	// Not parallel: it swaps syncrun.ContextGapsOf.
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, _ := synccmdtest.ContextLine(t, false)
	was := syncrun.ContextGapsOf
	syncrun.ContextGapsOf = func(string, []string, *wtsync.Config) ([]wtsync.ContextGap, []string, error) {
		return nil, nil, errors.New("merge-tree: out of cheese")
	}
	t.Cleanup(func() { syncrun.ContextGapsOf = was })
	look := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.ContextTmp, Autosquash: true}}
	note := "which fixups lack their context could not be read: merge-tree: out of cheese"
	st := synccmdtest.OverviewJSONWith(t, ctx, look).Stack
	if st == nil || len(st.ContextUnchecked) != 1 || !strings.Contains(st.ContextUnchecked[0], note) {
		t.Errorf("look --json: %+v, want the failure in contextUnchecked", st)
	}
	var human bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.ContextTmp, look, &human); err != nil {
		t.Fatalf("look: %v", err)
	}
	if !strings.Contains(human.String(), "note: "+note) {
		t.Errorf("look: the failure is not said:\n%s", human.String())
	}
}
