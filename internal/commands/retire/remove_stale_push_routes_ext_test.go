package retire_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"

	"github.com/anlindstrom/wt/internal/commands/retire"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// movedRemote moves branch on origin by a commit and fetches it, as another
// machine's push would.
func movedRemote(t *testing.T, main, branch string) {
	t.Helper()
	bare := strings.TrimSpace(testkit.GitOut(t, main, "config", "--get", "remote.origin.url"))
	clone := filepath.Join(t.TempDir(), "clone")
	testkit.GitOut(t, filepath.Dir(clone), "clone", "-q", "-b", branch, bare, clone)
	testkit.GitIn(t, clone, "-c", "user.name=O", "-c", "user.email=o@example.com", "commit", "-q", "--allow-empty", "-m", "moved on")
	testkit.GitIn(t, clone, "push", "-q", "origin", branch)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
}

// removeKept is wt remove with a quarantine, and what it printed.
func removeKept(t *testing.T, ctx *kit.Context, path string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := retire.RemoveAt(ctx, path, retire.RemoveOptions{Quarantine: testkit.TrashFor(t, ctx)}, &out)
	return out.String(), err
}

// untouched says a refused removal changed nothing: the worktree, the branch
// under its own name, and no kept copy.
func untouched(t *testing.T, ctx *kit.Context, path string) {
	t.Helper()
	if !testkit.Exists(path) || !ctx.Repo.BranchExists("fix_wt/stale-push") || ctx.Repo.BranchExists("stale-push") {
		t.Errorf("a refused removal changes nothing: worktree there %v, branch there %v, copy there %v",
			testkit.Exists(path), ctx.Repo.BranchExists("fix_wt/stale-push"), ctx.Repo.BranchExists("stale-push"))
	}
}

// A branch tracking a remote branch of another name is pushed there only by
// push.default=upstream: git's simple refuses it. Under upstream, a moved-on
// upstream is a route and goes.
func TestRemoveDropsAnUpstreamOfAnotherNameThatPushDefaultUses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, path, "push", "-q", "-u", "origin", "fix_wt/stale-push:upstream-name")
	testkit.GitIn(t, main, "config", "--local", "push.default", "upstream")
	movedRemote(t, main, "upstream-name")
	tip := strings.TrimSpace(testkit.GitOut(t, path, "rev-parse", "HEAD"))
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if r, m := branchConfig(t, main, "stale-push", "remote"), branchConfig(t, main, "stale-push", "merge"); r != "" || m != "" {
		t.Errorf("the kept copy still tracks the remote branch that moved on: remote %q merge %q\n%s", r, m, out)
	}
	if !strings.Contains(out, "origin/upstream-name has moved on") {
		t.Errorf("the removal should say it:\n%s", out)
	}
	if got := strings.TrimSpace(testkit.GitOut(t, main, "rev-parse", "refs/heads/stale-push")); got != tip {
		t.Errorf("the kept branch is at %s, want %s", got, tip)
	}
}

// A branch that only pulls from a remote branch that moved on, as one cut
// from origin/main does, keeps that upstream: nothing pushes there.
func TestRemoveKeepsAnUpstreamNothingPushesTo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, main, "push", "-q", "origin", "main")
	testkit.GitIn(t, path, "branch", "--set-upstream-to=origin/main")
	testkit.GitIn(t, main, "config", "--local", "push.default", "current")
	movedRemote(t, main, "main")
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if r, m := branchConfig(t, main, "stale-push", "remote"), branchConfig(t, main, "stale-push", "merge"); r != "origin" || m != "refs/heads/main" {
		t.Errorf("the upstream it only pulls from is kept: remote %q merge %q\n%s", r, m, out)
	}
	if got := branchConfig(t, main, "stale-push", "wtPushTo"); got != "origin fix_wt/stale-push" {
		t.Errorf("its push destination is kept: %q", got)
	}
	if strings.Contains(out, "has moved on") {
		t.Errorf("nothing it pushes to moved on:\n%s", out)
	}
}

