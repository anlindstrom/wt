package synccmd_test

import (
	"bytes"
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

// wt#210: a fold in place leaves s1 unchanged, but s1 was already off its
// remote branch before the run (an earlier move nobody pushed). The record
// says s1 was unchanged, and the push refuses for it, saying exactly that,
// not that the run moved it; nothing is pushed and the record is kept.
func TestPushLaterRefusesALayerTheRunLeftOffItsRemote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	testkit.GitIn(t, p.bare, "update-ref", "refs/heads/s1", p.s.Base)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	fixup(t, p.s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "s1"); got != p.s.Tips["s1"] {
		t.Fatalf("the fold moved s1 to %s", got)
	}
	rec, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	if len(rec.Layers) == 0 || rec.Layers[0] != (wtsync.PushLayer{Branch: "s1", Result: wtsync.MoveUnchanged, Tip: p.s.Tips["s1"]}) {
		t.Fatalf("the record does not say s1 was unchanged: %+v", rec.Layers)
	}
	remote, local := bareRefs(t, p.bare), testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")

	var out bytes.Buffer
	popts := syncfix.PushLaterOptions{}
	popts.Push = syncrun.PushAlways
	err = syncfix.SyncPushLater(p.ctx, gittest.StackTmp, popts, &out)
	if err == nil {
		t.Fatalf("the push went through\n%s", out.String())
	}
	const want = "s1 differs from origin/s1, and the push does not send it (the runs left it unchanged, so it was already off its remote branch before them): " +
		"pushing the layers above it would leave the stack split on the remote; push s1 first, then push again, or wt sync push tmp/stack --stack --forget drops the record"
	if !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "s1 moved") {
		t.Errorf("the refusal says s1 moved:\n%s", out.String())
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the remote changed:\nbefore %s\nafter %s", remote, got)
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != local {
		t.Errorf("local branches changed:\nbefore %s\nafter %s", local, got)
	}
	if after, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); !ok || after.Rev != rec.Rev {
		t.Errorf("the record was not kept as it was")
	}
}

// A push that sent the layers and left the line out (it would be new on
// origin) keeps the line in the record; the push of it with --push-line then
// goes through: the layers already sent stand at their tips, level with
// their remote branches, and are not sent again.
func TestPushLaterAfterAPartialPushSendsOnlyWhatIsLeft(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	if human, err := m.pushLater(t, false, false); err != nil {
		t.Fatalf("first push: %v\n%s", err, human)
	}
	rec, ok, err := wtsync.ReadPushRecord(m.ctx.Repo.MainRoot, mappedTop)
	if err != nil || !ok {
		t.Fatalf("the line's entry was not kept: %v", err)
	}
	if got := strings.Join(rec.Branches(), ","); got != mappedTop {
		t.Fatalf("the record holds %s after the first push, want only %s", got, mappedTop)
	}
	human, err := m.pushLater(t, false, true)
	if err != nil {
		t.Fatalf("second push: %v\n%s", err, human)
	}
	if got, want := refIn(t, m.bare, mappedTop), m.local(t, mappedTop); got != want {
		t.Errorf("origin %s is at %s, want %s\n%s", mappedTop, got, want, human)
	}
	if !strings.Contains(human, "1 branch\n") {
		t.Errorf("the second push does not send one branch:\n%s", human)
	}
	for _, l := range mappedLayers {
		if strings.Contains(human, "tmp/layer/"+l) {
			t.Errorf("the second push names tmp/layer/%s again:\n%s", l, human)
		}
	}
	if _, ok, _ := wtsync.ReadPushRecord(m.ctx.Repo.MainRoot, mappedTop); ok {
		t.Errorf("the record is still there after everything was pushed")
	}
}

// pushLaterRefused holds a push that must refuse: it errs, says want, and
// leaves origin and the record as they were.
func (m *mappedLine) pushLaterRefused(t *testing.T, want string) {
	t.Helper()
	before := m.refs(t)
	rec, _, _ := wtsync.ReadPushRecord(m.ctx.Repo.MainRoot, mappedTop)
	human, err := m.pushLater(t, false, true)
	if err == nil {
		t.Fatalf("the push went through:\n%s", human)
	}
	if !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	if got := m.refs(t); got != before {
		t.Errorf("origin changed:\nbefore %s\nafter %s", before, got)
	}
	if after, ok, _ := wtsync.ReadPushRecord(m.ctx.Repo.MainRoot, mappedTop); !ok || after.Rev != rec.Rev {
		t.Errorf("the record was not kept as it was")
	}
}

// Run A holds tmp/layer/a, run B moves it, and a push sends the layers and
// leaves the line out. Run A's word that it held a is older than run B's
// move: once a moves again locally, the push of the line refuses for it.
func TestPushLaterHoldsALayerAnOlderRunHeldToItsNewestTip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	wtA := filepath.Join(t.TempDir(), "a")
	testkit.GitIn(t, main, "worktree", "add", "-q", wtA, "tmp/layer/a")
	testkit.WriteFile(t, wtA, "a.txt", "work in progress\n")
	run := func(epoch int64) string {
		opts := synccmdtest.At(synccmdtest.NoAgents(), epoch)
		opts.Push = syncrun.PushNever
		opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
		_, human, _ := synccmdtest.StackRun(t, m.ctx, mappedTop, opts)
		return human
	}
	if human := run(epochA); !strings.Contains(human, "tmp/layer/a held") {
		t.Fatalf("run A did not hold a:\n%s", human)
	}
	// a is moved by hand to where run A would have put it, under b.
	testkit.GitIn(t, wtA, "checkout", "--", "a.txt")
	testkit.GitIn(t, wtA, "reset", "-q", "--hard", "tmp/layer/b^")
	commitOnRemote(t, main, m.bare, "main", "trunk moved on", false)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	if human := run(epochB); strings.Contains(human, "held") || !strings.Contains(human, "✓ tmp/layer/a ") {
		t.Fatalf("run B did not move a:\n%s", human)
	}
	if human, err := m.pushLater(t, false, false); err != nil || strings.Contains(human, "tmp/layer/a left out") {
		t.Fatalf("first push: %v\n%s", err, human)
	}
	testkit.WriteFile(t, wtA, "later.txt", "later\n")
	testkit.GitIn(t, wtA, "add", "later.txt")
	testkit.GitIn(t, wtA, "commit", "-q", "--no-verify", "-m", "feat(a): later")
	m.pushLaterRefused(t, "tmp/layer/a is no longer at ")
}

// A layer that pushes under another name is sent, the line left out, and
// then a teammate pushes to its remote branch: origin/feat_wt/a is ahead of
// the layer, a push to it is no longer safe, and the push of the line refuses
// for it rather than taking it as a layer with no remote branch.
func TestPushLaterComparesARenamedLayerItCannotPushTo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	if human, err := m.pushLater(t, false, false); err != nil {
		t.Fatalf("first push: %v\n%s", err, human)
	}
	commitOnRemote(t, m.ctx.Repo.MainRoot, m.bare, "feat_wt/a", "feat: teammate", false)
	testkit.GitIn(t, m.ctx.Repo.MainRoot, "fetch", "-q", "origin")
	m.pushLaterRefused(t, "tmp/layer/a differs from origin/feat_wt/a")
}
