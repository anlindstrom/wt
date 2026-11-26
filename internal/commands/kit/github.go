package kit

import (
	"fmt"
	"time"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/github"
)

// How these commands reach the GitHub CLI. Variables so a test binary can
// put one state in front of all its tests without a gh on the machine
// running it; FindGitHub is the one a TestMain sets. One test's own gh goes
// on its Context instead.
var (
	FindGitHub   = github.Find
	GitHubRemote = github.RemoteOf
	// LookupDeadline bounds the branch lookup behind `wt list` and `wt status`.
	LookupDeadline = github.ListDeadline
)

// GitHubFake is the GitHub one Context sees in place of FindGitHub,
// GitHubRemote and LookupDeadline. Tests set it, so that tests running side
// by side each have their own gh.
type GitHubFake struct {
	CLI       github.CLI
	Found     bool
	Remote    github.Remote
	HasRemote bool
	Deadline  time.Duration
}

func (c *Context) findGitHub() (github.CLI, bool) {
	if c.GitHubFake != nil {
		return c.GitHubFake.CLI, c.GitHubFake.Found
	}
	return FindGitHub()
}

func (c *Context) gitHubRemote() (github.Remote, bool) {
	if c.GitHubFake != nil {
		return c.GitHubFake.Remote, c.GitHubFake.HasRemote
	}
	return GitHubRemote(c.Repo.MainRoot)
}

// GitHubDeadline bounds the branch lookup behind `wt list` and `wt status`
// in this repository: LookupDeadline, or the fake's.
func (c *Context) GitHubDeadline() time.Duration {
	if c.GitHubFake != nil {
		return c.GitHubFake.Deadline
	}
	return LookupDeadline
}

// GitHub is a usable GitHub CLI and the repository it answers for.
type GitHub struct {
	CLI    github.CLI
	Remote github.Remote
}

// OpenGitHub runs the gates the integration stands on and returns, for the
// first that failed, one line saying how to fix it: the person's own setting,
// gh on the PATH, then a GitHub remote. The login is not among them — GitHub.Fail
// turns the real call's failure into the line this would have printed, and only
// `wt doctor` pays for a `gh auth status` of its own.
func OpenGitHub(ctx *Context) (GitHub, error) {
	if !ctx.UserConfig().GitHub {
		return GitHub{}, fmt.Errorf("GitHub is off in your wt config; turn it on with `wt config set %s true`",
			config.UserKeyGitHub)
	}
	cli, ok := ctx.findGitHub()
	if !ok {
		return GitHub{}, fmt.Errorf("gh is not on your PATH; install the GitHub CLI (https://cli.github.com), " +
			"or turn this off with `wt config set " + config.UserKeyGitHub + " false`")
	}
	remote, ok := ctx.gitHubRemote()
	if !ok {
		return GitHub{}, fmt.Errorf("%s has no GitHub remote; `wt pr` works on repositories hosted on GitHub",
			ctx.Repo.Name)
	}
	return GitHub{CLI: cli, Remote: remote}, nil
}

// Fail is what a `wt pr` command reports when a call to gh failed. A gh with
// no login for this host gets the sentence saying what to do about it rather
// than gh's own wording.
func (g GitHub) Fail(err error) error {
	if github.NotLoggedIn(err) {
		return fmt.Errorf("gh is not logged in to %s; run `gh auth login --hostname %s`",
			g.Remote.Host, g.Remote.Host)
	}
	return err
}

// DoctorGitHub reports whether `wt pr` would reach GitHub here, and where it
// would stop if it would not. Nothing in this section is counted as a
// problem: a machine without gh is an ordinary machine.
func DoctorGitHub(ctx *Context, f *Findings) {
	f.Printf("GitHub:\n")
	if !ctx.UserConfig().GitHub {
		f.Unused("off in your wt config; turn it on with `wt config set %s true`",
			config.UserKeyGitHub)
		return
	}
	cli, ok := ctx.findGitHub()
	if !ok {
		f.Note("github.cli", CheckWarn, "no gh on the PATH; `wt pr` is inactive and `wt list` shows no pull requests")
		return
	}
	version := cli.Version()
	status := CheckOK
	if version == "" {
		version, status = "version unknown", CheckUnknown
	}
	f.OK("github.cli", "%s (%s)", cli.Exe, version).As(status)

	remote, ok := ctx.gitHubRemote()
	if !ok {
		f.Unused("no GitHub remote for %s; `wt pr` is inactive here", ctx.Repo.Name)
		return
	}
	// A login gh answered about is one thing; a `gh auth status` that timed
	// out, or failed for its own reasons, is another, and reporting the
	// second as the first sends you off to log in again for nothing.
	switch err := cli.Authenticated(remote.Host); {
	case err == nil:
		f.OK("github.login", "%s on %s — gh is logged in", remote.Slug, remote.Host)
	case github.NotLoggedIn(err):
		f.Note("github.login", CheckWarn, "%s is on %s, which gh is not logged in to; run `gh auth login --hostname %s`",
			remote.Slug, remote.Host, remote.Host).WithFix("gh auth login --hostname " + remote.Host)
		return
	default:
		f.Note("github.login", CheckUnknown, "could not check gh's login for %s: %v", remote.Host, err)
		return
	}
	if err := prCacheWritable(ctx); err != nil {
		f.Note("github.cache", CheckWarn, "the pull requests cannot be cached, so every listing asks GitHub again: %v", err)
		return
	}
	f.OK("github.cache", "pull requests cached in %s", PRCachePath(ctx))
}