// The upstream dropped, the kept copy still pushes its own name by default,
// and git refuses that unless forced. The removal says so rather than that it
// won't push there.
func TestRemoveSaysWhenTheKeptNameStillPushesToTheRemoteThatMovedOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	// Pushed under the name it will be kept as, and tracking it.
	testkit.GitIn(t, path, "push", "-q", "-u", "origin", "fix_wt/stale-push:stale-push")
	movedRemote(t, main, "stale-push")
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if r, m := branchConfig(t, main, "stale-push", "remote"), branchConfig(t, main, "stale-push", "merge"); r != "" || m != "" {
		t.Errorf("the upstream goes: remote %q merge %q", r, m)
	}
	if want := "a plain git push from stale-push still goes to origin/stale-push, which has moved on; git refuses it unless forced"; !strings.Contains(out, want) {
		t.Errorf("the removal should say %q:\n%s", want, out)
	}
	if strings.Contains(out, "won't push there") {
		t.Errorf("a route remains, so it must not say it won't push:\n%s", out)
	}
}

// A refspec that forces, from the kept copy to a branch that moved on, makes
// a plain git push overwrite it. The removal refuses before changing
// anything, and the plan says so; the remote's setting is left alone.
func TestRemoveRefusesWhenAForcingPushRefspecReachesTheRemoteThatMovedOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	for _, tc := range []struct{ name, refspec string }{
		{"HEAD", "+HEAD:refs/heads/fix_wt/stale-push"},
		{"the kept branch", "+refs/heads/stale-push:refs/heads/fix_wt/stale-push"},
		{"a pattern", "+refs/heads/*:refs/heads/fix_wt/*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, path := stalePushWorktree(t, remoteBehind)
			main := ctx.Repo.MainRoot
			testkit.GitIn(t, main, "config", "--local", "remote.origin.push", tc.refspec)
			out, err := removeKept(t, ctx, path)
			if err == nil {
				t.Fatalf("the removal must refuse:\n%s", out)
			}
			for _, want := range []string{"remote.origin.push", tc.refspec, "origin/fix_wt/stale-push", "git branch -D"} {
				if !strings.Contains(out+err.Error(), want) {
					t.Errorf("the refusal should name %q:\n%s\n%v", want, out, err)
				}
			}
			untouched(t, ctx, path)
			if got := strings.TrimSpace(testkit.GitOut(t, main, "config", "--local", "--get", "remote.origin.push")); got != tc.refspec {
				t.Errorf("the remote's setting is shared and left alone: %q", got)
			}
		})
	}
}

// A refspec without + is refused by git unless forced, so it is said and the
// removal goes ahead.
func TestRemoveSaysWhenAnUnforcedPushRefspecReachesTheRemoteThatMovedOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteBehind)
	testkit.GitIn(t, ctx.Repo.MainRoot, "config", "--local", "remote.origin.push", "HEAD:refs/heads/fix_wt/stale-push")
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if want := "remote.origin.push still sends stale-push to origin/fix_wt/stale-push, which has moved on; git refuses it unless forced"; !strings.Contains(out, want) {
		t.Errorf("the removal should say %q:\n%s", want, out)
	}
}

// A refspec whose destination nothing else names is compared too: the kept
// copy's own destination is level, and the one the refspec forces is not.
func TestRemoveRefusesAForcingRefspecWhoseDestinationIsTheOnlyOneThatMovedOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, path, "push", "-q", "origin", "fix_wt/stale-push:review")
	movedRemote(t, main, "review")
	testkit.GitIn(t, main, "config", "--local", "remote.origin.push", "+HEAD:refs/heads/review")
	out, err := removeKept(t, ctx, path)
	if err == nil || !strings.Contains(out+err.Error(), "origin/review") {
		t.Fatalf("the removal must refuse, naming origin/review: %v\n%s", err, out)
	}
	untouched(t, ctx, path)
}

