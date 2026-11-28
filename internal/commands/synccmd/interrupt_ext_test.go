package synccmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// With the SIGTERM grace, a deferred step the interrupt kills returns to the
// run before the handler has exited, and the run would go on to print the
// step as owed, pin refs/wt-sync-result/<branch>/<epoch> and remove the plan,
// leaving the handler's "wt sync undo --force puts it back" stale. The
// goroutine that ran the killed step parks instead, so the rebase stands as
// the handler says, with no result pinned and nothing owed.
func TestInterruptDuringADeferredStepPinsNoResult(t *testing.T) {
	// Not parallel: signals every git process group in the test binary.
	if testing.Short() {
		t.Skip("interrupts a real run with a signal")
	}
	ctx, _ := provisiontest.RunFixture(t, false)
	main := ctx.Repo.MainRoot
	started := filepath.Join(t.TempDir(), "started")
	yaml, err := os.ReadFile(filepath.Join(main, ".wt-sync.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, main, ".wt-sync.yaml", string(yaml)+"defer:\n  - run: touch "+started+"; sleep 30\n    paths: [v.txt]\n")
	testkit.GitIn(t, main, "commit", "-q", "-am", "declare a slow step")
	testkit.GitIn(t, main, "fetch", "-q", "origin")

	var out testkit.LockedBuffer
	go func() { _ = synccmd.SyncRebase(ctx, []string{"bump"}, synccmdtest.NoAgents(), &out) }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the step never started:\n%s", out.String())
		}
	}
	before := gittest.Parked()
	n := git.Interrupt()
	if n < 1 {
		t.Fatalf("Interrupt signalled %d groups, want the step", n)
	}
	// Once the step's caller is parked, the run can no longer go on to its
	// failure path.
	gittest.WaitParked(t, before, n)
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.ResultPrefix); refs != "" {
		t.Errorf("the run pinned a result after the interrupt: %s", refs)
	}
	if s := out.String(); strings.Contains(s, "owed") || strings.Contains(s, "✗") {
		t.Errorf("the run reported the killed step:\n%s", s)
	}
}

// An interrupt while a run writes the handover leaves the rebase stopped in
// the worktree, with no sidecar yet to explain it. It has to name the
// worktree and what puts it back, as an interrupt during the rebase does.
func TestSyncRunInterruptedWhileHandingOverSaysHowToPutItBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("interrupts a real run with a signal")
	}
	if os.Getenv(testkit.InterruptChild) == "" {
		out, status := testkit.InterruptedChild(t)
		// The --json summary the signal handler wrote, among what was said.
		before, object, found := strings.Cut(out, `{"schema":`)
		if !found {
			t.Fatalf("no result object:\n%s", out)
		}
		line, after, _ := strings.Cut(`{"schema":`+object, "\n")
		said := before + after
		if status != kit.InterruptStatus || !strings.Contains(said, "interrupted while rebasing bump") || !strings.Contains(said, "rebase --abort") {
			t.Fatalf("status %d; the interrupt did not say how to put bump back:\n%s", status, out)
		}
		r := synccmdtest.DecodeSyncRun(t, []byte(line))
		p := synccmdtest.Participant(t, r, "feat_wt/bump")
		if r.Detail != syncrun.DetailSummary || r.Outcome != jsonout.OutcomeInterrupted || p.Result != syncrun.ResultInterrupted {
			t.Errorf("detail %q outcome %q result %q", r.Detail, r.Outcome, p.Result)
		}
		for what, recovery := range map[string]*string{"the run's": r.Recovery, "bump's": p.Recovery} {
			if recovery == nil || !strings.Contains(*recovery, "rebase --abort") {
				t.Errorf("%s recovery %v does not say how to put bump back", what, recovery)
			}
		}
		return
	}
	ctx, bump := provisiontest.ContestedFixture(t)
	gitDir, err := wtsync.GitDir(bump)
	if err != nil {
		t.Fatal(err)
	}
	testkit.InterruptWhileHandingOver(t, gitDir, func() bool {
		_, err := os.Stat(wtsync.PlanPath(gitDir))
		return err == nil
	})
	opts := synccmdtest.NoAgents()
	opts.Journal = syncrun.NewSyncRunJournal(os.Stdout, "sync rebase")
	opts.Journal.Summarize()
	err = synccmd.SyncRebase(ctx, []string{"bump"}, opts, os.Stdout)
	t.Fatalf("the run returned (%v) instead of being interrupted", err)
}
