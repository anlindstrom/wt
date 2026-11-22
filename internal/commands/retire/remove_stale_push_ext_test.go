package retire_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/quarantinecmd/quarantinecmdtest"
	"github.com/anlindstrom/wt/internal/commands/retire"
	"github.com/anlindstrom/wt/internal/commands/testkit"
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
