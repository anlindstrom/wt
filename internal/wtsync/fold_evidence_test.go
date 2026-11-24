package wtsync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// foldRepo is a line P, T, U, "fixup! T" (named by T's id, a clean change to
// n.txt) and, when tail is set, V, rebased by git's autosquash onto a trunk
// that changed what T and V add: the rebase stops at T. sameMeta gives P and
// T one subject and author date. The plan is the fold and its target, as
// recorded where wt hands the stop over.
func foldRepo(t *testing.T, tail, sameMeta, conflict bool) (dir, target, fixup string, plan *ReplayPlan) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	write := func(file, content, subject string) string {
		gittest.WriteFile(t, filepath.Join(dir, file), content)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject, "--date=2020-01-01T00:00:00Z")
		return gitIn(t, dir, "rev-parse", "HEAD")
	}
	write("t.txt", "a\nb\nc\n", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feat")
	subjects := [2]string{"feat: p", "feat: t"}
	if sameMeta {
		subjects = [2]string{"feat: same", "feat: same"}
	}
	write("p.txt", "p\n", subjects[0])
	target = write("t.txt", "a\nb\nc\nT\n", subjects[1])
	write("u.txt", "u\n", "feat: u")
	if conflict {
		// A change to the line T added, which T's resolution does not keep.
		fixup = write("t.txt", "a\nb\nc\nT2\n", "fixup! "+target)
	} else {
		fixup = write("n.txt", "n\n", "fixup! "+target)
	}
	if tail {
		write("v.txt", "v\n", "feat: v")
	}
	gitIn(t, dir, "checkout", "-q", "main")
	write("t.txt", "a\nb\nc\nZ\n", "trunk")
	write("v.txt", "trunk v\n", "trunk v")
	gitIn(t, dir, "checkout", "-q", "feat")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", "main"); err == nil {
		t.Fatalf("the rebase did not stop at the target:\n%s", out)
	}
	plan = &ReplayPlan{Folds: []string{fixup}, Targets: map[string]string{fixup: target}, Observed: true}
	w := newFoldWatch(plan)
	w.look(dir)
	w.record(plan)
	return dir, target, fixup, plan
}

func resolveTarget(t *testing.T, dir string) {
	t.Helper()
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), "a\nb\nc\nZ\nT\n")
	gitIn(t, dir, "add", "t.txt")
}

// A fold wt sees run, its target's pick right before it in the list, is
// confirmed, and so is a fold wt takes up at the stop it handed over.
func TestAWatchConfirmsAFoldRightBehindItsTargetsPick(t *testing.T) {
	t.Parallel()
	dir, _, fixup, plan := foldRepo(t, true, false, false)
	w := startAt(dir, plan)
	resolveTarget(t, dir)
	if out, err := gittest.Try(t, dir, "rebase", "--continue"); err == nil {
		t.Fatalf("the rebase did not stop at the tail:\n%s", out)
	}
	w.look(dir)
	if !slices.Equal(w.executed, []string{fixup}) {
		t.Errorf("executed %q, want the fixup", w.executed)
	}
}

// A target a person skipped is no target: the fixup folds into the commit
// before it, and the list, which still holds the target's pick, does not
// show it. The rebase wt takes up at a later stop was moved by hand, so the
// fixup is not confirmed, whether or not the commit before the target shares
// the target's author, date and subject.
func TestAWatchDoesNotConfirmAFoldWhoseTargetAPersonSkipped(t *testing.T) {
	t.Parallel()
	for _, sameMeta := range []bool{false, true} {
		name := map[bool]string{false: "different", true: "same metadata"}[sameMeta]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, _, _, plan := foldRepo(t, true, sameMeta, false)
			if out, err := gittest.Try(t, dir, "rebase", "--skip"); err == nil {
				t.Fatalf("the rebase did not stop at the tail:\n%s", out)
			}
			w := startAt(dir, plan)
			gittest.WriteFile(t, filepath.Join(dir, "v.txt"), "trunk v\nv\n")
			gitIn(t, dir, "add", "v.txt")
			if out, err := gittest.Try(t, dir, "rebase", "--continue"); err != nil {
				t.Fatalf("the rebase did not finish:\n%s", out)
			}
			w.settle()
			if len(w.executed) != 0 {
				t.Errorf("executed %q, want none: the fixup's target was skipped", w.executed)
			}
		})
	}
}

// A fixup still to run counts only when its target's pick is right before its
// run in the list: an edited list that puts another pick between them folds it
// into that pick.
func TestAWatchDoesNotConfirmAFoldBehindAnotherPick(t *testing.T) {
	t.Parallel()
	dir, _, fixup, plan := foldRepo(t, false, false, false)
	file := filepath.Join(gitIn(t, dir, "rev-parse", "--git-path", "rebase-merge"), "git-rebase-todo")
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var folds, others []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(l, "fixup") {
			folds = append(folds, l)
		} else if l != "" {
			others = append(others, l)
		}
	}
	// pick U, then the fixup of T: git folds it into U.
	if err := os.WriteFile(file, []byte(strings.Join(append(others, folds...), "\n")+"\n"), 0o644); err != nil { //nolint:gosec // the sequencer list of a test repository, from git rev-parse
		t.Fatal(err)
	}
	w := startAt(dir, plan)
	w.settle()
	if len(w.executed) != 0 {
		t.Errorf("executed %q, want none: %s folds into another commit", w.executed, fixup[:7])
	}
}

