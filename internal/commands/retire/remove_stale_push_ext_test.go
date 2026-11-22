package retire_test

import (
	"bytes"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/quarantinecmd/quarantinecmdtest"
	"github.com/anlindstrom/wt/internal/commands/retire"
	"github.com/anlindstrom/wt/internal/commands/retire/retiretest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/jsonout"
)

// remoteMoved says how the remote branch stands against the worktree's
// branch once both have gone their ways.
type remoteMoved int

const (
	remoteEqual remoteMoved = iota
	remoteBehind
	remoteDiverged
	remoteAhead
	remoteNotFetched
)

const stalePushWork = "fix/stale-push"

// stalePushWorktree is a worktree whose branch was pushed to a bare origin,
// then left as how says: the remote moved on by a commit (behind), both gone
// their own way (diverged), only the branch moved (ahead), neither (equal),
// or the remote-tracking ref gone (never fetched, or pruned). The remote's
// moves are fetched, so they are what wt last fetched.
func stalePushWorktree(t *testing.T, how remoteMoved) (*kit.Context, string) {
	t.Helper()
	ctx, path := provisiontest.SafetyWorktree(t, stalePushWork)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, path, "commit", "-q", "--allow-empty", "-m", "work")
	bare := filepath.Join(t.TempDir(), "origin.git")
	testkit.GitOut(t, main, "init", "-q", "--bare", bare)
	testkit.GitOut(t, main, "remote", "add", "origin", bare)
	testkit.GitOut(t, path, "push", "-q", "-u", "origin", "fix_wt/stale-push")
	other := func(msg string) {
		clone := filepath.Join(t.TempDir(), "clone")
		testkit.GitOut(t, filepath.Dir(clone), "clone", "-q", "-b", "fix_wt/stale-push", bare, clone)
		testkit.GitIn(t, clone, "-c", "user.name=O", "-c", "user.email=o@example.com", "commit", "-q", "--allow-empty", "-m", msg)
		testkit.GitIn(t, clone, "push", "-q", "origin", "fix_wt/stale-push")
		testkit.GitIn(t, main, "fetch", "-q", "origin")
	}
	switch how {
	case remoteBehind:
		other("moved on")
	case remoteDiverged:
		other("moved on")
		testkit.GitIn(t, path, "commit", "-q", "--allow-empty", "-m", "mine")
	case remoteAhead:
		testkit.GitIn(t, path, "commit", "-q", "--allow-empty", "-m", "mine")
	case remoteNotFetched:
		testkit.GitIn(t, main, "update-ref", "-d", "refs/remotes/origin/fix_wt/stale-push")
	}
	return ctx, path
}

// config is a branch's value for one key, "" when it has none.
func branchConfig(t *testing.T, main, branch, key string) string {
	t.Helper()
	return strings.TrimSpace(testkit.GitTry(t, main, "config", "--local", "--get", "branch."+branch+"."+key))
}

