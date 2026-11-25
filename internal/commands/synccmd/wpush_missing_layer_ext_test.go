package synccmd_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// wt#210: a push record that lacks a layer the run moved must not push the
// rest: the push refuses, names the layer and how to go on, sends nothing,
// and keeps the record and every local branch where they were.
func TestPushLaterRefusesAMovedLayerMissingFromTheRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	rec, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	rec.Items = slices.DeleteFunc(rec.Items, func(it wtsync.PushRecordItem) bool { return it.Branch == "s2" })
	if err := wtsync.WritePushRecord(main, rec); err != nil {
		t.Fatal(err)
	}
	remote, local := bareRefs(t, p.bare), testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	recBefore, _, _ := wtsync.ReadPushRecord(main, gittest.StackTmp)

	var out bytes.Buffer
	popts := syncfix.PushLaterOptions{}
	popts.Push = syncrun.PushAlways
	err = syncfix.SyncPushLater(p.ctx, gittest.StackTmp, popts, &out)
	if err == nil || !strings.Contains(err.Error(), "s2") {
		t.Fatalf("push: err %v, want a refusal naming s2\n%s", err, out.String())
	}
	for _, want := range []string{"s2 differs from origin/s2, and the push does not send it (a run moved it and the record holds no push for it)", "wt sync push tmp/stack --stack --forget"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the remote changed:\nbefore %s\nafter %s", remote, got)
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != local {
		t.Errorf("local branches changed:\nbefore %s\nafter %s", local, got)
	}
	if after, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); !ok || after.Rev != recBefore.Rev {
		t.Errorf("the record was not kept as it was")
	}
}

// A record written before the runs kept the line's layers cannot show that
// every moved layer is in it: the push refuses it, says why and how to go on,
// and changes nothing.
func TestPushLaterRefusesARecordThatNeverKeptTheLayers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	rec, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	rec.Layers, rec.LayersRead = nil, false
	if err := wtsync.WritePushRecord(main, rec); err != nil {
		t.Fatal(err)
	}
	remote := bareRefs(t, p.bare)
	var out bytes.Buffer
	popts := syncfix.PushLaterOptions{}
	popts.Push = syncrun.PushAlways
	err = syncfix.SyncPushLater(p.ctx, gittest.StackTmp, popts, &out)
	if err == nil {
		t.Fatalf("push went through\n%s", out.String())
	}
	for _, want := range []string{"cannot show that every moved layer is in it", "wt sync push tmp/stack --stack --forget"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the remote changed:\nbefore %s\nafter %s", remote, got)
	}
	if _, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); !ok {
		t.Error("the record was not kept")
	}
}
