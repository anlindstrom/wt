package synccmd_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// liftedLine is a stack line whose top commit, in layer s5, changes x.txt
// and y.txt, and a trunk that took that commit reworked, under its subject
// and author: trunk's copy writes x.txt its own way, and y.txt too when
// both is set. It returns the stack's commit, trunk's, and every branch's
// tip before the run.
func liftedLine(t *testing.T, both bool) (ctx *kit.Context, s *gittest.StackLine, lifted, trunk string, tips map[string]string) {
	t.Helper()
	return liftedLineWith(t, both, false)
}

// liftedLineWith is liftedLine, and with overlap both commits also change
// t.txt, on lines far enough apart that git merges them cleanly: a file the
// drop takes that was never a conflict.
func liftedLineWith(t *testing.T, both, overlap bool) (ctx *kit.Context, s *gittest.StackLine, lifted, trunk string, tips map[string]string) {
	t.Helper()
	ctx, s = synccmdtest.StackLine(t, "")
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	const subject = "feat(s5): lift the signup check"
	testkit.WriteFile(t, tmp, "x.txt", "stack's x\n")
	testkit.WriteFile(t, tmp, "y.txt", "stack's y\n")
	testkit.GitIn(t, tmp, "add", "x.txt", "y.txt")
	if overlap {
		testkit.WriteFile(t, tmp, "t.txt", "t\n"+strings.Repeat("filler\n", 10)+"stack's last\n")
		testkit.GitIn(t, tmp, "add", "t.txt")
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", subject)
	lifted = testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	testkit.GitIn(t, s.Paths["s5"], "merge", "-q", "--ff-only", lifted)
	testkit.WriteFile(t, main, "x.txt", "trunk's x\n")
	testkit.GitIn(t, main, "add", "x.txt")
	if both {
		testkit.WriteFile(t, main, "y.txt", "trunk's y\n")
		testkit.GitIn(t, main, "add", "y.txt")
	}
	if overlap {
		testkit.WriteFile(t, main, "t.txt", "t2\n"+strings.Repeat("filler\n", 4)+"trunk's filler\n"+strings.Repeat("filler\n", 5)+"last\n")
		testkit.GitIn(t, main, "add", "t.txt")
	}
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", subject)
	trunk = testkit.GitOut(t, main, "rev-parse", "HEAD")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	tips = map[string]string{gittest.StackTmp: lifted}
	for _, b := range s.Layers {
		tips[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return ctx, s, lifted, trunk, tips
}

// runToLiftedStop runs the line, which stops at the lifted commit: the run's
// look names trunk's copy, and the stop offers the drop by that commit.
func runToLiftedStop(t *testing.T, ctx *kit.Context, s *gittest.StackLine, lifted, trunk string) {
	t.Helper()
	runToLiftedStopWith(t, ctx, s, lifted, trunk, synccmdtest.NoAgents())
}

// runToLiftedStopWith is runToLiftedStop with the run's options.
func runToLiftedStopWith(t *testing.T, ctx *kit.Context, s *gittest.StackLine, lifted, trunk string, opts synccmd.RunOptions) {
	t.Helper()
	opts.Push = syncrun.PushNever
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the stop at the lifted commit\n%s", err, human)
	}
	look := "may already be on trunk as " + git.ShortID(trunk, 7)
	offer := "wt sync resume tmp/stack --drop " + git.ShortID(lifted, 7)
	if !strings.Contains(human, look) || !strings.Contains(human, offer) {
		t.Errorf("want the look's %q and the stop's %q in\n%s", look, offer, human)
	}
	gitDir, err := wtsync.GitDir(s.Paths[gittest.StackTmp])
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok || st.StoppedCommit != lifted {
		t.Fatalf("the run stopped at %+v (%v %v), want the lifted commit %s, which the look named", st.StoppedCommit, ok, err, lifted)
	}
	plan := readFile(t, wtsync.PlanPath(gitDir))
	if !strings.Contains(plan, offer) || !strings.Contains(plan, git.ShortID(trunk, 7)) {
		t.Errorf("the plan does not offer the drop:\n%s", plan)
	}
}

func dropResume(drop string) syncfix.ResumeOptions {
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.Drop = drop
	return ropts
}

func undoAndCheck(t *testing.T, ctx *kit.Context, tips map[string]string) {
	t.Helper()
	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for b, want := range tips {
		if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b); got != want {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, want, undone.String())
		}
	}
}