// A kept branch whose remote branch holds commits it lacks does not push
// there, and the removal says why. Ahead, equal and no remote ref are as
// they were. Every case keeps the commits: the branch under its new name, the
// quarantine's pin, and a restore that gives the name back.
func TestRemoveKeepsWhereAKeptBranchPushesOnlyWhenTheRemoteHasNotMovedOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		how   remoteMoved
		stale bool
	}{
		{"behind", remoteBehind, true},
		{"diverged", remoteDiverged, true},
		{"ahead", remoteAhead, false},
		{"equal", remoteEqual, false},
		{"no remote ref", remoteNotFetched, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if testing.Short() {
				t.Skip("integration: real repositories with an origin to push to")
			}
			ctx, path := stalePushWorktree(t, tc.how)
			main := ctx.Repo.MainRoot
			tip := strings.TrimSpace(testkit.GitOut(t, path, "rev-parse", "HEAD"))
			dir := testkit.TrashFor(t, ctx)
			var out bytes.Buffer
			if err := retire.RemoveAt(ctx, path, retire.RemoveOptions{Quarantine: dir}, &out); err != nil {
				t.Fatalf("RemoveAt: %v\n%s", err, out.String())
			}
			pushes := branchConfig(t, main, "stale-push", "wtPushTo")
			remote, merge := branchConfig(t, main, "stale-push", "remote"), branchConfig(t, main, "stale-push", "merge")
			if tc.stale {
				if pushes != "" || remote != "" || merge != "" || branchConfig(t, main, "stale-push", "pushRemote") != "" {
					t.Errorf("a copy the remote moved on from must not push there: wtPushTo %q remote %q merge %q",
						pushes, remote, merge)
				}
				for _, want := range []string{"origin/fix_wt/stale-push has moved on", "stale-push", "won't push there", "wt sync push-to"} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("the removal should say %q:\n%s", want, out.String())
					}
				}
				if strings.Contains(out.String(), "wt still pushes it") {
					t.Errorf("it must not say it still pushes:\n%s", out.String())
				}
			} else {
				if remote != "origin" || merge != "refs/heads/fix_wt/stale-push" {
					t.Errorf("the upstream is kept: remote %q merge %q", remote, merge)
				}
				if strings.Contains(out.String(), "has moved on") {
					t.Errorf("nothing has moved on:\n%s", out.String())
				}
				if tc.how != remoteNotFetched && (pushes != "origin fix_wt/stale-push" ||
					!strings.Contains(out.String(), "; wt still pushes it to origin/fix_wt/stale-push")) {
					t.Errorf("it still pushes where it did: wtPushTo %q\n%s", pushes, out.String())
				}
			}
			// The work is kept either way.
			if got := strings.TrimSpace(testkit.GitOut(t, main, "rev-parse", "refs/heads/stale-push")); got != tip {
				t.Fatalf("the kept branch is at %s, want %s", got, tip)
			}
			if ctx.Repo.BranchExists("fix_wt/stale-push") {
				t.Error("the prefixed name is renamed away")
			}
			if pins := testkit.GitOut(t, main, "for-each-ref", "--format=%(objectname)", "refs/wt-quarantine/"); !strings.Contains(pins, tip) {
				t.Errorf("the quarantine's pin must hold the tip %s:\n%s", tip, pins)
			}
			quarantinecmdtest.Restore(t, ctx, dir)
			if got := strings.TrimSpace(testkit.GitOut(t, main, "rev-parse", "refs/heads/fix_wt/stale-push")); got != tip {
				t.Errorf("restore gives the branch back at %s, want %s", got, tip)
			}
		})
	}
}

// The plan shows the decision, in its text and in --json, and its token
// follows the remote: a plan read while the remote had not moved on no
// longer matches once it has.
func TestRemovePlanShowsThatAKeptBranchWillNotPushToAMovedRemote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	level, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if level.Branch == nil || len(level.Branch.StaleRoutes) != 0 {
		t.Fatalf("a remote level with the branch has not moved on: %+v", level.Branch)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	bare := strings.TrimSpace(testkit.GitOut(t, main, "config", "--get", "remote.origin.url"))
	testkit.GitOut(t, filepath.Dir(clone), "clone", "-q", "-b", "fix_wt/stale-push", bare, clone)
	testkit.GitIn(t, clone, "-c", "user.name=O", "-c", "user.email=o@example.com", "commit", "-q", "--allow-empty", "-m", "moved on")
	testkit.GitIn(t, clone, "push", "-q", "origin", "fix_wt/stale-push")
	testkit.GitIn(t, main, "fetch", "-q", "origin")

	moved, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Branch == nil || len(moved.Branch.StaleRoutes) != 1 || moved.Branch.StaleRoutes[0].Ref != "origin/fix_wt/stale-push" {
		t.Fatalf("the plan should name the remote branch that moved on: %+v", moved.Branch)
	}
	if jsonout.Deref(moved.Token) == jsonout.Deref(level.Token) {
		t.Error("a token taken before the remote moved on must not match after it")
	}
	// A remote that moves on further is a different plan: the token follows
	// the commit compared, not only the name.
	movedRemote(t, main, "fix_wt/stale-push")
	staler, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if jsonout.Deref(staler.Token) == jsonout.Deref(moved.Token) {
		t.Error("a token taken before the stale remote moved further must not match after it")
	}
	moved = staler
	if _, err := retiretest.RemoveJSON(t, ctx, path, retire.RemoveOptions{Expect: jsonout.Deref(level.Token)}); err == nil {
		t.Error("--expect with the old token must refuse")
	}
	if !ctx.Repo.BranchExists("fix_wt/stale-push") {
		t.Fatal("a refused removal changes nothing")
	}

	var text bytes.Buffer
	if err := retire.RemoveAt(ctx, path, retire.RemoveOptions{DryRun: true}, &text); err != nil {
		t.Fatalf("dry run: %v\n%s", err, text.String())
	}
	if want := `origin/fix_wt/stale-push has moved on; the kept copy, stale-push, won't push there`; !strings.Contains(text.String(), want) {
		t.Errorf("the dry run should say %q:\n%s", want, text.String())
	}

	r, err := retiretest.RemoveJSON(t, ctx, path, retire.RemoveOptions{Expect: jsonout.Deref(moved.Token)})
	if err != nil {
		t.Fatalf("%v %+v", err, r)
	}
	if len(r.StaleRoutes) != 1 || r.StaleRoutes[0].Ref != "origin/fix_wt/stale-push" || jsonout.Deref(r.KeepAs) != "stale-push" {
		t.Errorf("the result repeats the decision: staleRoutes %+v keepAs %v", r.StaleRoutes, r.KeepAs)
	}
	if got := branchConfig(t, main, "stale-push", "wtPushTo"); got != "" {
		t.Errorf("the kept copy must not push to the remote that moved on: %q", got)
	}
}

