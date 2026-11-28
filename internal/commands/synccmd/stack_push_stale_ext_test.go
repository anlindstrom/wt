package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

const staleNotice = "older runs' leases are stale: the remote matches run 2000; "

// pushByHand puts every branch run A moved on its remote and fetches, as a
// push made outside wt does: the record still holds A's leases.
func (p *pushedLine) pushByHand(t *testing.T) {
	t.Helper()
	main := p.ctx.Repo.MainRoot
	for _, b := range movedBranches {
		remote := "origin"
		if to, ok, _ := p.ctx.Repo.PushTo(b); ok {
			remote = to.Remote
		}
		testkit.GitIn(t, main, "push", "-q", "--force", remote, "refs/heads/"+b+":refs/heads/"+b)
	}
	testkit.GitIn(t, main, "fetch", "-q", "--all")
}

// handPushedLine is run A, a push by hand of what it left, then run B of s4
// and the line, both runs --no-push.
func handPushedLine(t *testing.T, p *pushedLine) {
	t.Helper()
	p.runAt(t, epochA)
	p.pushByHand(t)
	p.runAt(t, epochB, "s4")
}

func (p *pushedLine) wantRemoteAtTips(t *testing.T, human string) {
	t.Helper()
	for _, b := range movedBranches {
		if got, want := refIn(t, p.bare, b), p.localTip(t, b); got != want {
			t.Errorf("remote %s is at %s, want the newest tip %s\n%s", b, got, want, human)
		}
	}
}

// A hand push between two --no-push runs leaves the older run's leases stale:
// the push goes over the newer run's, says so once, and --dry-run says it
// would.
func TestPushLaterPushesOverTheNewestLeasesAfterAHandPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, ask := range map[string]bool{"--yes": false, "a yes to the question": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			handPushedLine(t, p)
			before := bareRefs(t, p.bare)
			dry, err := p.pushLater(t, true)
			if err != nil || strings.Count(dry, staleNotice+"would push over run 2000's leases") != 1 || bareRefs(t, p.bare) != before {
				t.Fatalf("--dry-run: err %v, want it to say once what it would push over\n%s", err, dry)
			}
			var human bytes.Buffer
			opts := syncfix.PushLaterOptions{}
			if ask {
				opts.ConfirmPush = func([]string) bool { return true }
			} else {
				opts.Push = syncrun.PushAlways
			}
			if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human); err != nil {
				t.Fatalf("push: %v\n%s", err, human.String())
			}
			p.wantRemoteAtTips(t, human.String())
			if got := strings.Count(human.String(), staleNotice+"pushing over run 2000's leases"); got != 1 {
				t.Errorf("the notice is said %d times, want once\n%s", got, human.String())
			}
		})
	}
}

// A run that pushes, with an earlier unpushed run behind it, takes the same
// rule and says so once, asked or not.
func TestRunPushGoesOverTheNewestLeasesAfterAHandPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, ask := range map[string]bool{"--push": false, "a yes to the question": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			p.runAt(t, epochA)
			p.pushByHand(t)
			main := p.ctx.Repo.MainRoot
			commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
			testkit.GitIn(t, main, "fetch", "-q", "origin")
			opts := synccmdtest.At(synccmdtest.NoAgents(), epochB)
			if ask {
				opts.ConfirmPush = func([]string) bool { return true }
			} else {
				opts.Push = syncrun.PushAlways
			}
			opts.Stack = &synccmd.StackOptions{Layers: []string{"s4"}}
			_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
			if err != nil && !strings.Contains(err.Error(), "held") {
				t.Fatalf("run: %v\n%s", err, human)
			}
			p.wantRemoteAtTips(t, human)
			if got := strings.Count(human, staleNotice+"pushing over run 2000's leases"); got != 1 {
				t.Errorf("the notice is said %d times, want once\n%s", got, human)
			}
		})
	}
}

// The notice says a push is being made: not before a yes, and not when the
// checks after the yes refuse it.
func TestNoStaleNoticeForAPushThatIsNotMade(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, answer := range map[string]func(t *testing.T, p *pushedLine) bool{
		"declined": func(*testing.T, *pushedLine) bool { return false },
		"refused after the yes": func(t *testing.T, p *pushedLine) bool {
			commitOnRemote(t, p.ctx.Repo.MainRoot, p.bare, "s4", "theirs", false)
			return true
		},
	} {
		t.Run("later, "+name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			handPushedLine(t, p)
			var human bytes.Buffer
			opts := syncfix.PushLaterOptions{}
			opts.ConfirmPush = func([]string) bool { return answer(t, p) }
			err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human)
			if err == nil || strings.Contains(human.String(), "pushing over") {
				t.Errorf("err %v, want a refusal with no notice\n%s", err, human.String())
			}
		})
		t.Run("the run's own, "+name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			p.runAt(t, epochA)
			p.pushByHand(t)
			main := p.ctx.Repo.MainRoot
			commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
			testkit.GitIn(t, main, "fetch", "-q", "origin")
			opts := synccmdtest.At(synccmdtest.NoAgents(), epochB)
			opts.ConfirmPush = func([]string) bool { return answer(t, p) }
			opts.Stack = &synccmd.StackOptions{Layers: []string{"s4"}}
			_, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
			if strings.Contains(human, "pushing over") || strings.Contains(human, "✓ pushed") {
				t.Errorf("want no notice and no push\n%s", human)
			}
		})
	}
}

