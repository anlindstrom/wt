package wtsync

import (
	"slices"
	"testing"
)

// After a step folds its output into a layer, a run under targets checks
// again the owner the output went into, each commit a fixup had been folded
// into, and the tip: not the commits between.
func TestTargetsOfKeepsTheOwnerTheTargetsAndTheTip(t *testing.T) {
	t.Parallel()
	d := &driver{foldTargets: []bool{false, false, true, false, false}}
	got := d.targetsOf([]string{"owner", "plain", "target", "plain2", "tip"})
	if want := []string{"owner", "target", "tip"}; !slices.Equal(got, want) {
		t.Errorf("rechecked %q, want %q", got, want)
	}
}

// Without a record of which commits took a fixup every rewritten commit is
// checked again: more checks, never fewer.
func TestTargetsOfWithoutARecordKeepsEveryCommit(t *testing.T) {
	t.Parallel()
	d := &driver{}
	all := []string{"a", "b", "c"}
	if got := d.targetsOf(all); !slices.Equal(got, all) {
		t.Errorf("rechecked %q, want %q", got, all)
	}
}

func TestEachCommitModeTargets(t *testing.T) {
	t.Parallel()
	if m, err := EachCommitMode("targets"); err != nil || m != EachTargets {
		t.Errorf("mode %q, err %v", m, err)
	}
	if note := EachCommitNote(EachTargets, true); note != "each_commit checks run at the commits a fixup folded into and at the tip of each layer (--each-commit=targets): the commits between are not checked, and are not owed" {
		t.Errorf("note %q", note)
	}
}
