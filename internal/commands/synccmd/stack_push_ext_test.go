package synccmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
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
	// origin is the directory name of origin's bare repository.
	origin string
}

func newPushedLine(t *testing.T) *pushedLine {
	t.Helper()
	return newPushedLineWith(t, "")
}

func newPushedLineWith(t *testing.T, yaml string) *pushedLine {
	t.Helper()
	return newPushedLineAt(t, yaml, "origin.git")
}

func newPushedLineAt(t *testing.T, yaml, origin string) *pushedLine {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, yaml)
	main := ctx.Repo.MainRoot
	p := &pushedLine{ctx: ctx, s: s, origin: origin}
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
	name := remote + ".git"
	if remote == "origin" {
		name = p.origin
	}
	bare := filepath.Join(t.TempDir(), name)
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
	res, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil {
		t.Fatalf("expected the held layers to fail the run\n%s", human)
	}
	var order []string
	for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
		order = append(order, b.Branch)
	}
	if strings.Join(order, " ") != "s1 s2 s4 tmp/stack" {
		t.Errorf("push.branches is %v, want the moved layers bottom first, then the line, across remotes", order)
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

// --json gives each pushed branch its remote, remote branch, the lease and
// the commit pushed, and what came of it; a run that pushed nothing has null.
func TestStackPushJSONGivesEachBranchItsLeaseAndOutcome(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	oldTips := map[string]string{}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		oldTips[b] = refIn(t, p.bare, b)
	}
	res, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	row := synccmdtest.Participant(t, res, gittest.StackTmp)
	if row.Stack == nil || row.Stack.Push == nil {
		t.Fatalf("no push in the stack's report\n%s", human)
	}
	var order []string
	for _, b := range row.Stack.Push.Branches {
		order = append(order, b.Branch)
		if b.Remote != "origin" || b.RemoteBranch != b.Branch || b.Outcome != syncrun.PushedOK || b.Why != nil {
			t.Errorf("%s: %+v", b.Branch, b)
		}
		if b.Old == nil || *b.Old != oldTips[b.Branch] || b.New != refIn(t, p.bare, b.Branch) {
			t.Errorf("%s: old %v, new %s; the lease was %s and the remote now has %s", b.Branch, b.Old, b.New, oldTips[b.Branch], refIn(t, p.bare, b.Branch))
		}
	}
	if strings.Join(order, " ") != "s1 s2 s4 tmp/stack" {
		t.Errorf("branches %v, want the moved layers bottom first, then the line", order)
	}
	if !row.Pushed {
		t.Error("the line's row does not say it was pushed")
	}

	// --no-push: nothing pushed, nothing in push.
	q := newPushedLine(t)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	was := bareRefs(t, q.bare)
	res, human, _ = synccmdtest.StackRun(t, q.ctx, gittest.StackTmp, opts)
	if row := synccmdtest.Participant(t, res, gittest.StackTmp); row.Stack == nil || row.Stack.Push != nil {
		t.Errorf("a run that pushed nothing reports a push: %+v\n%s", row.Stack, human)
	}
	if bareRefs(t, q.bare) != was || pushCount(t, q.hooks) != 0 {
		t.Errorf("--no-push pushed")
	}
}

// commitOnRemote pushes a commit with message msg to branch of bare, from a
// clone of its own: amend rewrites the branch's tip instead of adding to it.
func commitOnRemote(t *testing.T, main, bare, branch, msg string, amend bool) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	testkit.GitIn(t, main, "clone", "-q", bare, other)
	testkit.GitIn(t, other, "checkout", "-q", branch)
	id := []string{"-c", "user.name=o", "-c", "user.email=o@example.com", "-c", "core.hooksPath=/dev/null"}
	if amend {
		testkit.GitIn(t, other, append(id, "commit", "-q", "--amend", "-m", msg)...)
		testkit.GitIn(t, other, "push", "-q", "--force", "origin", branch)
	} else {
		if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, other, "add", "-A")
		testkit.GitIn(t, other, append(id, "commit", "-q", "-m", msg)...)
		testkit.GitIn(t, other, "push", "-q", "origin", branch)
	}
	return testkit.GitOut(t, other, "rev-parse", "HEAD")
}

// A teammate's message-only amend of a layer, fetched, is a rewrite of the
// same patch that this branch never held: the layer is left out and the
// teammate's commit stays on the remote. The other branches still go.
func TestStackPushNeverOverwritesATeammatesPatchEquivalentRewrite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	theirs := commitOnRemote(t, main, p.bare, "s2", "chore: reworded by a teammate", true)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if got := refIn(t, p.bare, "s2"); got != theirs {
		t.Errorf("remote s2 is at %s, want the teammate's %s: it was overwritten\n%s", got, theirs, human)
	}
	for _, b := range []string{"s1", "s4"} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("remote %s is at %s, want %s\n%s", b, got, want, human)
		}
	}
	if !strings.Contains(human, "s2 not pushed") && !strings.Contains(human, "no safe push") {
		t.Errorf("the run does not name s2 as left out\n%s", human)
	}
}

