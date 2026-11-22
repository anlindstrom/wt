package syncfix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// handedOverPushedLine is a StackLine whose origin is a bare repository holding the
// line, and whose run stopped on a conflict that is the person's.
func handedOverPushedLine(t *testing.T) (ctx *kit.Context, s *gittest.StackLine, bare string) {
	t.Helper()
	ctx, s = synccmdtest.StackLine(t, "conflicts: []\n")
	main := ctx.Repo.MainRoot
	bare = filepath.Join(t.TempDir(), "origin.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", bare)
	testkit.GitIn(t, main, "push", "-q", bare, "refs/heads/*:refs/heads/*")
	testkit.GitIn(t, main, "remote", "set-url", "origin", bare)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "v.txt"), []byte("1.0.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "add", "v.txt")
	return ctx, s, bare
}

func remoteTip(t *testing.T, bare, branch string) string {
	t.Helper()
	return testkit.GitOut(t, bare, "rev-parse", "refs/heads/"+branch)
}

// wt sync resume --push of a --stack run pushes the layers it moves and the
// line in one atomic push.
func TestSyncResumePushesAStacksLayersInOnePush(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	ctx, _, bare := handedOverPushedLine(t)
	main := ctx.Repo.MainRoot
	opts := noResumeAgents()
	opts.Push = syncrun.PushAlways
	res, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the resume to say two layers were held", err)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := remoteTip(t, bare, b), testkit.GitOut(t, main, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("remote %s is at %s, want %s", b, got, want)
		}
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Stack == nil || p.Stack.Push == nil || len(p.Stack.Push.Branches) != 4 || !p.Pushed {
		t.Errorf("push in the report: %+v", p.Stack)
	}
}

// A resume leases on what the remote held when the run began, not on what a
// fetch since then brought: a layer somebody pushed to meanwhile fails its
// lease even though wt's own remote-tracking ref has caught up, and nothing
// on the remote moves.
func TestSyncResumePushLeasesOnWhatTheRunRecorded(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	ctx, s, bare := handedOverPushedLine(t)
	main := ctx.Repo.MainRoot
	other := filepath.Join(t.TempDir(), "other")
	testkit.GitIn(t, main, "clone", "-q", bare, other)
	testkit.GitIn(t, other, "checkout", "-q", "s2")
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, other, "add", "-A")
	testkit.GitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@example.com", "commit", "-q", "-m", "chore: theirs")
	testkit.GitIn(t, other, "push", "-q", "origin", "s2")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	before := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")

	opts := noResumeAgents()
	opts.Push = syncrun.PushAlways
	res, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "stack push failed") {
		t.Fatalf("err %v, want the resume to say the stack push failed", err)
	}
	if after := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Errorf("the remote changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	var moved string
	for _, b := range synccmdtest.Participant(t, res, gittest.StackTmp).Stack.Push.Branches {
		if b.Outcome == syncrun.PushedLeaseFailed {
			moved = b.Branch
			if b.Old == nil || *b.Old != s.Tips["s2"] {
				t.Errorf("the lease was %v, want what the run recorded, %s", b.Old, s.Tips["s2"])
			}
		}
	}
	if moved != "s2" {
		t.Errorf("the branch that moved under its lease is %q, want s2", moved)
	}
}

// A resume does not push through a remote whose URLs changed while the run
// waited: with two push URLs, nothing is pushed to either.
func TestSyncResumePushRefusesARemoteWhoseURLsChanged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	ctx, _, bare := handedOverPushedLine(t)
	main := ctx.Repo.MainRoot
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", elsewhere)
	testkit.GitIn(t, main, "remote", "set-url", "--add", "--push", "origin", bare)
	testkit.GitIn(t, main, "remote", "set-url", "--add", "--push", "origin", elsewhere)
	before := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")
	opts := noResumeAgents()
	opts.Push = syncrun.PushAlways
	var human strings.Builder
	err := SyncResume(ctx, gittest.StackTmp, opts, &human)
	if err == nil {
		t.Fatalf("the resume succeeded\n%s", human.String())
	}
	if after := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Errorf("the remote changed:\n%s", human.String())
	}
	if refs := testkit.GitOut(t, elsewhere, "for-each-ref"); refs != "" {
		t.Errorf("the other push URL got refs:\n%s", refs)
	}
	if !strings.Contains(human.String(), "stack not pushed") {
		t.Errorf("the resume does not say the stack was not pushed\n%s", human.String())
	}
}

// A record written before leases were kept is not pushed as a stack: a fresh
// reading would lease on whatever was fetched since.
func TestSyncResumePushDeclinesARecordWithNoLeases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	ctx, _, bare := handedOverPushedLine(t)
	main := ctx.Repo.MainRoot
	common := testkit.GitOut(t, main, "rev-parse", "--path-format=absolute", "--git-common-dir")
	files, err := filepath.Glob(filepath.Join(common, "wt-sync-stack", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records %v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	delete(rec, "pushes")
	raw, _ = json.Marshal(rec)
	if err := os.WriteFile(files[0], raw, 0o644); err != nil {
		t.Fatal(err)
	}
	before := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")
	opts := noResumeAgents()
	opts.Push = syncrun.PushAlways
	var human strings.Builder
	if err := SyncResume(ctx, gittest.StackTmp, opts, &human); err == nil {
		t.Fatalf("the resume succeeded\n%s", human.String())
	}
	if after := testkit.GitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Errorf("the remote changed:\n%s", human.String())
	}
	if !strings.Contains(human.String(), "stack not pushed") {
		t.Errorf("the resume does not say the stack was not pushed\n%s", human.String())
	}
}