// Each stale route is in the plan and the result with its own destination,
// whichever way it goes. An unforced refspec is left and said; the token
// covers it.
func TestRemoveJSONPairsEachStaleRouteWithItsOwnDestination(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteBehind)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, path, "push", "-q", "origin", "fix_wt/stale-push:review")
	movedRemote(t, main, "review")
	without, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const spec = "HEAD:refs/heads/review"
	testkit.GitIn(t, main, "config", "--local", "remote.origin.push", spec)
	with, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []retire.RemoveStaleRoute{
		{Ref: "origin/fix_wt/stale-push", Via: retire.ViaRecorded, Dropped: true},
		{Ref: "origin/review", Via: retire.ViaRefspec, Refspec: jsonout.Strp("remote.origin.push=" + spec)},
	}
	if !reflect.DeepEqual(with.Branch.StaleRoutes, want) {
		t.Fatalf("routes %+v\nwant   %+v", with.Branch.StaleRoutes, want)
	}
	if len(without.Branch.StaleRoutes) != 1 || without.Branch.StaleRoutes[0].Ref != "origin/fix_wt/stale-push" {
		t.Errorf("without the refspec: %+v", without.Branch.StaleRoutes)
	}
	if jsonout.Deref(with.Token) == jsonout.Deref(without.Token) {
		t.Error("the token covers the routes")
	}
	r, err := retiretest.RemoveJSON(t, ctx, path, retire.RemoveOptions{Expect: jsonout.Deref(with.Token)})
	if err != nil {
		t.Fatalf("%v %+v", err, r)
	}
	if !reflect.DeepEqual(r.StaleRoutes, want) {
		t.Errorf("the result repeats them: %+v", r.StaleRoutes)
	}
}

// A refspec that forces over a remote branch that moved on is a problem of
// the plan with its own code, which refuses it and its token.
func TestRemoveJSONRefusesAForcingRefspecAsAProblem(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteBehind)
	testkit.GitIn(t, ctx.Repo.MainRoot, "config", "--local", "remote.origin.push", "+HEAD:refs/heads/fix_wt/stale-push")
	p, err := retire.RemovePlanOf(t, ctx, path, retire.RemoveOptions{})
	if err == nil || p.Token != nil {
		t.Fatalf("a refused plan has an error and no token: %v %v", err, p.Token)
	}
	if codes := retire.ProblemCodes(p.Problems); !slices.Contains(codes, retire.ProblemStaleForcedPush) {
		t.Errorf("problems %v", codes)
	}
	rs := p.Branch.StaleRoutes
	if len(rs) != 2 || !rs[1].Forces || rs[1].Dropped || rs[1].Via != retire.ViaRefspec {
		t.Errorf("routes %+v", rs)
	}
	r, _ := retiretest.RemoveJSON(t, ctx, path, retire.RemoveOptions{})
	if r.Outcome != retire.RemoveRefused || !ctx.Repo.BranchExists("fix_wt/stale-push") {
		t.Errorf("a refused removal changes nothing: %+v", r)
	}
}