// A remote that is anywhere but at the newest run's lease refuses the whole
// push in one line, cause first; --dry-run lists the branches.
func TestPushLaterRefusesARemoteNotAtTheNewestLease(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const (
		oneOff = "1 branch: the recorded runs' leases differ and the remote is not at the newest run's"
		unread = "2 branches: the recorded runs' leases differ and origin could not be read"
	)
	for name, c := range map[string]struct {
		cause string
		move  func(t *testing.T, p *pushedLine)
	}{
		"at the older run's lease": {oneOff, func(t *testing.T, p *pushedLine) {
			testkit.GitIn(t, p.bare, "update-ref", "refs/heads/s4", p.s.Tips["s4"])
		}},
		"moved after the newest run": {oneOff, func(t *testing.T, p *pushedLine) {
			commitOnRemote(t, p.ctx.Repo.MainRoot, p.bare, "s4", "theirs", false)
		}},
		"unreadable": {unread, func(t *testing.T, p *pushedLine) {
			if err := os.Rename(p.bare, p.bare+".away"); err != nil {
				t.Fatal(err)
			}
			p.bare += ".away"
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			handPushedLine(t, p)
			c.move(t, p)
			before, pushes := bareRefs(t, p.bare), pushCount(t, p.hooks)
			human, err := p.pushLater(t, false)
			if err == nil || !strings.HasPrefix(err.Error(), "not pushed: "+c.cause) || strings.Contains(err.Error(), "\n") {
				t.Fatalf("err %v, want one line starting with %q\n%s", err, c.cause, human)
			}
			if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != pushes {
				t.Errorf("something was pushed\n%s", human)
			}
			dry, err := p.pushLater(t, true)
			if err == nil || !strings.Contains(dry, "✗ s4: ") || strings.Contains(dry, "would push") {
				t.Errorf("--dry-run: err %v, want s4 listed and nothing said it would push\n%s", err, dry)
			}
			if _, ok, _ := wtsync.ReadPushRecord(p.ctx.Repo.MainRoot, gittest.StackTmp); !ok {
				t.Error("the record is gone")
			}
		})
	}
}

// Every disputed branch is read before the first remote is pushed: a mismatch
// on the second remote leaves the first untouched.
func TestPushLaterReadsEveryRemoteBeforeItPushesTheFirst(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	other := p.addRemote(t, "second")
	testkit.GitIn(t, main, "config", "branch.s4.wtPushTo", "second s4")
	handPushedLine(t, p)
	testkit.GitIn(t, other, "update-ref", "refs/heads/s4", p.s.Tips["s4"])
	before, pushes := bareRefs(t, p.bare), pushCount(t, p.hooks)
	human, err := p.pushLater(t, false)
	if err == nil || !strings.Contains(err.Error(), "not at the newest run's") {
		t.Fatalf("err %v, want the second remote refused\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != pushes {
		t.Errorf("origin was pushed\n%s", human)
	}
}

// --dry-run holds every branch of a remote's atomic push to what the remote
// has, disputed or not: one git would refuse takes its whole group with it.
func TestPushLaterDryRunPredictsTheWholeAtomicPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	second := p.addRemote(t, "second")
	testkit.GitIn(t, main, "config", "branch.s4.wtPushTo", "second s4")
	handPushedLine(t, p)
	commitOnRemote(t, main, p.bare, "s2", "theirs", false)
	before := bareRefs(t, p.bare)
	dry, err := p.pushLater(t, true)
	if err == nil || !strings.Contains(dry, "✗ s2: origin/s2 is not at ") || strings.Contains(dry, "would push s1 ") ||
		strings.Contains(dry, "would push "+gittest.StackTmp) || !strings.Contains(dry, "would push s4 ") {
		t.Errorf("err %v, want origin's group refused over s2 and second's s4 to go\n%s", err, dry)
	}
	human, err := p.pushLater(t, false)
	if got := bareRefs(t, p.bare); err == nil || got != before {
		t.Errorf("err %v; origin is\n%s\nwas\n%s\n%s", err, got, before, human)
	}
	if got, want := refIn(t, second, "s4"), p.localTip(t, "s4"); got != want {
		t.Errorf("second/s4 is at %s, want %s, as the dry run said\n%s", got, want, human)
	}
}

