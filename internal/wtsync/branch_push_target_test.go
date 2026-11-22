package wtsync

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A remote-tracking ref that is a symbolic ref to a local branch must not move
// that branch: the update is of the tracking ref itself.
func TestUpdateTrackingMovesNoBranchBehindASymref(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	a := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "branch", "other")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "b")
	b := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "symbolic-ref", "refs/remotes/origin/s1", "refs/heads/other")
	_ = UpdateTracking(dir, "refs/remotes/origin/s1", b, a)
	if got := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "refs/heads/other")); got != a {
		t.Errorf("the local branch other moved to %s through the tracking ref, want it at %s", got, a)
	}
}

// With no lease the tracking ref must not exist yet: one created meanwhile is
// left where it is.
func TestUpdateTrackingWithNoLeaseLeavesAnExistingRef(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	a := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/s1", a)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "b")
	b := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	if err := UpdateTracking(dir, "refs/remotes/origin/s1", b, ""); err == nil {
		t.Error("an update expecting no ref went through over an existing one")
	}
	if got := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "refs/remotes/origin/s1")); got != a {
		t.Errorf("origin/s1 moved to %s, want it left at %s", got, a)
	}
}

// A recorded relative path is that directory, never a configured remote of
// the same name.
func TestPushAtomicGoesToTheRecordedPathNotARemoteOfThatName(t *testing.T) {
	parent := t.TempDir()
	dir := gittest.NewRepo(t, parent, "r")
	tip := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "init", "-q", "--bare", "backup")
	other := filepath.Join(parent, "other.git")
	gittest.Git(t, parent, "init", "-q", "--bare", other)
	gittest.Git(t, dir, "remote", "add", "backup", other)
	if err := PushAtomic(dir, "backup", []AtomicRef{{RemoteBranch: "s1", New: tip}}, time.Minute); err != nil {
		t.Fatalf("PushAtomic: %v", err)
	}
	if out, _ := gittest.Try(t, other, "rev-parse", "--verify", "-q", "refs/heads/s1"); strings.TrimSpace(out) != "" {
		t.Errorf("the push went to the remote named backup, %s", other)
	}
	if got, _ := gittest.Try(t, filepath.Join(dir, "backup"), "rev-parse", "refs/heads/s1"); strings.TrimSpace(got) != tip {
		t.Errorf("the recorded path backup has s1 at %q, want %s", strings.TrimSpace(got), tip)
	}
}

// A recorded URL that git's url.<base>.pushInsteadOf (or insteadOf) would
// rewrite again is refused, and nothing is pushed anywhere.
func TestPushAtomicRefusesAnEndpointGitWouldRewrite(t *testing.T) {
	parent := t.TempDir()
	dir := gittest.NewRepo(t, parent, "r")
	tip := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
	recorded := filepath.Join(parent, "recorded.git")
	elsewhere := filepath.Join(parent, "elsewhere.git")
	gittest.Git(t, parent, "init", "-q", "--bare", recorded)
	gittest.Git(t, parent, "init", "-q", "--bare", elsewhere)
	gittest.Git(t, dir, "config", "url."+elsewhere+".pushInsteadOf", recorded)
	if err := PushAtomic(dir, recorded, []AtomicRef{{RemoteBranch: "s1", New: tip}}, time.Minute); err == nil {
		t.Error("a push to a URL git rewrites went ahead")
	}
	for _, bare := range []string{recorded, elsewhere} {
		if out, _ := gittest.Try(t, bare, "rev-parse", "--verify", "-q", "refs/heads/s1"); strings.TrimSpace(out) != "" {
			t.Errorf("%s got s1", bare)
		}
	}
}

// git applies an empty insteadOf prefix to every URL, and a rewrite base may
// have a space in it: both are rewrites the check must see.
func TestPushAtomicRefusesAnEmptyOrSpacedRewritePrefix(t *testing.T) {
	for name, set := range map[string]func(t *testing.T, dir, recorded, elsewhere string){
		"an empty prefix": func(t *testing.T, dir, _, elsewhere string) {
			gittest.Git(t, dir, "config", "url."+elsewhere+".pushInsteadOf", "")
		},
		"a base with a space": func(t *testing.T, dir, recorded, _ string) {
			spaced := filepath.Join(filepath.Dir(recorded), "some where", "x.git")
			gittest.Git(t, filepath.Dir(recorded), "init", "-q", "--bare", spaced)
			gittest.Git(t, dir, "config", "url."+spaced+".insteadOf", recorded)
		},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := gittest.NewRepo(t, parent, "r")
			tip := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
			recorded := filepath.Join(parent, "recorded.git")
			elsewhere := filepath.Join(parent, "elsewhere.git")
			gittest.Git(t, parent, "init", "-q", "--bare", recorded)
			gittest.Git(t, parent, "init", "-q", "--bare", elsewhere)
			set(t, dir, recorded, elsewhere)
			// Refused by wt for the rewrite, not failed by git somewhere else.
			if err := PushAtomic(dir, recorded, []AtomicRef{{RemoteBranch: "s1", New: tip}}, time.Minute); err == nil || !strings.Contains(err.Error(), "would rewrite") {
				t.Errorf("a push to a URL git rewrites must be refused for it: %v", err)
			}
			if _, err := RemoteRefs(dir, recorded, []string{"s1"}, time.Minute); err == nil || !strings.Contains(err.Error(), "would rewrite") {
				t.Errorf("a read-back from a URL git rewrites must be refused for it: %v", err)
			}
		})
	}
}
