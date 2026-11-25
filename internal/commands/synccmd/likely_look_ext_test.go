package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func commitFile(t *testing.T, dir, name, content, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitOut(t, dir, "add", "-A")
	testkit.GitOut(t, dir, "commit", "-q", "-m", subject)
}

// The case of the issue: the first stop is a conflict that is yours, and the
// look also names the later commits that change a file trunk changed, grouped
// by file, without moving anything.
func TestLookListsEveryLikelyStopGroupedByFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a look over real repositories and worktrees")
	}
	ctx, bump := provisiontest.ContestedFixture(t)
	commitFile(t, bump, "a.txt", "branch again\n", "second edit of a")
	commitFile(t, bump, "z.txt", "z\n", "own file")
	commitFile(t, bump, "a.txt", "branch thrice\n", "third edit of a")
	main := ctx.Repo.MainRoot
	tip := testkit.GitOut(t, main, "rev-parse", "feat_wt/bump")

	var detail bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "bump", synccmd.SyncOptions{NoFetch: true}, &detail); err != nil {
		t.Fatalf("%v\n%s", err, detail.String())
	}
	for _, want := range []string{
		"⚠ likely to stop again, by overlap alone (git may still merge them)",
		"\n  a.txt\n    2/4  second edit of a\n    4/4  third edit of a\n",
	} {
		if !strings.Contains(detail.String(), want) {
			t.Errorf("missing %q in\n%s", want, detail.String())
		}
	}
	if strings.Contains(detail.String(), "own file") && strings.Contains(detail.String(), "3/4  own file") {
		t.Errorf("a commit on a file trunk left alone is listed:\n%s", detail.String())
	}

	row := synccmdtest.OverviewRow(t, synccmdtest.OverviewJSON(t, ctx), "bump")
	if len(row.LikelyStops) != 2 || row.LikelyStops[0].Index != 2 || row.LikelyStops[1].Index != 4 || row.LikelyStops[0].Paths[0] != "a.txt" {
		t.Errorf("likelyStops %+v", row.LikelyStops)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "feat_wt/bump"); got != tip {
		t.Errorf("the look moved the branch from %s to %s", tip, got)
	}
	if st := testkit.GitOut(t, bump, "status", "--porcelain"); st != "" {
		t.Errorf("the look left the worktree dirty:\n%s", st)
	}

	var run bytes.Buffer
	_ = synccmd.SyncRebase(ctx, []string{"bump"}, synccmdtest.NoAgents(), &run)
	if !strings.Contains(run.String(), "contested at 1/4: the run stops there and writes a plan\n  ⚠ likely to stop again") ||
		!strings.Contains(run.String(), "    2/4  second edit of a") {
		t.Errorf("the run's look lacks the likely stops:\n%s", run.String())
	}
}

// A stack commit trunk carries under the same subject and author is named in
// the look with the trunk commit, and the branch keeps it.
func TestLookNamesACommitTrunkCarriesReworked(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a look over real repositories and worktrees")
	}
	ctx, bump := provisiontest.ContestedFixture(t)
	subject := testkit.GitOut(t, bump, "log", "-1", "--format=%s")
	main := ctx.Repo.MainRoot
	commitFile(t, main, "d.txt", "reworked\n", subject)
	testkit.GitOut(t, main, "fetch", "-q", "origin")
	trunk := testkit.GitOut(t, main, "rev-parse", "--short=7", "HEAD")

	var detail bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "bump", synccmd.SyncOptions{NoFetch: true}, &detail); err != nil {
		t.Fatalf("%v\n%s", err, detail.String())
	}
	if want := "may already be on trunk as " + trunk + " (same subject and author); wt keeps it and replays it"; !strings.Contains(detail.String(), want) {
		t.Errorf("missing %q in\n%s", want, detail.String())
	}
	row := synccmdtest.OverviewRow(t, synccmdtest.OverviewJSON(t, ctx), "bump")
	if len(row.ReworkedOnTrunk) != 1 || row.ReworkedOnTrunk[0].Subject != subject || !strings.HasPrefix(row.ReworkedOnTrunk[0].TrunkCommit, trunk) {
		t.Errorf("reworkedOnTrunk %+v", row.ReworkedOnTrunk)
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", "origin/main..feat_wt/bump"); n == "0" {
		t.Errorf("the look dropped the commit")
	}
}

// A likely stop that git merges is no stop in the run: the look names the
// overlap on gen.txt, the run stops once, at the conflict that is yours, and
// after the resume the later commit sits on trunk with both sides' lines.
func TestLikelyStopThatMergesCleanDoesNotStopTheRun(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run and resume over real repositories")
	}
	ctx, bump := provisiontest.ContestedFixture(t)
	commitFile(t, bump, "gen.txt", "head\nstale\n", "edit above gen")
	commitFile(t, bump, "z.txt", "z\n", "own file")
	main := ctx.Repo.MainRoot
	commitFile(t, main, "gen.txt", "stale\ntrunk-tail\n", "trunk appends to gen")
	testkit.GitOut(t, main, "fetch", "-q", "origin")
	other := testkit.GitOut(t, main, "rev-parse", "feat_wt/other")

	var detail bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "bump", synccmd.SyncOptions{NoFetch: true}, &detail); err != nil {
		t.Fatalf("%v\n%s", err, detail.String())
	}
	if want := "  gen.txt\n    2/3  edit above gen\n"; !strings.Contains(detail.String(), want) {
		t.Fatalf("missing %q in\n%s", want, detail.String())
	}

	_, st := synccmdtest.HandOverNow(t, ctx, bump)
	if st.Stop != 1 || st.Total != 3 {
		t.Fatalf("the run stopped at %d/%d, want 1/3, the one stop predicted for certain", st.Stop, st.Total)
	}
	testkit.WriteFile(t, bump, "a.txt", "merged by hand\n")
	testkit.GitOut(t, bump, "add", "--", "a.txt")
	var out bytes.Buffer
	opts := syncfix.ResumeOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
	if err := syncfix.SyncResume(ctx, "bump", opts, &out); err != nil {
		t.Fatalf("resume: %v\n%s", err, out.String())
	}
	if busy, err := wtsync.RebaseInProgress(bump); err != nil || busy {
		t.Fatalf("the resume stopped again (%v, %v):\n%s", busy, err, out.String())
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", "origin/main..feat_wt/bump"); n != "3" {
		t.Errorf("%s commits above trunk, want 3", n)
	}
	if got := testkit.GitOut(t, bump, "show", "HEAD~1:gen.txt"); got != "head\nstale\ntrunk-tail" {
		t.Errorf("gen.txt after the replay is %q", got)
	}
	if got := testkit.GitOut(t, bump, "show", "HEAD~2:a.txt"); got != "merged by hand" {
		t.Errorf("a.txt after the replay is %q", got)
	}
	if st := testkit.GitOut(t, bump, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean:\n%s", st)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "feat_wt/other"); got != other {
		t.Errorf("an untouched branch moved from %s to %s", other, got)
	}
}