// wt#214: the stop at a commit trunk carries reworked offers the drop, and
// nothing is dropped unless it is taken by that commit. A drop named by
// another commit is refused with the stop as it was. Taken, it keeps the
// stack's change to y.txt, which trunk's copy does not touch, takes trunk's
// x.txt, saves what the person had written in x.txt at the stop as a blob,
// and the line goes on. Undo puts every branch back.
func TestDropAtAReworkedStopKeepsWhatTrunkDoesNotTouch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, trunk, tips := liftedLine(t, false)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	runToLiftedStop(t, ctx, s, lifted, trunk)

	var refused bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(s.Tips["s1"]), &refused)
	if err == nil || !strings.Contains(err.Error(), "not the commit the rebase stopped at") {
		t.Fatalf("a drop of another commit: err %v\n%s", err, refused.String())
	}
	if st := testkit.GitOut(t, tmp, "status", "--porcelain=v1", "--", "x.txt"); st != "AA x.txt" {
		t.Fatalf("the refused drop touched x.txt: %q", st)
	}

	testkit.WriteFile(t, tmp, "x.txt", "half merged by hand\n")
	var human bytes.Buffer
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(git.ShortID(lifted, 7)), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("resume: %v\n%s", err, human.String())
	}
	out := human.String()
	if !strings.Contains(out, "took trunk's x.txt") || !strings.Contains(out, "kept its y.txt") {
		t.Errorf("the resume does not say what the drop took and kept:\n%s", out)
	}
	kept := regexp.MustCompile(`x\.txt as you left it is kept under (\S+):`).FindStringSubmatch(out)
	if kept == nil {
		t.Fatalf("the person's x.txt is not named as kept:\n%s", out)
	}
	if got := testkit.GitOut(t, main, "show", kept[1]+":x.txt"); got != "half merged by hand" {
		t.Errorf("the kept x.txt holds %q", got)
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":x.txt"); got != "trunk's x" {
		t.Errorf("the line's x.txt is %q, want trunk's", got)
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":y.txt"); got != "stack's y" {
		t.Errorf("the line's y.txt is %q, want the stack's", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == tips[b] {
			t.Errorf("layer %s did not move\n%s", b, out)
		}
	}
	undoAndCheck(t, ctx, tips)
	if got := readFile(t, filepath.Join(tmp, "x.txt")); got != "stack's x\n" {
		t.Errorf("x.txt in the line's worktree is %q after the undo", got)
	}
}