// The folds the rebase's end settles are in the stack record before the
// resume returns, so an interrupt in what runs after the rebase leaves the
// record with them.
func TestAResumeRecordsTheFoldsItSettlesBeforeItReturns(t *testing.T) {
	t.Parallel()
	dir, _, fixup, plan := foldRepo(t, false, false, false)
	if err := WriteStackRecord(dir, &StackRecord{Epoch: 9, Branch: "feat", Replay: plan}); err != nil {
		t.Fatal(err)
	}
	resolveTarget(t, dir)
	old := gitIn(t, dir, "rev-parse", "feat")
	req := Request{Path: dir, Branch: "feat", Trunk: "main", Onto: "main", Epoch: 9, ReportPlan: plan}
	if _, err := Resume(dir, &Config{}, req, old, Safety{Branch: "feat", Epoch: 9}, nil); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := ReadStackRecord(dir, 9)
	if err != nil || !ok {
		t.Fatalf("record %v %v", ok, err)
	}
	if !slices.Equal(rec.Replay.Folded, []string{fixup}) {
		t.Errorf("the record's folded %q, want the fixup", rec.Replay.Folded)
	}
}

// A target skipped at a conflict, the rebase resumed to a second handover and
// resumed again, leaves the fixup unconfirmed at the end: what wt saw it could
// not confirm at the first resume is not evidence at the second.
func TestASkippedTargetStaysUnconfirmedThroughASecondHandover(t *testing.T) {
	t.Parallel()
	dir, _, fixup, plan := foldRepo(t, true, false, false)
	if err := WriteStackRecord(dir, &StackRecord{Epoch: 9, Branch: "feat", Replay: plan}); err != nil {
		t.Fatal(err)
	}
	if out, err := gittest.Try(t, dir, "rebase", "--skip"); err == nil {
		t.Fatalf("the rebase did not stop at the tail:\n%s", out)
	}
	old := gitIn(t, dir, "rev-parse", "feat")
	resume := func(plan *ReplayPlan) Result {
		t.Helper()
		req := Request{Path: dir, Branch: "feat", Trunk: "main", Onto: "main", Epoch: 9, ReportPlan: plan}
		res, err := Resume(dir, &Config{}, req, old, Safety{Branch: "feat", Epoch: 9}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := resume(plan); res.Left == nil {
		t.Fatalf("the first resume did not hand over at the tail: %+v", res)
	}
	rec, ok, err := ReadStackRecord(dir, 9)
	if err != nil || !ok {
		t.Fatalf("record %v %v", ok, err)
	}
	gittest.WriteFile(t, filepath.Join(dir, "v.txt"), "trunk v\nv\n")
	gitIn(t, dir, "add", "v.txt")
	res := resume(rec.Replay)
	if res.Left != nil || res.Replay == nil {
		t.Fatalf("the second resume did not finish: %+v", res)
	}
	if slices.Contains(res.Replay.Folded, fixup) {
		t.Errorf("folded %q: the fixup of a skipped target is confirmed", res.Replay.Folded)
	}
}

// A conflicting fixup skipped where the list held a break right behind it
// leaves the filtered count of the done list as it was, and is still seen:
// the lines of the list differ, so the fixup is not confirmed.
func TestAWatchDoesNotConfirmAFixupSkippedIntoABreak(t *testing.T) {
	t.Parallel()
	dir, _, _, plan := foldRepo(t, false, false, true)
	resolveTarget(t, dir)
	if out, err := gittest.Try(t, dir, "rebase", "--continue"); err == nil {
		t.Fatalf("the rebase did not stop at the fixup:\n%s", out)
	}
	// wt looks at the stop it hands over, as the run does.
	w := newFoldWatch(plan)
	w.look(dir)
	w.record(plan)
	file := filepath.Join(gitIn(t, dir, "rev-parse", "--git-path", "rebase-merge"), "git-rebase-todo")
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append([]byte("break\n"), data...), 0o644); err != nil { //nolint:gosec // the sequencer list of a test repository, from git rev-parse
		t.Fatal(err)
	}
	out, err := gittest.Try(t, dir, "rebase", "--skip")
	if busyNo(dir) {
		t.Fatalf("the rebase did not stop at the break: %v\n%s", err, out)
	}
	w = startAt(dir, plan)
	w.settle()
	if len(w.executed) != 0 {
		t.Errorf("executed %q, want none: the fixup was skipped", w.executed)
	}
}

func busyNo(dir string) bool {
	busy, err := RebaseInProgress(dir)
	return err != nil || !busy
}
