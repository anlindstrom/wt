package retire

import (
	"testing"

	"github.com/anlindstrom/wt/internal/jsonout"
)

// The outcome rules, as docs/json.md states them.
func TestSweepOutcomeRules(t *testing.T) {
	t.Parallel()
	items := func(results ...string) []*SweepResultItem {
		var out []*SweepResultItem
		for _, r := range results {
			out = append(out, &SweepResultItem{Category: SweepDelete, Result: r})
		}
		out = append(out, &SweepResultItem{Category: SweepInUse, Result: SweepKept})
		return out
	}
	for _, tc := range []struct {
		items []*SweepResultItem
		early bool
		want  string
	}{
		{items(SweepDeleted, SweepRemoved), false, jsonout.OutcomeDone},
		{items(), false, jsonout.OutcomeDone},
		{items(), true, jsonout.OutcomeRefused},
		{items(SweepKept, SweepNotRun), false, jsonout.OutcomeRefused},
		{items(SweepDeleted, SweepKept), false, jsonout.OutcomePartial},
		{items(SweepFailed), false, jsonout.OutcomeRefused},
		{[]*SweepResultItem{{Category: SweepRemove, Result: SweepFailed, WorktreeRemoved: true}}, false, jsonout.OutcomePartial},
	} {
		if got := sweepOutcome(tc.items, tc.early); got != tc.want {
			t.Errorf("%+v early=%v: got %s, want %s", tc.items, tc.early, got, tc.want)
		}
	}
}