// When trunk's copy touches every file the commit does, the drop leaves
// nothing of it: the commit is dropped as resolved to trunk's side, the
// layers that held it count one commit fewer and move, and undo puts every
// branch back.
func TestDropAtAReworkedStopDropsACommitTrunkFullyTook(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, trunk, tips := liftedLine(t, true)
	main := ctx.Repo.MainRoot
	runToLiftedStop(t, ctx, s, lifted, trunk)
	var human bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("resume: %v\n%s", err, human.String())
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", "--grep", "lift the signup check", "origin/main.."+gittest.StackTmp); n != "0" {
		t.Errorf("the line still carries the stack's copy (%s)\n%s", n, human.String())
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":y.txt"); got != "trunk's y" {
		t.Errorf("the line's y.txt is %q, want trunk's", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	undoAndCheck(t, ctx, tips)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// foldLiftedLine is a fold of the line onto trunk (--stack --autosquash
// --onto main), whose content check holds the result to the old line, run
// to the stop at the lifted commit; x.txt conflicts there and t.txt, which
// trunk's copy changes too, merges cleanly.
func foldLiftedLine(t *testing.T) (ctx *kit.Context, s *gittest.StackLine, lifted string, tips map[string]string) {
	t.Helper()
	ctx, s, lifted, trunk, tips := liftedLineWith(t, false, true)
	opts := synccmdtest.Onto("main", "")
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	runToLiftedStopWith(t, ctx, s, lifted, trunk, opts)
	return ctx, s, lifted, tips
}

// keptRefs is every ref the drop kept the person's files under.
func keptRefs(t *testing.T, main string) []string {
	t.Helper()
	out := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", wtsync.EditKeptPrefix)
	var refs []string
	for _, r := range strings.Split(out, "\n") {
		if strings.Contains(r, "-drop") {
			refs = append(refs, r)
		}
	}
	return refs
}

// A drop cut after it replaced its first file has already kept what the
// person had there, every file it would replace, under a ref the handover
// records, and named it. A plain resume then refuses, naming the drop to
// finish, and changes nothing, even with nothing left unmerged. A second --drop finishes it without a second ref,
// the fold passes its content check, and undo leaves the kept ref.
func TestDropCutPartWayHasKeptThePersonsFilesFirst(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, tips := foldLiftedLine(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "x.txt", "half merged by hand\n")
	staged := testkit.GitOut(t, tmp, "show", ":0:t.txt")
	ropts := dropResume(lifted)
	ropts.DuringDrop = func(path string) error { return errors.New("interrupted after " + path) }
	var cut bytes.Buffer
	if err := syncfix.SyncResume(ctx, gittest.StackTmp, ropts, &cut); err == nil || !strings.Contains(err.Error(), "interrupted after") {
		t.Fatalf("err %v, want the interrupt\n%s", err, cut.String())
	}
	refs := keptRefs(t, main)
	if len(refs) != 1 {
		t.Fatalf("kept refs %v, want one\n%s", refs, cut.String())
	}
	if got := testkit.GitOut(t, main, "show", refs[0]+":x.txt"); got != "half merged by hand" {
		t.Errorf("the kept x.txt is %q", got)
	}
	if got := testkit.GitOut(t, main, "show", refs[0]+":t.txt"); got != staged {
		t.Errorf("the kept t.txt is %q, want what the stop staged", got)
	}
	if !strings.Contains(cut.String(), refs[0]) {
		t.Errorf("the cut resume does not name %s:\n%s", refs[0], cut.String())
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok, err := wtsync.ReadState(gitDir); err != nil || !ok || st.DropKept != refs[0] {
		t.Fatalf("the handover does not record the kept ref: %+v %v %v", st.DropKept, ok, err)
	}

	// Nothing unmerged is left: x.txt staged as the person left it, which
	// the cut drop had kept and was still to replace.
	testkit.GitIn(t, tmp, "add", "x.txt")
	heads := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	files := testkit.GitOut(t, tmp, "status", "--porcelain=v1")
	var plain bytes.Buffer
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(""), &plain)
	if err == nil || !strings.Contains(err.Error(), "--drop "+git.ShortID(lifted, 7)) || !strings.Contains(err.Error(), "part way") {
		t.Fatalf("a plain resume over the cut drop: err %v, want a refusal naming the drop to finish\n%s", err, plain.String())
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != heads {
		t.Errorf("the refused resume moved branches:\nbefore %s\nafter %s", heads, got)
	}
	if got := testkit.GitOut(t, tmp, "status", "--porcelain=v1"); got != files {
		t.Errorf("the refused resume changed the worktree:\nbefore %s\nafter %s", files, got)
	}

	var human bytes.Buffer
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("second --drop: %v\n%s", err, human.String())
	}
	if got := keptRefs(t, main); len(got) != 1 || got[0] != refs[0] {
		t.Errorf("kept refs after the second --drop %v, want only %s", got, refs[0])
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":x.txt"); got != "trunk's x" {
		t.Errorf("the line's x.txt is %q, want trunk's", got)
	}
	undoAndCheck(t, ctx, tips)
	if got := keptRefs(t, main); len(got) != 1 {
		t.Errorf("undo took the kept ref: %v", got)
	}
}

// A resume cut after it kept the drop with the handover, retried with no
// --drop, still takes every file the drop took as resolved at the stop, the
// clean t.txt included: the fold passes its content check and the layers
// move.
func TestDropRetriedPlainKeepsEveryTakenFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, _, lifted, tips := foldLiftedLine(t)
	main := ctx.Repo.MainRoot
	ropts := dropResume(lifted)
	ropts.AfterKeepStop = func() error { return errors.New("interrupted") }
	var cut bytes.Buffer
	if err := syncfix.SyncResume(ctx, gittest.StackTmp, ropts, &cut); err == nil || err.Error() != "interrupted" {
		t.Fatalf("err %v, want the interrupt\n%s", err, cut.String())
	}
	var human bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(""), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("plain retry: %v\n%s", err, human.String())
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":t.txt"); strings.Contains(got, "stack's last") {
		t.Errorf("the line's t.txt keeps the dropped change:\n%s", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	undoAndCheck(t, ctx, tips)
}

// A file the drop would replace that is a symlink, or whose staged and
// working versions differ, refuses the drop, naming it, with every file and
// the index as they were.
func TestDropRefusesASymlinkOrAFileStagedOneWayAndEditedAnother(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, tips := foldLiftedLine(t)
	tmp := s.Paths[gittest.StackTmp]
	status := func() string { return testkit.GitOut(t, tmp, "status", "--porcelain=v1") }

	testkit.WriteFile(t, tmp, "t.txt", "resolution A\n")
	testkit.GitIn(t, tmp, "add", "t.txt")
	testkit.WriteFile(t, tmp, "t.txt", "edited to B\n")
	before := status()
	var out bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &out)
	if err == nil || !strings.Contains(err.Error(), "t.txt") || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("err %v, want a refusal naming t.txt's staged and working versions\n%s", err, out.String())
	}
	if got := status(); got != before || testkit.GitOut(t, tmp, "show", ":0:t.txt") != "resolution A" || readFile(t, filepath.Join(tmp, "t.txt")) != "edited to B\n" {
		t.Errorf("the refused drop changed t.txt or the status:\n%s", got)
	}
	testkit.GitIn(t, tmp, "add", "t.txt")

	if err := os.Remove(filepath.Join(tmp, "x.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("y.txt", filepath.Join(tmp, "x.txt")); err != nil {
		t.Fatal(err)
	}
	before = status()
	out.Reset()
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &out)
	if err == nil || !strings.Contains(err.Error(), "x.txt") || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err %v, want a refusal naming the symlink x.txt\n%s", err, out.String())
	}
	if got := status(); got != before {
		t.Errorf("the refused drop changed the status:\nbefore %s\nafter %s", before, got)
	}
	if target, err := os.Readlink(filepath.Join(tmp, "x.txt")); err != nil || target != "y.txt" {
		t.Errorf("x.txt is no longer the symlink to y.txt: %q %v", target, err)
	}
	if refs := keptRefs(t, ctx.Repo.MainRoot); len(refs) != 0 {
		t.Errorf("a refused drop kept refs: %v", refs)
	}
	_ = tips
}