// Git reads its configuration from every scope, and so does wt.
func TestRemoveReadsAPushRefspecFromAnIncludedFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteBehind)
	main := ctx.Repo.MainRoot
	extra := filepath.Join(t.TempDir(), "extra.conf")
	if err := os.WriteFile(extra, []byte("[remote \"origin\"]\n\tpush = +HEAD:refs/heads/fix_wt/stale-push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, main, "config", "--local", "include.path", extra)
	out, err := removeKept(t, ctx, path)
	if err == nil || !strings.Contains(out+err.Error(), "remote.origin.push") {
		t.Fatalf("the removal must refuse over the included refspec: %v\n%s", err, out)
	}
	untouched(t, ctx, path)
}

// A refspec on origin is not lost to a remote named origin/team that claims
// origin's team/x by prefix.
func TestRemoveReadsTheRefspecOfTheRemoteAMovedBranchIsOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, path, "push", "-q", "origin", "fix_wt/stale-push:team/x")
	// git remote add refuses the name; the configuration takes it.
	testkit.GitIn(t, main, "config", "--local", "remote.origin/team.url", filepath.Join(t.TempDir(), "other.git"))
	testkit.GitIn(t, main, "config", "--local", "branch.fix_wt/stale-push.wtPushTo", "origin team/x")
	movedRemote(t, main, "team/x")
	testkit.GitIn(t, main, "config", "--local", "remote.origin.push", "+HEAD:refs/heads/team/x")
	out, err := removeKept(t, ctx, path)
	if err == nil || !strings.Contains(out+err.Error(), "origin/team/x") {
		t.Fatalf("the removal must refuse over origin's refspec: %v\n%s", err, out)
	}
	untouched(t, ctx, path)
}

// A refspec that sends the branch elsewhere is not a route to the remote
// branch that moved on, and nothing is said of it.
func TestRemoveIgnoresAPushRefspecToAnotherBranch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteBehind)
	testkit.GitIn(t, ctx.Repo.MainRoot, "config", "--local", "remote.origin.push", "+HEAD:refs/heads/elsewhere")
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if strings.Contains(out, "remote.origin.push") {
		t.Errorf("no route to say:\n%s", out)
	}
}

// git's other forcing forms reach the moved remote too: +: pushes every branch
// to the same name there, and a source is any revision, such as HEAD~0. Each
// is refused like +HEAD:.
func TestRemoveRefusesTheMatchingAndRevisionForcingRefspecs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	for _, tc := range []struct{ name, refspec, dest string }{
		{"matching", "+:", "stale-push"},
		{"a revision of HEAD", "+HEAD~0:refs/heads/review", "review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, path := stalePushWorktree(t, remoteEqual)
			main := ctx.Repo.MainRoot
			testkit.GitIn(t, path, "push", "-q", "origin", "fix_wt/stale-push:"+tc.dest)
			movedRemote(t, main, tc.dest)
			testkit.GitIn(t, main, "config", "--local", "remote.origin.push", tc.refspec)
			out, err := removeKept(t, ctx, path)
			if err == nil || !strings.Contains(out+err.Error(), "origin/"+tc.dest) {
				t.Fatalf("the removal must refuse, naming origin/%s: %v\n%s", tc.dest, err, out)
			}
			untouched(t, ctx, path)
		})
	}
}

// A refspec on the remote a plain push goes to overrides push.default, so an
// upstream that only push.default would push to is no route: one that moved
// on (origin/main here) takes nothing from a branch that is level with where
// it does push.
func TestRemoveKeepsAnUpstreamARefspecOverrides(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: real repositories with an origin to push to")
	}
	ctx, path := stalePushWorktree(t, remoteEqual)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, main, "push", "-q", "origin", "main")
	testkit.GitIn(t, path, "push", "-q", "origin", "fix_wt/stale-push:review")
	testkit.GitIn(t, path, "branch", "--set-upstream-to=origin/main")
	testkit.GitIn(t, main, "config", "--local", "push.default", "upstream")
	testkit.GitIn(t, main, "config", "--local", "remote.origin.push", "HEAD:refs/heads/review")
	movedRemote(t, main, "main")
	before := branchConfig(t, main, "fix_wt/stale-push", "wtPushTo")
	out, err := removeKept(t, ctx, path)
	if err != nil {
		t.Fatalf("RemoveAt: %v\n%s", err, out)
	}
	if r, m := branchConfig(t, main, "stale-push", "remote"), branchConfig(t, main, "stale-push", "merge"); r != "origin" || m != "refs/heads/main" {
		t.Errorf("the upstream a refspec overrides is kept: remote %q merge %q\n%s", r, m, out)
	}
	if got := branchConfig(t, main, "stale-push", "wtPushTo"); before != "" && got == "" {
		t.Errorf("where wt pushes the branch is kept (was %q):\n%s", before, out)
	}
	if strings.Contains(out, "has moved on") {
		t.Errorf("nothing it pushes to moved on:\n%s", out)
	}
}
