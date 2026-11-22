package wtsync

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/git"
)

// BranchPush is where one branch of a stack run pushes and the commit that
// push replaces, read before the run moved anything: the remote-tracking
// value as last fetched, which a push is leased on. A run keeps it in its
// record, so a resume pushes on the lease the run took and not on a
// remote-tracking ref a later fetch moved.
type BranchPush struct {
	Branch string `json:"branch"`
	// Remote and RemoteBranch are where it pushes; both "" when nowhere is
	// safe, and Why says why.
	Remote       string `json:"remote"`
	RemoteBranch string `json:"remoteBranch"`
	// Lease is the commit the remote branch must be at for the push to go
	// through; "" says the branch must not exist there yet.
	Lease string `json:"lease"`
	// SetUpstream says the branch has no upstream, so a push that creates
	// or replaces its remote branch gives it one.
	SetUpstream bool `json:"setUpstream,omitempty"`
	// Endpoint is the one URL the remote pushed to when the run recorded the
	// lease: a push goes through only while the remote still means that.
	Endpoint string `json:"endpoint,omitempty"`
	// Tracking is the remote-tracking ref (in full) a push to Remote would
	// have moved: a push to the endpoint's URL moves none, so the run does.
	Tracking string `json:"tracking,omitempty"`
	Why      string `json:"why,omitempty"`
	// Rule is what decided where it pushes: recorded, or its own name.
	Rule string `json:"rule,omitempty"`
}

// Pushes reports that the branch has a safe push.
func (b BranchPush) Pushes() bool { return b.Remote != "" }

// Creates reports that the push would make the remote branch, and that
// nothing recorded says it should: a line's own branch with no remote branch
// of its own yet. A stack push leaves such a branch out unless asked.
func (b BranchPush) Creates() bool { return b.Pushes() && b.Lease == "" && b.Rule != "recorded" }

// BranchPush is the push of a branch of a stack run, from the state the
// branch stood in against its own remote. The line's own branch (top) may be
// new on its remote, as a branch's first push is; a layer needs a remote
// branch of its own to be leased on. Neither is pushed over commits it never
// had.
func (o *Own) BranchPush(branch string, top bool) BranchPush {
	bp := BranchPush{Branch: branch}
	if o == nil {
		bp.Why = "its own remote was not read"
		return bp
	}
	to := o.Push(branch)
	if to.Remote == "" {
		bp.Why = to.Why
		if to.Undecided != nil {
			bp.Why = to.Undecided.Hint()
		}
		return bp
	}
	st := o.State(branch)
	switch st.State {
	case OwnInSync, OwnAhead:
		bp.Lease = st.Commit
	case OwnRebased:
		// Patch for patch is not ownership: a teammate's reworded commit
		// matches too, and an explicit lease would replace it. Only a tip
		// this branch once had is a version of its own.
		if !formerTip(o.mainRoot, branch, st.Commit) {
			bp.Why = to.Ref() + " is at a commit this branch was never at, so it cannot be shown to hold only old versions of this branch"
			return bp
		}
		bp.Lease = st.Commit
	case OwnNone:
		if !top {
			bp.Why = "there is no remote branch of its own yet"
			return bp
		}
	case OwnGone:
		bp.Why = "there is no remote branch of its own yet"
		return bp
	case OwnBehind, OwnDiverged:
		bp.Why = to.Ref() + " has commits this branch never had"
		return bp
	default:
		bp.Why = "it cannot be compared with " + to.Ref() + ": " + st.Why
		return bp
	}
	// One destination, as the same-name push reads it: a mirror, several push
	// URLs, or a push URL that is another repository than the fetch URL is
	// no place for an atomic push.
	r := o.remotes[to.Remote]
	if r == nil {
		bp.Why = "there is no remote " + to.Remote
		return bp
	}
	r = o.urls(to.Remote)
	switch {
	case r.mirror:
		bp.Why = to.Remote + " is a mirror remote"
		return bp
	case len(r.pushURLs) != 1:
		bp.Why = to.Remote + " has " + strconv.Itoa(len(r.pushURLs)) + " push URLs"
		return bp
	case len(r.fetchURLs) == 0 || !sameRepository(r.pushURLs[0], r.fetchURLs[0]):
		bp.Why = to.Remote + " pushes to another URL than it fetches from"
		return bp
	}
	bp.Endpoint = r.pushURLs[0]
	bp.Remote, bp.RemoteBranch, bp.SetUpstream, bp.Tracking, bp.Rule = to.Remote, to.Branch, to.SetUpstream, to.Tracking, to.Rule
	return bp
}

// PushEndpoint is the one URL remote pushes to now. It is an error for the
// remote to have none or several: a push to several URLs is not atomic.
func PushEndpoint(dir, remote string) (string, error) {
	out, err := gitEnv(dir, nil, nil, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return "", err
	}
	// One URL a line, as git prints them: a path may have a space in it.
	var urls []string
	for _, u := range strings.Split(out, "\n") {
		if u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) != 1 {
		return "", fmt.Errorf("%s has %d push URLs", remote, len(urls))
	}
	return urls[0], nil
}