// A line whose deferred step is owed is not pushable, and its layers are not
// pushed either.
func TestStackPushPushesNothingForALineWithAnOwedStep(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	const v = "WT_PUSH_OWED_TOKEN"
	t.Setenv(v, "")
	if err := os.Unsetenv(v); err != nil {
		t.Fatal(err)
	}
	p := newPushedLineWith(t, synccmdtest.StackYAML+"defer:\n  - run: ./x.sh\n    paths: [v.txt]\n    needs: [env:"+v+"]\n")
	before := bareRefs(t, p.bare)
	opts := synccmdtest.PushAlways()
	opts.AllowOwed = true
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(human, "owed") {
		t.Fatalf("expected the step to be owed: %v\n%s", err, human)
	}
	if after := bareRefs(t, p.bare); after != before {
		t.Errorf("a line that is not pushable was pushed:\nbefore\n%s\nafter\n%s\n%s", before, after, human)
	}
}

// A line branch whose remote branch is gone is not left to a push of its own
// while its layers go: nothing is pushed, and the run says so.
func TestStackPushWithAnUnpushableLineBranchPushesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	testkit.GitIn(t, main, "branch", "--set-upstream-to=origin/"+gittest.StackTmp, gittest.StackTmp)
	testkit.GitIn(t, p.bare, "update-ref", "-d", "refs/heads/"+gittest.StackTmp)
	testkit.GitIn(t, main, "fetch", "-q", "--prune", "origin")
	before := bareRefs(t, p.bare)
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if err == nil {
		t.Fatalf("expected the run to fail\n%s", human)
	}
	if after := bareRefs(t, p.bare); after != before {
		t.Errorf("the remote changed:\nbefore\n%s\nafter\n%s\n%s", before, after, human)
	}
	if !strings.Contains(human, "stack not pushed") {
		t.Errorf("the run does not say the stack was not pushed\n%s", human)
	}
}

// A hook that declines the push is reported with the hook's own reason, not
// as somebody else having pushed.
func TestStackPushNamesAHooksRefusalAsItIs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	gittest.WriteExecutable(t, filepath.Join(p.bare, "hooks", "pre-receive"), "#!/bin/sh\necho 'no pushes on fridays' >&2\nexit 1\n")
	before := bareRefs(t, p.bare)
	res, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if after := bareRefs(t, p.bare); after != before {
		t.Errorf("the remote changed\n%s", human)
	}
	if strings.Contains(human, "moved under its lease") || strings.Contains(human, "somebody pushed") {
		t.Errorf("a hook's refusal is narrated as a lease failure\n%s", human)
	}
	if !strings.Contains(human, "hook declined") {
		t.Errorf("git's own reason is not printed\n%s", human)
	}
	for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
		if b.Outcome == syncrun.PushedLeaseFailed {
			t.Errorf("%s is leaseFailed: %+v", b.Branch, b)
		}
	}
}

// gitThatFailsAfterPushing puts a git on the PATH whose atomic push does its
// work (or not, per runIt) and then fails as a dropped connection does.
func gitThatFailsAfterPushing(t *testing.T, runIt bool) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	do := ""
	if runIt {
		do = gitBin + ` "$@" >/dev/null 2>&1` + "\n"
	}
	body := "#!/bin/sh\ncase \"$*\" in\n*'push --atomic'*)\n" + do + "echo 'fatal: the remote end hung up unexpectedly' >&2\nexit 128 ;;\nesac\nexec " + gitBin + " \"$@\"\n"
	stub := t.TempDir()
	gittest.WriteExecutable(t, filepath.Join(stub, "git"), body)
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A transport failure is not proof that nothing moved: wt reads the remote's
// refs back and reports each branch as it finds it.
func TestStackPushReadsTheRemoteBackAfterATransportFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	for name, runIt := range map[string]bool{"it went through": true, "it did not": false} {
		t.Run(name, func(t *testing.T) {
			p := newPushedLine(t)
			main := p.ctx.Repo.MainRoot
			gitThatFailsAfterPushing(t, runIt)
			res, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
			if err == nil {
				t.Fatalf("expected the run to fail\n%s", human)
			}
			want := syncrun.PushedNotSent
			if runIt {
				want = syncrun.PushedOK
			}
			for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
				if b.Outcome != want {
					t.Errorf("%s is %s, want %s as the remote shows\n%s", b.Branch, b.Outcome, want, human)
				}
			}
			moved := refIn(t, p.bare, "s1") == testkit.GitOut(t, main, "rev-parse", "refs/heads/s1")
			if moved != runIt {
				t.Errorf("remote s1 moved: %v, want %v", moved, runIt)
			}
			if runIt && strings.Contains(human, "nothing") {
				t.Errorf("the run claims nothing moved though the push went through\n%s", human)
			}
			if runIt {
				for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
					if b.Why != nil {
						t.Errorf("%s: why is %q for a pushed branch, want null", b.Branch, *b.Why)
					}
				}
				if up := testkit.GitOut(t, main, "rev-parse", "--abbrev-ref", gittest.StackTmp+"@{upstream}"); up != "origin/"+gittest.StackTmp {
					t.Errorf("the line's upstream is %q after a push the read-back confirmed", up)
				}
			}
		})
	}
}

