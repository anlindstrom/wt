package synccmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision"
	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// aloneState is everything a run must leave as it was in a worktree it does
// not touch: its branch, its tree, its index, its files and the stash.
type aloneState struct{ head, index, status, diff, untracked, stash string }

func readIfThere(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

func aloneStateOf(t *testing.T, path, main string) aloneState {
	t.Helper()
	return aloneState{
		head:      testkit.GitOut(t, path, "rev-parse", "HEAD"),
		index:     testkit.GitOut(t, path, "write-tree"),
		status:    testkit.GitOut(t, path, "status", "--porcelain"),
		diff:      testkit.GitOut(t, path, "diff"),
		untracked: readIfThere(path, "untracked.txt"),
		stash:     testkit.GitOut(t, main, "stash", "list", "--format=%H %gs"),
	}
}

// aloneFixture is three worktrees stacked on one history: bump at the bottom,
// child on it and grand on child. child is dirty three ways (a tracked change,
// an untracked file and a stash); bump and grand are clean.
func aloneFixture(t *testing.T) (ctx *kit.Context, bump, child, grand string) {
	t.Helper()
	c, bump := provisiontest.RunFixture(t, false)
	child = provisiontest.StackFixture(t, c)
	var buf bytes.Buffer
	grand, err := provision.New(c, "feat/grand", provision.NewOptions{NoSetup: true, Base: "feat_wt/child"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grand, "g.txt"), []byte("g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitOut(t, grand, "add", "-A")
	testkit.GitOut(t, grand, "commit", "-q", "-m", "grand")
	// The stash is made first, then the dirt that stays.
	if err := os.WriteFile(filepath.Join(child, "c.txt"), []byte("stashed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitOut(t, child, "stash", "push", "-q", "-m", "alone-test")
	if err := os.WriteFile(filepath.Join(child, "c.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "untracked.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return c, bump, child, grand
}

func TestSyncRunAloneRebasesOneWorktreeAndTouchesNoOther(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, bump, child, grand := aloneFixture(t)
	main := ctx.Repo.MainRoot

	// Without --alone the dirty member refuses the whole stack, as ever.
	var refused bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"bump"}, synccmdtest.NoAgents(), &refused); err == nil || !strings.Contains(refused.String(), "child: tracked changes") {
		t.Fatalf("without --alone: err %v\n%s", err, refused.String())
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", "refs/wt-sync/"); refs != "" {
		t.Fatalf("a refused run wrote a safety ref: %s", refs)
	}

	oldBump := testkit.GitOut(t, bump, "rev-parse", "HEAD")
	childBefore, grandBefore := aloneStateOf(t, child, main), aloneStateOf(t, grand, main)
	oldChildBranch := testkit.GitOut(t, main, "rev-parse", "feat_wt/child")

	opts := synccmdtest.NoAgents()
	opts.Alone = true
	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"bump"}, opts, &out); err != nil {
		t.Fatalf("--alone: %v\n%s", err, out.String())
	}
	s := out.String()
	if !testkit.GitAncestor(t, main, "origin/main", "feat_wt/bump") {
		t.Fatalf("bump was not rebased:\n%s", s)
	}
	if strings.Contains(s, "rebasing all of them") {
		t.Errorf("the run took the stack along:\n%s", s)
	}
	for _, want := range []string{"left alone", "child", "grand", "old commits"} {
		if !strings.Contains(s, want) {
			t.Errorf("the output does not say %q:\n%s", want, s)
		}
	}
	if got := aloneStateOf(t, child, main); got != childBefore {
		t.Errorf("the dirty worktree changed:\nbefore %+v\nafter  %+v", childBefore, got)
	}
	if got := aloneStateOf(t, grand, main); got != grandBefore {
		t.Errorf("the third worktree changed:\nbefore %+v\nafter  %+v", grandBefore, got)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "feat_wt/child"); got != oldChildBranch {
		t.Errorf("child's branch moved to %s", got)
	}
	refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", "refs/wt-sync/")
	if !strings.Contains(refs, "feat_wt/bump/") || strings.Contains(refs, "feat_wt/child/") || strings.Contains(refs, "feat_wt/grand/") {
		t.Errorf("safety refs are not bump's alone:\n%s", refs)
	}

	// Undo puts back what the run moved, and nothing else moves.
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, "bump", syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := testkit.GitOut(t, bump, "rev-parse", "HEAD"); got != oldBump {
		t.Errorf("undo left bump at %s, want %s\n%s", got, oldBump, undone.String())
	}
	if st := testkit.GitOut(t, bump, "status", "--porcelain"); st != "" {
		t.Errorf("bump is not clean after undo: %s", st)
	}
	if got := aloneStateOf(t, child, main); got != childBefore {
		t.Errorf("undo changed the dirty worktree:\n%+v", got)
	}
	if got := aloneStateOf(t, grand, main); got != grandBefore {
		t.Errorf("undo changed the third worktree:\n%+v", got)
	}
}

// Naming a worktree that has stack members below it rebases its branch onto
// trunk as a copy of its own commits; the parent is not taken along.
func TestSyncRunAloneOnAMiddleWorktreeLeavesTheOnesAboveAndBelow(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, bump, child, grand := aloneFixture(t)
	// child is the one named here, so it must be clean: put its dirt aside.
	main := ctx.Repo.MainRoot
	testkit.GitOut(t, child, "checkout", "-q", "--", "c.txt")
	if err := os.Remove(filepath.Join(child, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	bumpBefore, grandBefore := testkit.GitOut(t, bump, "rev-parse", "HEAD"), testkit.GitOut(t, grand, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(bump, "untracked.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := synccmdtest.NoAgents()
	opts.Alone = true
	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"child"}, opts, &out); err != nil {
		t.Fatalf("--alone: %v\n%s", err, out.String())
	}
	if got := testkit.GitOut(t, bump, "rev-parse", "HEAD"); got != bumpBefore {
		t.Errorf("the parent moved:\n%s", out.String())
	}
	if got := testkit.GitOut(t, grand, "rev-parse", "HEAD"); got != grandBefore {
		t.Errorf("the child above moved:\n%s", out.String())
	}
	if !testkit.GitAncestor(t, main, "origin/main", "feat_wt/child") {
		t.Errorf("child was not rebased onto trunk:\n%s", out.String())
	}
	if got := read(t, filepath.Join(bump, "untracked.txt")); got != "mine\n" {
		t.Errorf("the parent's untracked file is %q", got)
	}
}

func TestSyncRunAloneNeedsOneWorktreeNamed(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, child, _ := aloneFixture(t)
	before := testkit.GitOut(t, child, "rev-parse", "HEAD")
	for name, works := range map[string][]string{"none": nil, "two": {"bump", "grand"}} {
		opts := synccmdtest.NoAgents()
		opts.Alone = true
		var out bytes.Buffer
		err := synccmd.SyncRebase(ctx, works, opts, &out)
		if err == nil || !strings.Contains(err.Error(), "--alone") {
			t.Errorf("%s named: err %v\n%s", name, err, out.String())
		}
	}
	if got := testkit.GitOut(t, child, "rev-parse", "HEAD"); got != before {
		t.Error("a refused run moved a worktree")
	}
	if refs := testkit.GitOut(t, ctx.Repo.MainRoot, "for-each-ref", "refs/wt-sync/"); refs != "" {
		t.Errorf("a refused run wrote safety refs: %s", refs)
	}
}

func TestSyncRunAloneWithOntoIsRefused(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, _, _ := aloneFixture(t)
	synccmdtest.Integration(t, ctx)
	opts := synccmdtest.Onto("integ", "")
	opts.Alone = true
	var out bytes.Buffer
	err := synccmd.SyncRebase(ctx, []string{"bump"}, opts, &out)
	if err == nil || !strings.Contains(err.Error(), "--onto") {
		t.Fatalf("err %v\n%s", err, out.String())
	}
}

// The look names what --alone would leave, and changes nothing.
func TestSyncLookAloneNamesTheWorktreesItWouldLeave(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, _, _ := aloneFixture(t)
	var out bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "bump", synccmd.SyncOptions{NoFetch: true, Alone: true}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{"--alone", "left alone", "child", "grand"} {
		if !strings.Contains(s, want) {
			t.Errorf("the look does not say %q:\n%s", want, s)
		}
	}
}

// A reference run of a copy of a stacked line: the copy folds its fixups with
// --stack --autosquash, and no layer branch of the real line, nor the worktree
// holding one, is moved, held, reset or touched. wt sync undo puts back the
// copy and nothing else.
func TestSyncRunAloneWithStackFoldsTheCopyAndLeavesEveryLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	tmp := s.Paths[gittest.StackTmp]
	for _, f := range []struct{ file, target string }{{"b1.txt", "feat(s2): b1.txt"}, {"d1.txt", "feat(s4): d1.txt"}} {
		if err := os.WriteFile(filepath.Join(tmp, f.file), []byte("fixed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! "+f.target)
	}
	oldTmp := testkit.GitOut(t, main, "rev-parse", gittest.StackTmp)
	// s1's worktree is clean: give it a staged change, an untracked file and
	// a stash, which a plain --stack run would reset.
	s1 := s.Paths["s1"]
	if err := os.WriteFile(filepath.Join(s1, "a.txt"), []byte("stashed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, s1, "stash", "push", "-q", "-m", "alone-layer")
	if err := os.WriteFile(filepath.Join(s1, "a.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, s1, "add", "a.txt")
	if err := os.WriteFile(filepath.Join(s1, "untracked.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tips := map[string]string{}
	states := map[string]aloneState{}
	for _, b := range s.Layers {
		tips[b] = testkit.GitOut(t, main, "rev-parse", b)
		if path, ok := s.Paths[b]; ok {
			states[b] = aloneStateOf(t, path, main)
		}
	}
	same := func(when string) {
		t.Helper()
		for b, tip := range tips {
			if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
				t.Errorf("%s %s: layer %s moved to %s", when, b, b, got)
			}
		}
		for b, before := range states {
			if got := aloneStateOf(t, s.Paths[b], main); got != before {
				t.Errorf("%s: worktree of %s changed:\nbefore %+v\nafter  %+v", when, b, before, got)
			}
		}
	}

	opts := synccmdtest.NoAgents()
	opts.Alone = true
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", s.Base+".."+gittest.StackTmp); n != "12" {
		t.Errorf("the copy has %s commits from its base, want 12: both fixups folded\n%s", n, human)
	}
	same("after the run")
	for _, b := range s.Layers {
		if !strings.Contains(human, "left alone: "+b) && !strings.Contains(human, "left alone: "+kitWork(b)) {
			t.Errorf("the output does not name layer %s as left alone:\n%s", b, human)
		}
	}
	if strings.Contains(human, "held") || strings.Contains(human, "worktree reset") || strings.Contains(human, "branch moved") {
		t.Errorf("a layer was moved or held:\n%s", human)
	}
	left := map[string]bool{}
	for _, l := range res.LeftAlone {
		left[l.Branch] = true
	}
	for _, b := range s.Layers {
		if !left[b] {
			t.Errorf("leftAlone lacks %s: %+v", b, res.LeftAlone)
		}
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", "refs/wt-sync/"); strings.Contains(refs, "/s1/") || strings.Contains(refs, "/s2/") || strings.Contains(refs, "/s3/") {
		t.Errorf("a layer was pinned as moved:\n%s", refs)
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.StackTmp, syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.StackTmp); got != oldTmp {
		t.Errorf("undo left the copy at %s, want %s\n%s", got, oldTmp, undone.String())
	}
	same("after the undo")
}

// A look at the line with --alone lists its layers and says none would move.
func TestSyncLookAloneWithStackSaysNoLayerWouldMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := synccmdtest.StackLine(t, "")
	var out bytes.Buffer
	look := synccmd.SyncOptions{NoFetch: true, Alone: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}}
	if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, look, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"--alone", "no layer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the look lacks %q:\n%s", want, out.String())
		}
	}
}

// --edit stops for a person to amend and then moves the layers by the
// result: with --alone there are no layers to move, and it is refused.
func TestSyncRunAloneWithEditIsRefused(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := synccmdtest.StackLine(t, "")
	opts := synccmdtest.NoAgents()
	opts.Alone = true
	opts.Stack = &synccmd.StackOptions{Edit: "HEAD"}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "--alone and --edit") {
		t.Fatalf("err %v\n%s", err, human)
	}
}

func kitWork(branch string) string { return branch }

// --if-ready judges the worktree named alone: a dirty member of its stack
// does not make the run fail.
func TestSyncRunAloneIfReadyJudgesOnlyTheWorktreeNamed(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, bump, child, _ := aloneFixture(t)
	childBefore := aloneStateOf(t, child, ctx.Repo.MainRoot)
	opts := synccmdtest.NoAgents()
	opts.Alone, opts.IfReady = true, true
	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"bump"}, opts, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !testkit.GitAncestor(t, ctx.Repo.MainRoot, "origin/main", "feat_wt/bump") || testkit.GitOut(t, bump, "status", "--porcelain") != "" {
		t.Errorf("bump was not rebased cleanly:\n%s", out.String())
	}
	if got := aloneStateOf(t, child, ctx.Repo.MainRoot); got != childBefore {
		t.Errorf("the dirty worktree changed: %+v", got)
	}
}

// The JSON result names the worktrees left alone.
func TestSyncRunAloneJSONNamesTheWorktreesLeftAlone(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, child, grand := aloneFixture(t)
	var js bytes.Buffer
	j := syncrun.NewSyncRunJournal(&js, "sync rebase")
	opts := synccmdtest.NoAgents()
	opts.Alone, opts.Journal = true, j
	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"bump"}, opts, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var res syncrun.RunResult
	if err := json.Unmarshal(js.Bytes(), &res); err != nil {
		t.Fatalf("%v\n%s", err, js.String())
	}
	got := map[string]string{}
	for _, l := range res.LeftAlone {
		got[l.Branch] = testkit.Str(l.Path)
	}
	if len(got) != 2 || !samePathAs(got["feat_wt/child"], child) || !samePathAs(got["feat_wt/grand"], grand) {
		t.Errorf("leftAlone = %+v, want child and grand", res.LeftAlone)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Branch != "feat_wt/bump" {
		t.Errorf("participants = %+v, want bump alone", res.Worktrees)
	}
}

func samePathAs(a, b string) bool {
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return ea == eb && ea != ""
}
