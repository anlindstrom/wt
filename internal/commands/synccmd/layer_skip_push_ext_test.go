package synccmd_test

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// The line a --no-push run ends with spells the plural, and, once an earlier
// run's branches are in the record, says how many of the count it did not
// move itself: run A moves s1, s2, s4 and the line, run B only s4 and the line.
func TestNoPushLineSaysBranchesAndHowManyAreFromEarlierRuns(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	run := func(epoch int64, layers ...string) string {
		main := p.ctx.Repo.MainRoot
		if epoch != epochA {
			commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
			testkit.GitIn(t, main, "fetch", "-q", "origin")
		}
		opts := synccmdtest.At(synccmdtest.NoAgents(), epoch)
		opts.Push = syncrun.PushNever
		opts.Stack = &synccmd.StackOptions{Layers: layers}
		_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
		return human
	}
	const tail = ": wt sync push tmp/stack --stack pushes it later\n"
	if human := run(epochA); !strings.Contains(human, "↪ 4 branches left unpushed, with their leases"+tail) {
		t.Errorf("run A:\n%s", human)
	}
	if human := run(epochB, "s4"); !strings.Contains(human, "↪ 4 branches left unpushed (2 of them from earlier runs), with their leases"+tail) {
		t.Errorf("run B:\n%s", human)
	}
}
