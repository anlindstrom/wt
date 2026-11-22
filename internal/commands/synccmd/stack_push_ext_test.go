package synccmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// pushedLine is a StackLine whose origin is a bare repository that holds the
// line as the run finds it, fetched: every branch is in sync with its own
// remote branch, so a layer has a lease and a push.
type pushedLine struct {
	ctx  *kit.Context
	s    *gittest.StackLine
	bare string
	// hooks is a file the bare repository's pre-receive hook appends a line
	// to for each push it receives.
	hooks string
}

func newPushedLine(t *testing.T) *pushedLine {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	p := &pushedLine{ctx: ctx, s: s}
	p.bare = p.addRemote(t, "origin")
	p.hooks = filepath.Join(t.TempDir(), "pushes.log")
	hook := "#!/bin/sh\necho push >> '" + p.hooks + "'\n"
	gittest.WriteExecutable(t, filepath.Join(p.bare, "hooks", "pre-receive"), hook)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	return p
}

// addRemote makes a bare repository holding every branch of the line and
// points remote at it.
func (p *pushedLine) addRemote(t *testing.T, remote string) string {
	t.Helper()
	main := p.ctx.Repo.MainRoot
	bare := filepath.Join(t.TempDir(), remote+".git")
	testkit.GitIn(t, main, "init", "-q", "--bare", bare)
	testkit.GitIn(t, main, "push", "-q", bare, "refs/heads/*:refs/heads/*")
	if remote == "origin" {
		testkit.GitIn(t, main, "remote", "set-url", "origin", bare)
	} else {
		testkit.GitIn(t, main, "remote", "add", remote, bare)
	}
	testkit.GitIn(t, main, "fetch", "-q", remote)
	return bare
}

// refs is every ref the bare repository holds, as "name sha" lines.
func bareRefs(t *testing.T, bare string) string {
	t.Helper()
	return testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")
}

func refIn(t *testing.T, bare, branch string) string {
	t.Helper()
	return testkit.GitOut(t, bare, "rev-parse", "refs/heads/"+branch)
}

func pushCount(t *testing.T, log string) int {
	t.Helper()
	raw, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "push")
}

// --push on a --stack run pushes every moved layer and the line's own branch
// in one push, each over a lease on what the remote had; a held layer is
// left out and named, and its remote branch stays where it was.
func TestStackPushPushesEveryMovedLayerInOnePush(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the run to say two layers were held\n%s", err, human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		local := testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b)
		if got := refIn(t, p.bare, b); got != local {
			t.Errorf("remote %s is at %s, want the run's new tip %s\n%s", b, got, local, human)
		}
		if local == p.s.Tips[b] {
			t.Errorf("%s did not move", b)
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := refIn(t, p.bare, b); got != p.s.Tips[b] {
			t.Errorf("held %s was pushed: remote at %s, want %s", b, got, p.s.Tips[b])
		}
		if !strings.Contains(human, b+" held") {
			t.Errorf("the run does not name %s as held\n%s", b, human)
		}
	}
	if n := pushCount(t, p.hooks); n != 1 {
		t.Errorf("the remote received %d pushes, want one atomic push\n%s", n, human)
	}
}

// A lease that fails on one layer moves nothing on the remote: the remote's
// refs are exactly as before, the run names the layer that moved under it,
// every local layer stays where the rebase put it, and wt sync undo puts the
// line back.
func TestStackPushFailedLeaseMovesNothingAndNamesTheLayer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	// Somebody else pushed to s2 after wt last fetched.
	other := filepath.Join(t.TempDir(), "other")
	testkit.GitIn(t, main, "clone", "-q", p.bare, other)
	testkit.GitIn(t, other, "checkout", "-q", "s2")
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, other, "add", "-A")
	testkit.GitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@example.com", "commit", "-q", "-m", "theirs")
	testkit.GitIn(t, other, "push", "-q", "origin", "s2")
	before := bareRefs(t, p.bare)
	hooksBefore := pushCount(t, p.hooks)

	res, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil {
		t.Fatalf("the run succeeded over a failed lease\n%s", human)
	}
	if after := bareRefs(t, p.bare); after != before {
		t.Errorf("the remote changed under a failed atomic push:\nbefore\n%s\nafter\n%s\n%s", before, after, human)
	}
	if n := pushCount(t, p.hooks) - hooksBefore; n != 0 {
		t.Errorf("the remote's hook ran %d times for a push that should have been refused before any ref moved", n)
	}
	if !strings.Contains(human, "s2") || !strings.Contains(human, "moved") {
		t.Errorf("the run does not say s2 moved under its lease\n%s", human)
	}
	layers := layersOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	for _, b := range []string{"s1", "s2", "s4"} {
		if got := testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != str(layers[b].NewTip) || got == p.s.Tips[b] {
			t.Errorf("local %s is at %s, want the rebase's %s", b, got, str(layers[b].NewTip))
		}
	}
	var undone strings.Builder
	undo := syncfix.UndoOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, undo, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != p.s.Tips[b] {
			t.Errorf("after undo %s is at %s, want %s", b, got, p.s.Tips[b])
		}
	}
}

// A layer with no remote branch of its own to push to is named and left out;
// the others are still pushed.
func TestStackPushLeavesOutALayerWithNowhereToPush(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	testkit.GitIn(t, p.bare, "update-ref", "-d", "refs/heads/s1")
	testkit.GitIn(t, main, "fetch", "-q", "--prune", "origin")
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil {
		t.Fatalf("expected the held layers to fail the run\n%s", human)
	}
	if refs := bareRefs(t, p.bare); strings.Contains(refs, "refs/heads/s1 ") {
		t.Errorf("s1 was created on the remote:\n%s", refs)
	}
	if !strings.Contains(human, "s1 not pushed") && !strings.Contains(human, "not pushed by this run") {
		t.Errorf("the run does not name s1 as left out\n%s", human)
	}
	for _, b := range []string{"s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("remote %s is at %s, want %s", b, got, want)
		}
	}
}

// Layers that push to different remotes get one atomic push per remote.
func TestStackPushIsOneAtomicPushPerRemote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and two bare remotes")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	backup := p.addRemote(t, "backup")
	backupLog := filepath.Join(t.TempDir(), "backup.log")
	gittest.WriteExecutable(t, filepath.Join(backup, "hooks", "pre-receive"), "#!/bin/sh\necho push >> '"+backupLog+"'\n")
	testkit.GitIn(t, main, "config", "branch.s2.pushRemote", "backup")
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil {
		t.Fatalf("expected the held layers to fail the run\n%s", human)
	}
	for _, b := range []string{"s1", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("origin %s is at %s, want %s", b, got, want)
		}
	}
	if got := refIn(t, p.bare, "s2"); got != p.s.Tips["s2"] {
		t.Errorf("s2 went to origin: at %s", got)
	}
	if got, want := refIn(t, backup, "s2"), testkit.GitOut(t, main, "rev-parse", "refs/heads/s2"); got != want {
		t.Errorf("backup s2 is at %s, want %s\n%s", got, want, human)
	}
	if a, b := pushCount(t, p.hooks), pushCount(t, backupLog); a != 1 || b != 1 {
		t.Errorf("pushes received: origin %d, backup %d, want one each\n%s", a, b, human)
	}
	if !strings.Contains(human, "one atomic push per remote") {
		t.Errorf("the run does not say atomicity holds per remote\n%s", human)
	}
}