// RemoteRefs is the commit each of branches is at on target (a URL), read
// from the remote itself ("" for a branch it does not have): what a push that ended
// in a transport failure is judged by.
func RemoteRefs(dir, target string, branches []string, timeout time.Duration) (map[string]string, error) {
	target, err := pushTarget(dir, target)
	if err != nil {
		return nil, err
	}
	args := []string{"ls-remote", target}
	for _, b := range branches {
		args = append(args, "refs/heads/"+b)
	}
	out, err := git.RunTimeout(dir, timeout, args...)
	if err != nil {
		return nil, err
	}
	got := map[string]string{}
	for _, b := range branches {
		got[b] = ""
	}
	for _, l := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(l, "\t")
		if name, isHead := strings.CutPrefix(ref, "refs/heads/"); ok && isHead {
			got[name] = sha
		}
	}
	return got, nil
}

// AtomicRef is one ref of an atomic push: the remote branch, the commit it
// must be at ("", absent) and the commit it is set to.
type AtomicRef struct{ RemoteBranch, Lease, New string }

// PushAtomic is one git push --atomic of refs to target, the URL the leases
// were recorded for (never a remote name, which a config change can point
// elsewhere), each over a lease
// on the commit it names. It writes the remote's refs only: no local branch
// moves, and git moves all of the remote's refs or none.
func PushAtomic(dir, target string, refs []AtomicRef, timeout time.Duration) error {
	target, err := pushTarget(dir, target)
	if err != nil {
		return err
	}
	args := []string{"push", "--atomic"}
	for _, r := range refs {
		args = append(args, "--force-with-lease=refs/heads/"+r.RemoteBranch+":"+r.Lease)
	}
	args = append(args, target)
	for _, r := range refs {
		args = append(args, r.New+":refs/heads/"+r.RemoteBranch)
	}
	_, err = git.RunTimeout(dir, timeout, args...)
	return err
}

// pushTarget is the recorded endpoint as git push and ls-remote must be given
// it to reach that repository and no other: a relative local path made
// absolute, so a configured remote of the same name can't stand in for it. An
// endpoint git would rewrite again (url.<base>.insteadOf or pushInsteadOf) is
// refused, since the push would go to the rewritten URL.
func pushTarget(dir, endpoint string) (string, error) {
	u := endpoint
	if localPath(u) && !filepath.IsAbs(u) {
		u = filepath.Join(dir, u)
	}
	if remotes, err := git.Run(dir, "remote"); err == nil {
		for _, r := range strings.Split(remotes, "\n") {
			if r != "" && r == u {
				return "", fmt.Errorf("%s is also the name of a remote; not pushing to it", u)
			}
		}
	}
	// Exit 1 is no such setting; any other failure refuses, since the
	// rewrite can't be ruled out.
	// -z: entries end in NUL and a key ends in a newline, so a base with a
	// space in it is read whole. An empty prefix (or none) matches every URL,
	// as git applies it.
	out, _, err := gitEnvAllow(dir, nil, nil, 1, "config", "-z", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if err != nil {
		return "", fmt.Errorf("reading url rewrites: %w", err)
	}
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		key, prefix, _ := strings.Cut(entry, "\n")
		if strings.HasPrefix(u, prefix) {
			return "", fmt.Errorf("%s would rewrite %s; not pushing to it", key, u)
		}
	}
	return u, nil
}

// localPath is a URL git reads as a path on this machine: no scheme, and no
// host: before the first slash as an scp-like address has.
func localPath(u string) bool {
	if strings.Contains(u, "://") {
		return false
	}
	colon, slash := strings.Index(u, ":"), strings.Index(u, "/")
	return colon < 0 || (slash >= 0 && slash < colon)
}

// SetUpstream makes branch track remote/remoteBranch, after a push gave it
// that remote branch.
func SetUpstream(dir, remote, remoteBranch, branch string) error {
	_, err := git.Run(dir, "branch", "--set-upstream-to="+remote+"/"+remoteBranch, branch)
	return err
}

// UpdateTracking moves a remote-tracking ref to commit after a push to a URL,
// which git does not do for one; lease is the commit it was at, "" for a ref
// that was not there. It fails, and moves nothing, if the ref is elsewhere.
func UpdateTracking(dir, tracking, commit, lease string) error {
	if !strings.HasPrefix(tracking, "refs/remotes/") {
		return fmt.Errorf("%s is not a remote-tracking ref", tracking)
	}
	// --no-deref: a tracking ref that is a symbolic ref is replaced, and the
	// branch it points at never moves. An empty old value means the ref must
	// not exist, so one made meanwhile is left alone.
	_, err := git.Run(dir, "update-ref", "--no-deref", tracking, commit, lease)
	if err != nil && trackingAt(dir, tracking, commit) {
		// git moved it itself, as it does after a push to a URL a remote
		// fetches from: already where the push put it.
		return nil
	}
	return err
}

// trackingAt is whether tracking is a plain ref (not a symbolic one) at commit.
func trackingAt(dir, tracking, commit string) bool {
	if _, code, err := gitEnvAllow(dir, nil, nil, 1, "symbolic-ref", "-q", tracking); err != nil || code == 0 {
		return false
	}
	at, err := git.Run(dir, "rev-parse", "--verify", "-q", tracking)
	return err == nil && strings.TrimSpace(at) == commit
}
