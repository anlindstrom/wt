package synccmd_test

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

const keepTrunkYAML = "conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: keep-trunk\n"

// A stack run says, loudly and once per commit, which commits left nothing
// in the result, each with the layer it was in, and --json carries the same.
func TestSyncRebaseStackNamesTheCommitsItEmptied(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, keepTrunkYAML)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	short := func(sha string) string { return testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "--short=7", sha) }
	bump := s.Tips["s3"]
	for _, want := range []string{
		"dropped as empty: " + short(bump) + " chore(s3): bump to 1.0.1 [layer s3] (",
		"dropped as empty: " + short(s.Tips["s4"]) + " feat(s4): d2.txt [layer s4] (",
		"dropped as empty: " + short(s.Tips["s2"]) + " feat(s2): b3.txt [layer s2] (",
	} {
		if !strings.Contains(human, want) {
			t.Errorf("the summary lacks %q:\n%s", want, human)
		}
	}
	if len(p.Emptied) != 4 || p.TestsOnly == nil || len(p.TestsOnly) != 0 {
		t.Fatalf("emptied %+v, testsOnly %+v", p.Emptied, p.TestsOnly)
	}
	if e := p.Emptied[1]; e.Commit != bump || e.Subject != "chore(s3): bump to 1.0.1" || e.Layer == nil || *e.Layer != "s3" || e.Unmatched {
		t.Errorf("the bump is %+v", e)
	}
}
