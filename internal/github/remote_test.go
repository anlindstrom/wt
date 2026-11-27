package github

import (
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

func TestParseURL(t *testing.T) {
	t.Parallel()
	for url, want := range map[string]struct{ host, slug string }{
		"git@github.com:acme/tooling.git":         {"github.com", "acme/tooling"},
		"git@github.com:acme/tooling":             {"github.com", "acme/tooling"},
		"https://github.com/acme/tooling.git":     {"github.com", "acme/tooling"},
		"https://someone@github.com/acme/tooling": {"github.com", "acme/tooling"},
		"ssh://git@github.com/acme/tooling.git":   {"github.com", "acme/tooling"},
		"ssh://git@github.com:2222/acme/tooling":  {"github.com", "acme/tooling"},
		"git@github.acme.example:Team/thing.git":  {"github.acme.example", "Team/thing"},
		"https://GitHub.com/acme/tooling":         {"github.com", "acme/tooling"},
		"https://gitlab.com/acme/tooling":         {"gitlab.com", "acme/tooling"},
		"/srv/git/mirror.git":                     {"", ""},
		"https://github.com/acme":                 {"", ""},
		"git@github.com:":                         {"", ""},
	} {
		t.Run(url, func(t *testing.T) {
			host, slug, ok := parseURL(url)
			if (want.host != "") != ok {
				t.Fatalf("parseURL(%q) ok = %v", url, ok)
			}
			if ok && (host != want.host || slug != want.slug) {
				t.Errorf("parseURL(%q) = %q, %q; want %q, %q", url, host, slug, want.host, want.slug)
			}
		})
	}
}

// Only a host wt reads as GitHub's counts; everything else means "this
// repository is not on GitHub", which is an ordinary state.
func TestIsGitHub(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"github.com":          true,
		"github.acme.example": true,
		"ghe.github.corp":     true,
		"gitlab.com":          false,
		"bitbucket.org":       false,
		"":                    false,
	} {
		if got := isGitHub(host); got != want {
			t.Errorf("isGitHub(%q) = %v, want %v", host, got, want)
		}
	}
}

// On a fork, origin is your copy and upstream is where the pull requests
// are. gh resolves upstream as the base repository, and wt has to ask about
// the same one: it acts on gh's answers, down to fetching a merged pull
// request's head commit from this remote.
func TestRemoteOfResolvesTheBaseRepositoryTheWayGhDoes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", ".")
	gittest.Git(t, dir, "remote", "add", "upstream", "git@github.com:sharkdp/fd.git")
	gittest.Git(t, dir, "remote", "add", "origin", "git@github.com:anders/fd.git")
	gittest.Git(t, dir, "remote", "add", "mirror", "/srv/git/fd.git")

	r, ok := RemoteOf(dir)
	if !ok {
		t.Fatal("RemoteOf found nothing")
	}
	if r.Name != "upstream" || r.Slug != "sharkdp/fd" || r.Host != "github.com" {
		t.Errorf("RemoteOf() = %+v, want upstream", r)
	}
}

// `gh repo set-default` writes the answer into git config, and it overrides
// the ordering: the person said which repository this checkout is about.
func TestRemoteOfHonoursGhRepoSetDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", ".")
	gittest.Git(t, dir, "remote", "add", "upstream", "git@github.com:sharkdp/fd.git")
	gittest.Git(t, dir, "remote", "add", "origin", "git@github.com:anders/fd.git")
	gittest.Git(t, dir, "config", "remote.origin.gh-resolved", "base")
	if r, _ := RemoteOf(dir); r.Name != "origin" || r.Slug != "anders/fd" {
		t.Errorf("RemoteOf() = %+v, want origin", r)
	}

	// The other spelling: a repository that need not be a remote here.
	gittest.Git(t, dir, "config", "--unset", "remote.origin.gh-resolved")
	gittest.Git(t, dir, "config", "remote.origin.gh-resolved", "acme/server")
	if r, _ := RemoteOf(dir); r.Slug != "acme/server" || r.Host != "github.com" {
		t.Errorf("RemoteOf() = %+v, want acme/server", r)
	}
}

func TestRemoteOfFallsBackAndGivesUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", ".")
	gittest.Git(t, dir, "remote", "add", "fork", "https://github.com/anders/fd")
	if r, ok := RemoteOf(dir); !ok || r.Name != "fork" {
		t.Errorf("RemoteOf() = %+v, %v; want the only GitHub remote", r, ok)
	}

	bare := t.TempDir()
	gittest.Git(t, bare, "init", "-q", ".")
	gittest.Git(t, bare, "remote", "add", "origin", "https://gitlab.com/t/x.git")
	if r, ok := RemoteOf(bare); ok {
		t.Errorf("RemoteOf() = %+v on a repository that is not on GitHub", r)
	}
	if _, ok := RemoteOf(t.TempDir()); ok {
		t.Error("RemoteOf found a remote outside a repository")
	}
}
