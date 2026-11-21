package syncrun_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// finishFixture is a worktree whose rebase has finished, as a run or a resume
// hands it to Finish.
type finishFixture struct {
	ctx    *kit.Context
	cfg    *wtsync.Config
	res    wtsync.Result
	branch string
	path   string
}

func newFinishFixture(t *testing.T) finishFixture {
	t.Helper()
	ctx, bump := provisiontest.RunFixture(t, false)
	trunk := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "origin/main")
	branch := testkit.GitOut(t, bump, "rev-parse", "--abbrev-ref", "HEAD")
	cfg, err := wtsync.LoadFromRef(ctx.Repo.MainRoot, trunk)
	if err != nil {
		t.Fatal(err)
	}
	res, err := wtsync.Rebase(ctx.Repo.MainRoot, cfg, wtsync.Request{Path: bump, Branch: branch, Trunk: trunk, Onto: trunk, Epoch: 7, Work: "bump"}, nil)
	if err != nil || res.Left != nil || res.Restored {
		t.Fatalf("rebase %+v err %v", res, err)
	}
	return finishFixture{ctx: ctx, cfg: cfg, res: res, branch: branch, path: bump}
}

// input builds a FinishInput whose hooks record the order they ran in.
func (f finishFixture) input(events *[]string, layers func() (bool, string), settle func(string, error, bool) []string) syncrun.FinishInput {
	return syncrun.FinishInput{
		Work: "bump", Path: f.path, Branch: f.branch, Epoch: 7, Res: f.res,
		Headline: "  ✓ rebased the headline",
		Complete: func() syncrun.CompleteInput {
			*events = append(*events, "complete")
			return syncrun.CompleteInput{Branch: f.branch, Epoch: 7, Res: f.res, TrunkName: "main"}
		},
		Layers: func() (bool, string) {
			*events = append(*events, "layers")
			return layers()
		},
		Settle: func(head string, err error, restored bool) []string {
			*events = append(*events, "settle")
			return settle(head, err, restored)
		},
	}
}

// What a finish does is in this order: the headline, the deferred steps and
// the result ref, the stack, what the caller settles, the report. A caller
// that moved a step would move a safety rule with it.
func TestFinishRunsItsStepsInOrderAndHandsTheHooksTheirInputs(t *testing.T) {
	t.Parallel()
	f := newFinishFixture(t)
	var events []string
	var out bytes.Buffer
	in := f.input(&events,
		func() (bool, string) { return false, "layer moves stopped" },
		func(head string, err error, restored bool) []string {
			if head != testkit.GitOut(t, f.path, "rev-parse", "HEAD") || err != nil || restored {
				t.Errorf("settle got head %q err %v restored %v", head, err, restored)
			}
			return []string{"each_commit check at tip"}
		})
	in.Extra = []string{"    the caller's extra line"}
	got := syncrun.Finish(f.ctx, &out, f.cfg, &syncrun.RebaseTracker{}, nil, in)
	if want := []string{"complete", "layers", "settle"}; !slices.Equal(events, want) {
		t.Fatalf("hooks ran %v, want %v", events, want)
	}
	text := out.String()
	head, extra, undo := strings.Index(text, "the headline"), strings.Index(text, "the caller's extra line"), strings.Index(text, "↩")
	if head < 0 || extra < head || undo < extra {
		t.Fatalf("headline, extra and the undo line are not in that order:\n%s", text)
	}
	if got.Err != nil || got.StackFailure != "layer moves stopped" || got.Restored {
		t.Fatalf("outcome %+v", got)
	}
	if got.Head != testkit.GitOut(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("head %s", got.Head)
	}
	if want := []string{"each_commit check at tip"}; !slices.Equal(got.Owed, want) {
		t.Fatalf("owed %v", got.Owed)
	}
	if want := []string{"bump (owed: each_commit check at tip)"}; !slices.Equal(got.OwedFailures, want) {
		t.Fatalf("owed failures %v", got.OwedFailures)
	}
}

// The finish's own failure comes back in the outcome and does not stop the
// hooks after it: the rebase stands, and the layers and the report are still
// the caller's to settle.
func TestFinishReturnsACompleteRunFailureAndStillSettles(t *testing.T) {
	t.Parallel()
	f := newFinishFixture(t)
	f.cfg = &wtsync.Config{}
	var events []string
	var out bytes.Buffer
	in := f.input(&events, func() (bool, string) { return false, "" }, func(string, error, bool) []string { return nil })
	// A tracker-built input that cannot finish: the worktree is gone.
	in.Path = f.path + "-gone"
	got := syncrun.Finish(f.ctx, &out, f.cfg, &syncrun.RebaseTracker{}, nil, in)
	if got.Err == nil {
		t.Fatalf("no failure for a worktree that is not there:\n%s", out.String())
	}
	if want := []string{"complete", "layers", "settle"}; !slices.Equal(events, want) {
		t.Fatalf("hooks ran %v", events)
	}
}

// A line put back is a result that no longer exists: the report of what the
// rebase did to its commits is not told, and the hooks hear the line was
// restored.
func TestFinishTellsNothingOfTheReplayWhenTheLineWasPutBack(t *testing.T) {
	t.Parallel()
	for _, restored := range []bool{false, true} {
		f := newFinishFixture(t)
		f.res.Emptied = []wtsync.Dropped{{Commit: strings.Repeat("a", 40), Subject: "the emptied one"}}
		var events []string
		var out bytes.Buffer
		var heard bool
		in := f.input(&events, func() (bool, string) { return restored, "" }, func(_ string, _ error, r bool) []string { heard = r; return nil })
		in.Res = f.res
		got := syncrun.Finish(f.ctx, &out, f.cfg, &syncrun.RebaseTracker{}, nil, in)
		if got.Restored != restored || heard != restored {
			t.Fatalf("restored=%v: outcome %v, settle heard %v", restored, got.Restored, heard)
		}
		if told := strings.Contains(out.String(), "the emptied one"); told == restored {
			t.Fatalf("restored=%v but the report was told=%v:\n%s", restored, told, out.String())
		}
	}
}

// The builder takes the four inputs a run and a resume differ in from the
// caller and everything else from the record and the result.
func TestStackFinishForTakesWhatDiffersFromTheCaller(t *testing.T) {
	t.Parallel()
	rec := &wtsync.StackRecord{Onto: "abc", Force: true}
	res := wtsync.Result{NewTip: "new", Emptied: []wtsync.Dropped{{Subject: "x"}}}
	own := &wtsync.Own{}
	now := time.Unix(100, 0)
	lerr := errors.New("no sessions")
	got := syncrun.StackFinishFor(rec, res, &wtsync.Config{}, now, nil, lerr, syncrun.StackFinishCaller{
		OldTip: "old", Own: own, Stopped: []string{"p"}, Confirmed: true, Unchecked: []string{"u"}})
	if got.Record != rec || got.OldTip != "old" || got.NewTip != "new" || got.Onto != "abc" || got.Own != own ||
		!errors.Is(got.ListErr, lerr) || !got.Force || !got.Now.Equal(now) || !got.Confirmed ||
		!slices.Equal(got.Stopped, []string{"p"}) || len(got.Dropped) != 1 || !slices.Equal(got.Unchecked, []string{"u"}) {
		t.Fatalf("%+v", got)
	}
}