// A drop keeps the bytes the person wrote, not what a clean filter makes of
// them, and a file's mode with its content: x.txt resolved to trunk's text
// but made executable and staged is kept with its mode. A file staged with
// one mode and another in the worktree refuses the drop, naming it.
func TestDropKeepsRawBytesAndModes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, _ := foldLiftedLine(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	testkit.GitIn(t, main, "config", "filter.lossy.clean", "sed s/secret/XXX/")
	common := testkit.GitOut(t, main, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err := os.MkdirAll(filepath.Join(common, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, "info", "attributes"), []byte("t.txt filter=lossy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(filepath.Join(tmp, "t.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &out)
	if err == nil || !strings.Contains(err.Error(), "t.txt") || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("err %v, want a refusal naming t.txt, staged with one mode and another in the worktree\n%s", err, out.String())
	}
	if refs := keptRefs(t, main); len(refs) != 0 {
		t.Fatalf("a refused drop kept refs: %v", refs)
	}
	if err := os.Chmod(filepath.Join(tmp, "t.txt"), 0o644); err != nil {
		t.Fatal(err)
	}

	testkit.WriteFile(t, tmp, "t.txt", "my secret line\n")
	testkit.GitIn(t, tmp, "add", "t.txt")
	testkit.WriteFile(t, tmp, "t.txt", "my secret line\n")
	testkit.WriteFile(t, tmp, "x.txt", testkit.GitOut(t, tmp, "show", "HEAD:x.txt")+"\n")
	if err := os.Chmod(filepath.Join(tmp, "x.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "add", "x.txt")
	out.Reset()
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &out)
	if err != nil && strings.Contains(out.String(), "no layer moved") {
		t.Fatalf("resume: %v\n%s", err, out.String())
	}
	refs := keptRefs(t, main)
	if len(refs) != 1 {
		t.Fatalf("kept refs %v, want one\n%s", refs, out.String())
	}
	if got := testkit.GitOut(t, main, "cat-file", "-p", refs[0]+":t.txt"); got != "my secret line" {
		t.Errorf("the kept t.txt is %q, want the bytes written", got)
	}
	if got := testkit.GitOut(t, main, "ls-tree", refs[0], "--", "x.txt"); !strings.HasPrefix(got, "100755 ") {
		t.Errorf("x.txt is not kept with its executable mode: %q", got)
	}
}