// --dry-run is git's own dry run of each atomic push, with no hook of ours:
// it fails where git would for more than a ref's value, and announces no push
// over newer leases for a remote whose push would not go.
func TestPushLaterDryRunIsGitsOwnDryRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, c := range map[string]struct {
		refuse func(t *testing.T, p *pushedLine)
		want   string
	}{
		"a remote that takes no atomic push": {func(t *testing.T, p *pushedLine) {
			testkit.GitIn(t, p.bare, "config", "receive.advertiseAtomic", "false")
		}, "does not support --atomic"},
		"a stale lease beside a disputed one": {func(t *testing.T, p *pushedLine) {
			commitOnRemote(t, p.ctx.Repo.MainRoot, p.bare, "s2", "theirs", false)
		}, "s2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			handPushedLine(t, p)
			c.refuse(t, p)
			ran := filepath.Join(t.TempDir(), "hook")
			gittest.WriteExecutable(t, filepath.Join(p.ctx.Repo.MainRoot, ".git", "hooks", "pre-push"), "#!/bin/sh\ntouch '"+ran+"'\n")
			before, pushes := bareRefs(t, p.bare), pushCount(t, p.hooks)
			dry, err := p.pushLater(t, true)
			if err == nil || !strings.Contains(err.Error()+dry, c.want) || strings.Contains(dry, "would push ") {
				t.Errorf("err %v, want the push refused naming %q, with nothing it would push\n%s", err, c.want, dry)
			}
			if _, serr := os.Stat(ran); serr == nil {
				t.Error("the dry run ran the pre-push hook")
			}
			if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != pushes {
				t.Errorf("the dry run pushed\n%s", dry)
			}
		})
	}
}

// A refusal over a changed ref and an unreadable one counts each for what it
// is.
func TestPushLaterCountsChangedAndUnreadBranchesApart(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	other := p.addRemote(t, "second")
	testkit.GitIn(t, main, "config", "branch.s4.wtPushTo", "second s4")
	handPushedLine(t, p)
	testkit.GitIn(t, p.bare, "update-ref", "refs/heads/"+gittest.StackTmp, p.s.Tips[gittest.StackTmp])
	if err := os.Rename(other, other+".away"); err != nil {
		t.Fatal(err)
	}
	human, err := p.pushLater(t, false)
	const changed = "not pushed: 1 branch: the recorded runs' leases differ and the remote is not at the newest run's; 1 more could not be checked, as second could not be read ("
	if err == nil || !strings.HasPrefix(err.Error(), changed) {
		t.Errorf("err %v, want it to start %q\n%s", err, changed, human)
	}
}

// A remote that moves after wt read it, and before git push reads it, is
// refused by the lease the push carries, which moves nothing of that atomic
// push.
func TestPushLaterLeavesACommitMadeAfterTheRead(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	// origin through a helper that, once armed, moves s4 as a push connects.
	dir := t.TempDir()
	arm, helper := filepath.Join(dir, "arm"), filepath.Join(dir, "remote.sh")
	gittest.WriteExecutable(t, helper, "#!/bin/sh\nif [ \"$1\" = receive-pack ] && [ -f '"+arm+"' ]; then\n"+
		"  git --git-dir=\"$2\" update-ref refs/heads/s4 \"$(cat '"+arm+"')\" && rm '"+arm+"'\nfi\nexec git \"$1\" \"$2\"\n")
	testkit.GitIn(t, main, "config", "protocol.ext.allow", "always")
	testkit.GitIn(t, main, "remote", "set-url", "origin", "ext::"+helper+" %s "+p.bare)
	handPushedLine(t, p)
	theirs := commitOnRemote(t, main, p.bare, "s3", "theirs", false)
	want := strings.Replace(bareRefs(t, p.bare), "refs/heads/s4 "+refIn(t, p.bare, "s4"), "refs/heads/s4 "+theirs, 1)
	if err := os.WriteFile(arm, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}
	human, err := p.pushLater(t, false)
	if err == nil || !strings.Contains(human, "s4 moved under its lease") {
		t.Fatalf("err %v, want s4's lease refused\n%s", err, human)
	}
	if got := bareRefs(t, p.bare); got != want {
		t.Errorf("the remote is\n%s\nwant only the competing commit on s4\n%s\n%s", got, want, human)
	}
}