// A line branch with no remote branch yet joins the push with an empty
// lease, so the layers and the line go together or not at all.
func TestStackPushCreatesAnAbsentLineBranchInTheSamePush(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	testkit.GitIn(t, p.bare, "update-ref", "-d", "refs/heads/"+gittest.StackTmp)
	testkit.GitIn(t, main, "fetch", "-q", "--prune", "origin")
	opts := synccmdtest.PushAlways()
	opts.PushLine = true // creating the line's branch is asked for: --push-line
	_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	if got, want := refIn(t, p.bare, gittest.StackTmp), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+gittest.StackTmp); got != want {
		t.Errorf("remote %s is at %s, want %s\n%s", gittest.StackTmp, got, want, human)
	}
	if n := pushCount(t, p.hooks); n != 1 {
		t.Errorf("the remote received %d pushes, want one atomic push\n%s", n, human)
	}
}

// With two push URLs configured, no recorded push is safe, and nothing is
// pushed by any path: not as a stack, not as the line's own push.
func TestStackPushWithNoSafeDestinationPushesNothingByAnyPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", elsewhere)
	testkit.GitIn(t, main, "remote", "set-url", "--add", "--push", "origin", p.bare)
	testkit.GitIn(t, main, "remote", "set-url", "--add", "--push", "origin", elsewhere)
	before := bareRefs(t, p.bare)
	_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	if after := bareRefs(t, p.bare); after != before {
		t.Errorf("the remote changed:\nbefore\n%s\nafter\n%s\n%s", before, after, human)
	}
	if refs := bareRefs(t, elsewhere); refs != "" {
		t.Errorf("the other push URL got refs:\n%s", refs)
	}
	if !strings.Contains(human, "stack not pushed") {
		t.Errorf("the run does not say the stack was not pushed\n%s", human)
	}
}

// The push goes to the endpoint the leases were recorded for, not to whatever
// the remote's name resolves to once the person has said yes: a push URL
// changed while the question waits redirects nothing. The remote-tracking
// refs follow the push all the same.
func TestStackPushGoesToTheRecordedEndpointNotTheRemotesName(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", elsewhere)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushAsk
	opts.ConfirmPush = func([]string) bool {
		testkit.GitIn(t, main, "config", "remote.origin.pushurl", elsewhere)
		return true
	}
	_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	if refs := bareRefs(t, elsewhere); refs != "" {
		t.Errorf("the push followed the changed URL:\n%s\n%s", refs, human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		local := testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b)
		if got := refIn(t, p.bare, b); got != local {
			t.Errorf("recorded endpoint's %s is at %s, want %s\n%s", b, got, local, human)
		}
		if got := testkit.GitOut(t, main, "rev-parse", "refs/remotes/origin/"+b); got != local {
			t.Errorf("origin/%s is at %s, want %s: the tracking ref did not follow the push", b, got, local)
		}
	}
}

// The read-back asks the endpoint that was pushed to, not the remote's fetch
// URL, which may have changed to another repository since.
func TestStackPushReadsBackTheEndpointItPushedTo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", elsewhere)
	testkit.GitIn(t, main, "config", "remote.origin.pushurl", p.bare)
	gitThatFailsAfterPushing(t, true)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushAsk
	opts.ConfirmPush = func([]string) bool {
		testkit.GitIn(t, main, "config", "remote.origin.url", elsewhere)
		return true
	}
	res, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
		if b.Outcome != syncrun.PushedOK {
			t.Errorf("%s is %s: the read-back looked at the wrong repository\n%s", b.Branch, b.Outcome, human)
		}
	}
}

// A remote path with a space in it is one URL.
func TestStackPushToARemoteWhosePathHasASpace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineAt(t, "", "origin with space.git")
	main := p.ctx.Repo.MainRoot
	_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.PushAlways())
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("remote %s is at %s, want %s\n%s", b, got, want, human)
		}
	}
}
