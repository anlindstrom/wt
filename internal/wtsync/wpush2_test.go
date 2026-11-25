package wtsync

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/gittest"
)

// interruptedAfterFiles is a run whose move of s1 was cut once its files
// had followed its ref, before the move was recorded as made.
func interruptedAfterFiles(t *testing.T, epoch int64) (*gittest.StackLine, LayerMove) {
	t.Helper()
	s := stackLine(t, stackYAML)
	rec, tips := rebasedLine(t, s, epoch)
	opts := moveOpts(t)
	opts.Between = func(stage string, m LayerMove) error {
		if stage == StageAfterFiles && m.Branch == "s1" {
			return errors.New("interrupted")
		}
		return nil
	}
	if err := MoveLayers(s.Dir, rec, tips, opts); err == nil {
		t.Fatal("the moves went on")
	}
	return s, movesOf(rec)["s1"]
}

// A layer cut after its files moved is put back by undo only while its
// index and files are exactly its new tip's: a change made there since is
// refused, the layer left at its new tip with its pin and the change kept.
func TestUndoRefusesALayerCutAfterItsFilesWithChangesOnTop(t *testing.T) {
	t.Parallel()
	s, m := interruptedAfterFiles(t, 61)
	gittest.WriteFile(t, filepath.Join(s.Paths["s1"], "a1.txt"), "changed by hand\n")
	_, err := Undo(s.Dir, worktreesOf(t, s.Dir), nil, gittest.StackTmp, time.Now(), true)
	if err == nil || !strings.Contains(err.Error(), s.Paths["s1"]) {
		t.Fatalf("err %v, want a refusal naming %s", err, s.Paths["s1"])
	}
	if got := gitIn(t, s.Dir, "rev-parse", "s1"); got != m.New {
		t.Errorf("s1 is at %s, want left at %s", got, m.New)
	}
	if refs := gitIn(t, s.Dir, "for-each-ref", "refs/wt-sync/s1/"); refs == "" {
		t.Error("s1 lost its pin")
	}
	if st := gitIn(t, s.Paths["s1"], "status", "--porcelain", "--untracked-files=no"); !strings.Contains(st, "a1.txt") {
		t.Errorf("the change in s1 is gone: %q", st)
	}
}

// What a resume finds at a conflict stop replaces what an earlier resume of
// the same stop kept, leaves the records of other stops as they are, and
// finding nothing there keeps what was kept.
func TestSettledAtReplacesOnlyTheSameStopsRecords(t *testing.T) {
	t.Parallel()
	earlier := StopResolution{Commit: "aaa", Path: "x.txt", Blob: "1", Order: 1}
	kept := StopResolution{Commit: "bbb", Path: "y.txt", Blob: "2", Order: 2}
	st := State{StoppedCommit: "bbb", Resolutions: []StopResolution{earlier, kept}}
	now := []StopResolution{{Commit: "bbb", Path: "y.txt", Blob: "3"}}
	StampStops(st, now, nil)
	got, _ := st.SettledAt(now, nil)
	want := []StopResolution{earlier, {Commit: "bbb", Path: "y.txt", Blob: "3", Order: 2}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("settled %+v, want %+v", got, want)
	}
	if got, _ := st.SettledAt(nil, nil); fmt.Sprint(got) != fmt.Sprint(st.Resolutions) {
		t.Errorf("nothing found replaced the kept records: %+v", got)
	}
}
