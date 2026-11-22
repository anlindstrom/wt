package provision

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
)

// Where wt new's base commit comes from, as the plan and the result say it.
const (
	// BaseLocal is the local trunk branch.
	BaseLocal = "local"
	// BaseRemote is origin/<trunk>, because local trunk is behind it.
	BaseRemote = "remote"
	// BaseGiven is what --base named.
	BaseGiven = "base"
)

// trunkFetchTimeout bounds wt new's fetch of trunk. It is wt sync's.
const trunkFetchTimeout = 45 * time.Second

// baseChoice is where wt new cuts from when --base is not given: the newer
// of local trunk and origin/<trunk>, as last fetched.
type baseChoice struct {
	// Ref is what the worktree is cut from, Source where that is.
	Ref, Source string
	// Note is the line that names the base, with the reason when it is not
	// local trunk, and Commit the commit it names.
	Note, Commit string
	// local is local trunk's commit as it was compared, the old value the
	// fast-forward is guarded by; empty when there is no local trunk.
	local string
	// behind is set when Ref is origin/<trunk> and local trunk exists, an
	// ancestor of it: local trunk could be brought up to it.
	behind bool
}

// fetchTrunk fetches trunk from origin, as wt sync does: the same remote,
// the same ref, the same timeout. It does nothing for --base, --no-fetch or a
// repository with no origin, and it returns the line to print when the fetch
// failed, which does not stop wt new: it goes on with what was last fetched.
func fetchTrunk(ctx *kit.Context, opts NewOptions) string {
	trunk := ctx.Config.MainBranch
	if opts.Base != "" || opts.NoFetch || trunk == "" || !ctx.Repo.HasRemote("origin") {
		return ""
	}
	if _, err := git.RunTimeout(ctx.Repo.MainRoot, trunkFetchTimeout, "fetch", "--quiet", "origin", trunk); err != nil {
		return fmt.Sprintf("fetch of origin %s failed (%s); going on with origin/%s as last fetched",
			trunk, kit.FetchReason(err), trunk)
	}
	return ""
}

// chooseTrunkBase reads local trunk and origin/<trunk> as they are now and
// picks the base. A ref git cannot read leaves local trunk, as wt new always
// did, for the plan's own check of it to refuse.
func chooseTrunkBase(ctx *kit.Context) baseChoice {
	trunk := ctx.Config.MainBranch
	c := baseChoice{Ref: trunk, Source: BaseLocal}
	if trunk == "" {
		return c
	}
	local, lok, lerr := ctx.Repo.ResolveRef("refs/heads/" + trunk)
	remote, rok, rerr := ctx.Repo.ResolveRef("refs/remotes/origin/" + trunk)
	c.local = local
	if lerr != nil || rerr != nil || (!rok && !lok) {
		c.Commit = local
		return c
	}
	if !rok {
		c.Commit = local
		c.Note = fmt.Sprintf("cut from %s @ %s", trunk, git.ShortID(local, 10))
		return c
	}
	remoteRef := "origin/" + trunk
	short := git.ShortID
	if !lok {
		return baseChoice{Ref: remoteRef, Source: BaseRemote, Commit: remote,
			Note: fmt.Sprintf("there is no local %s; cut from %s @ %s", trunk, remoteRef, short(remote, 10))}
	}
	c.Commit = local
	if local == remote {
		c.Note = fmt.Sprintf("cut from %s @ %s", trunk, short(local, 10))
		return c
	}
	ahead, behind, ok := countsBetween(ctx, local, remote)
	switch {
	case !ok || behind == 0:
		c.Note = fmt.Sprintf("cut from %s @ %s", trunk, short(local, 10))
	case ahead == 0:
		return baseChoice{Ref: remoteRef, Source: BaseRemote, Commit: remote, behind: true, local: local,
			Note: fmt.Sprintf("local %s is %d behind %s; cut from %s @ %s", trunk, behind, remoteRef, remoteRef, short(remote, 10))}
	default:
		c.Note = fmt.Sprintf("warning: local %s is %d ahead and %d behind %s; cut from %s @ %s",
			trunk, ahead, behind, remoteRef, trunk, short(local, 10))
	}
	return c
}

// countsBetween is how many commits local has that remote lacks, and the
// other way round.
func countsBetween(ctx *kit.Context, local, remote string) (ahead, behind int, ok bool) {
	out, err := git.Run(ctx.Repo.MainRoot, "rev-list", "--left-right", "--count", local+"..."+remote)
	f := strings.Fields(out)
	if err != nil || len(f) != 2 {
		return 0, 0, false
	}
	a, aerr := strconv.Atoi(f[0])
	b, berr := strconv.Atoi(f[1])
	return a, b, aerr == nil && berr == nil
}

// cutFrom is what git is given: the chosen commit when there is one.
func (c baseChoice) cutFrom() string {
	if c.Commit != "" {
		return c.Commit
	}
	return c.Ref
}

// bringTrunkUp moves local trunk to origin's when it is behind and safe to
// move, as wt sync does: the worktrees are read again immediately before the
// write, and anything using trunk (a checkout, a bisect, a stopped rebase
// that will move it) leaves it where it is; the write is update-ref with the
// commit that was compared as the old value, so a trunk that moved since is
// not overwritten. It says what it did, or why not, in one line.
func bringTrunkUp(ctx *kit.Context, c baseChoice, w io.Writer) {
	if !c.behind || c.local == "" {
		return
	}
	trunk := ctx.Config.MainBranch
	users, err := ctx.Repo.BranchUsers()
	if err != nil {
		fmt.Fprintf(w, "local %s left where it is: the worktrees could not be listed (%s)\n", trunk, git.Reason(err))
		return
	}
	if use, ok := users[trunk]; ok {
		if use.By != "" {
			fmt.Fprintf(w, "local %s is held by the %s in %s and left where it is\n", trunk, use.By, use.Path)
		} else {
			fmt.Fprintf(w, "local %s is checked out at %s and left where it is\n", trunk, use.Path)
		}
		return
	}
	if _, err := git.Run(ctx.Repo.MainRoot, "update-ref", "-m", "wt new: fast-forward to "+c.Ref,
		"refs/heads/"+trunk, c.Commit, c.local); err != nil {
		fmt.Fprintf(w, "local %s left where it is: it could not be fast-forwarded (%s)\n", trunk, git.Reason(err))
		return
	}
	fmt.Fprintf(w, "fast-forwarded local %s to %s\n", trunk, c.Ref)
}
