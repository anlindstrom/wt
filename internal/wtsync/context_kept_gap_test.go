package wtsync

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// Where wt cannot order the fold, a gap is still recorded at the commit the
// line has the context at, with a note: the run's resume check reads the gaps.
func TestContextGapsKeepAGapWhoseOrderCannotBeTold(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// extra goes behind the fixup that lacks its context, given the id
		// of the fixup before that one.
		extra func(first string) [2]string
		// runs is whether the run takes the line.
		runs bool
	}{
		{"a fixup of a fixup in the same group", func(first string) [2]string { return [2]string{"fixup! " + first, "g.txt"} }, true},
		{"a move! commit", func(string) [2]string { return [2]string{"move! feat: a", "g.txt"} }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, wt, cfg := liftRepo(t, nil, nil)
			commitLine(t, wt, "feat: a", "a.txt", "a2\n")
			needs := commitLine(t, wt, "feat: b", "t.txt", "one\n")
			first := commitLine(t, wt, "fixup! feat: a", "a.txt", "a3\n")
			fixup := commitLine(t, wt, "fixup! feat: a", "t.txt", "one\ntwo\n")
			extra := c.extra(first)
			commitLine(t, wt, extra[0], extra[1], "g\n")
			line, err := ReadLine(dir, "feature", "origin/main", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			a, err := AnalyseFixups(wt, line.Commits, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Gaps) != 1 || a.Gaps[0].Fixup != fixup || a.Gaps[0].Needs != needs {
				t.Fatalf("gaps %+v, unchecked %q; want the second fixup needing feat: b", a.Gaps, a.Unchecked)
			}
			const doubt = "wt cannot tell which commit first has the context in the fold"
			if !slices.ContainsFunc(a.Unchecked, func(n string) bool { return strings.Contains(n, doubt) }) {
				t.Errorf("unchecked %q, want one saying %q", a.Unchecked, doubt)
			}
			if got := outcomeOf(t, a, fixup); got.State != FixupNotAnalysed || !strings.Contains(got.Why, doubt) {
				t.Errorf("outcome %+v, want not analysed for it", got)
			}
			if !c.runs {
				return
			}
			req := Request{Path: wt, Branch: "feature", Trunk: "origin/main", Onto: line.Base, Epoch: 9,
				Stack: &StackRequest{Line: line, Autosquash: true}, Gaps: a.Gaps}
			var log bytes.Buffer
			res, err := Rebase(dir, cfg, req, &log)
			if err != nil || res.Left == nil || res.Left.Commit != fixup || !slices.Equal(res.Left.Context, a.Gaps) {
				t.Errorf("err %v, left %+v: want a handover at the fixup with its gap\n%s", err, res.Left, log.String())
			}
		})
	}
}
