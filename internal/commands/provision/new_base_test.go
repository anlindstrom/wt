package provision

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// baseFixture is a repository on main with a bare origin, and a second clone
// of that origin to move it from. main holds a tracked file, so a checkout of
// it can be made dirty.
type baseFixture struct {
	main, origin, other string
}

func newBaseFixture(t *testing.T) *baseFixture {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.WriteFile(t, main, "tracked.txt", "one\n")
	testkit.GitIn(t, main, "add", "tracked.txt")
	testkit.GitIn(t, main, "commit", "-q", "-m", "tracked")
	f := &baseFixture{main: main}
	f.origin = gittest.WithOrigin(t, main)
	f.other = filepath.Join(t.TempDir(), "other")
	gittest.Git(t, filepath.Dir(f.other), "clone", "-q", f.origin, f.other)
	gittest.Git(t, f.other, "config", "user.name", "T")
	gittest.Git(t, f.other, "config", "user.email", "t@example.com")
	gittest.Git(t, f.other, "config", "commit.gpgsign", "false")
	return f
}

// moveOrigin puts n commits on origin's main, from the second clone. The main
// repository has not fetched them.
func (f *baseFixture) moveOrigin(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		gittest.Git(t, f.other, "commit", "-q", "--allow-empty", "-m", "origin commit")
	}
	gittest.Git(t, f.other, "push", "-q", "origin", "main")
}

// commitLocal puts n commits on the main checkout's main.
func (f *baseFixture) commitLocal(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		testkit.GitIn(t, f.main, "commit", "-q", "--allow-empty", "-m", "local commit")
	}
}

// freeTrunk moves the main checkout off main, so no worktree has it.
func (f *baseFixture) freeTrunk(t *testing.T) {
	t.Helper()
	testkit.GitIn(t, f.main, "switch", "-q", "-c", "scratch")
}

// dirty leaves a modified tracked file and an untracked one in the main
// checkout, and returns what they hold.
func (f *baseFixture) dirty(t *testing.T) map[string]string {
	t.Helper()
	testkit.WriteFile(t, f.main, "tracked.txt", "edited\n")
	testkit.WriteFile(t, f.main, "scratch.txt", "mine\n")
	return f.files(t)
}

func (f *baseFixture) files(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"tracked.txt", "scratch.txt"} {
		if b, err := os.ReadFile(filepath.Join(f.main, name)); err == nil {
			out[name] = string(b)
		}
	}
	return out
}

func (f *baseFixture) rev(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(testkit.GitOut(t, dir, "rev-parse", ref))
}

func (f *baseFixture) ctx(t *testing.T) *kit.Context {
	t.Helper()
	ctx, err := kit.Open(f.main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// run is wt new fix/x --no-setup, and what it printed.
func (f *baseFixture) run(t *testing.T, opts NewOptions) (path, out string) {
	t.Helper()
	opts.NoSetup = true
	var buf bytes.Buffer
	path, err := New(f.ctx(t), "fix/x", opts, &buf)
	if err != nil {
		t.Fatalf("New: %v\n%s", err, buf.String())
	}
	return path, buf.String()
}

// want is what a run leaves: the new branch's commit, local main's commit,
// and the main checkout's files.
type want struct{ branch, local string }

func (f *baseFixture) check(t *testing.T, path string, w want, files map[string]string) {
	t.Helper()
	if got := f.rev(t, path, "HEAD"); got != w.branch {
		t.Errorf("the new branch is at %s, want %s", got, w.branch)
	}
	if got := f.rev(t, f.main, "refs/heads/main"); got != w.local {
		t.Errorf("local main is at %s, want %s", got, w.local)
	}
	if files != nil {
		got := f.files(t)
		for name, content := range files {
			if got[name] != content {
				t.Errorf("%s in the main checkout is %q, want %q", name, got[name], content)
			}
		}
	}
}

func TestNewCutsFromOriginWhenLocalTrunkIsBehind(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.freeTrunk(t)
	f.moveOrigin(t, 3)
	remote := f.rev(t, f.other, "HEAD")
	path, out := f.run(t, NewOptions{})
	// Nothing has main checked out, so it is brought up to origin's.
	f.check(t, path, want{branch: remote, local: remote}, nil)
	for _, s := range []string{"local main is 3 behind origin/main", "cut from origin/main @ " + remote[:10]} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "fast-forwarded local main") {
		t.Errorf("output does not say local main was fast-forwarded:\n%s", out)
	}
}

func TestNewLeavesACheckedOutDirtyTrunkAloneWhenBehind(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	f.moveOrigin(t, 2)
	remote := f.rev(t, f.other, "HEAD")
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: remote, local: local}, files)
	if !strings.Contains(out, "cut from origin/main @ "+remote[:10]) {
		t.Errorf("output does not name the base:\n%s", out)
	}
	if !strings.Contains(out, "main is checked out at "+f.main) || !strings.Contains(out, "left where it is") {
		t.Errorf("output does not say why local main stayed:\n%s", out)
	}
}

func TestNewCutsFromLocalTrunkWhenEqualToOrigin(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: local, local: local}, files)
	if !strings.Contains(out, "cut from main @ "+local[:10]) {
		t.Errorf("output does not name the base:\n%s", out)
	}
}

func TestNewCutsFromLocalTrunkWhenAheadOfOrigin(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.commitLocal(t, 2)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: local, local: local}, files)
	if strings.Contains(out, "warning") {
		t.Errorf("a trunk that is only ahead is no warning:\n%s", out)
	}
}

func TestNewWarnsAndCutsFromLocalTrunkWhenDiverged(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 3)
	f.commitLocal(t, 2)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: local, local: local}, files)
	for _, s := range []string{"warning", "2 ahead", "3 behind", "origin/main", "cut from main @ " + local[:10]} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func TestNewCutsFromLocalTrunkWithNoOriginRef(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	// An origin that has no main: nothing to fetch, nothing remote to cut from.
	empty := filepath.Join(t.TempDir(), "empty.git")
	gittest.Git(t, filepath.Dir(empty), "init", "-q", "--bare", empty)
	testkit.GitIn(t, f.main, "remote", "set-url", "origin", empty)
	testkit.GitIn(t, f.main, "update-ref", "-d", "refs/remotes/origin/main")
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: local, local: local}, files)
	if !strings.Contains(out, "cut from main @ "+local[:10]) {
		t.Errorf("output does not name the base:\n%s", out)
	}
}

func TestNewWithNoOriginAtAllSaysNothingOfFetching(t *testing.T) {
	t.Parallel()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	ctx, _ := kit.Open(main, io.Discard)
	var buf bytes.Buffer
	path, err := New(ctx, "fix/x", NewOptions{NoSetup: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(testkit.GitOut(t, path, "rev-parse", "HEAD")), strings.TrimSpace(testkit.GitOut(t, main, "rev-parse", "main")); got != want {
		t.Errorf("branch at %s, want %s", got, want)
	}
	if strings.Contains(buf.String(), "fetch") {
		t.Errorf("no origin, so no word of a fetch:\n%s", buf.String())
	}
}

func TestNewGoesOnWhenTheFetchFails(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 2)
	testkit.GitIn(t, f.main, "fetch", "-q", "origin")
	remote := f.rev(t, f.main, "refs/remotes/origin/main")
	local := f.rev(t, f.main, "HEAD")
	testkit.GitIn(t, f.main, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{})
	// Origin as last fetched is still ahead of local main.
	f.check(t, path, want{branch: remote, local: local}, files)
	if n := strings.Count(out, "fetch"); n == 0 || !strings.Contains(out, "as last fetched") {
		t.Errorf("output does not say the fetch failed and what is used instead:\n%s", out)
	}
}

func TestNewNoFetchDoesNotFetch(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 2)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, _ := f.run(t, NewOptions{NoFetch: true})
	f.check(t, path, want{branch: local, local: local}, files)
	if got := f.rev(t, f.main, "refs/remotes/origin/main"); got != local {
		t.Errorf("origin/main moved to %s: --no-fetch fetched", got)
	}
}

// --base is cut from exactly what it names, as it always was: no fetch, and
// the one line it always printed.
func TestNewBaseIsUntouchedByTheFetch(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 2)
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	path, out := f.run(t, NewOptions{Base: "main"})
	f.check(t, path, want{branch: local, local: local}, files)
	if got := f.rev(t, f.main, "refs/remotes/origin/main"); got != local {
		t.Errorf("origin/main moved to %s: --base fetched", got)
	}
	if wantOut := "Creating fix_wt/x at " + path + " (from main)\n"; out != wantOut {
		t.Errorf("output %q, want %q", out, wantOut)
	}
}

func TestNewDryRunFetchesAndSaysWhereTheBaseIsFrom(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 2)
	remote := f.rev(t, f.other, "HEAD")
	local := f.rev(t, f.main, "HEAD")
	files := f.dirty(t)
	var buf bytes.Buffer
	if err := NewPlanJSON(f.ctx(t), "fix/x", NewOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	testkit.ValidateJSON(t, "new-plan", buf.Bytes())
	var p NewPlan
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.BaseSource != "remote" || p.Base == nil || *p.Base != "origin/main" || p.BaseCommit == nil || *p.BaseCommit != remote {
		t.Errorf("plan base %v @ %v from %q, want origin/main @ %s from remote", jsonStr(p.Base), jsonStr(p.BaseCommit), p.BaseSource, remote)
	}
	if got := f.rev(t, f.main, "refs/heads/main"); got != local {
		t.Errorf("a dry run moved local main to %s", got)
	}
	for name, content := range files {
		if got := f.files(t)[name]; got != content {
			t.Errorf("a dry run changed %s in the main checkout", name)
		}
	}
}

func TestNewDryRunNoFetchAndBaseSources(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 1)
	local := f.rev(t, f.main, "HEAD")
	var buf bytes.Buffer
	if err := NewPlanJSON(f.ctx(t), "fix/x", NewOptions{NoFetch: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var p NewPlan
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.BaseSource != "local" || p.BaseCommit == nil || *p.BaseCommit != local {
		t.Errorf("--no-fetch plan from %q @ %v, want local @ %s", p.BaseSource, jsonStr(p.BaseCommit), local)
	}
	if got := f.rev(t, f.main, "refs/remotes/origin/main"); got != local {
		t.Errorf("--no-fetch fetched: origin/main at %s", got)
	}
	buf.Reset()
	if err := NewPlanJSON(f.ctx(t), "fix/x", NewOptions{Base: "main"}, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.BaseSource != "base" {
		t.Errorf("--base plan from %q, want base", p.BaseSource)
	}
}

func TestNewResultNamesWhereTheBaseWasFrom(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.moveOrigin(t, 1)
	remote := f.rev(t, f.other, "HEAD")
	var out, human bytes.Buffer
	if err := NewJSON(f.ctx(t), "fix/x", NewOptions{NoSetup: true}, "", NewCreateJournal(&out, "new"), &human); err != nil {
		t.Fatalf("%v\n%s", err, human.String())
	}
	testkit.ValidateJSON(t, "new", out.Bytes())
	var r NewResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.BaseSource != "remote" || r.Base == nil || *r.Base != "origin/main" || r.Commit == nil || *r.Commit != remote {
		t.Errorf("result base %v from %q at %v, want origin/main from remote at %s", jsonStr(r.Base), r.BaseSource, jsonStr(r.Commit), remote)
	}
}

func jsonStr(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

// A tag called origin/main wins over the remote-tracking ref when git reads
// the shorthand; the commit chosen is the one cut from, and the plan's.
func TestNewCutsFromTheChosenCommitNotAShadowingTag(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.freeTrunk(t)
	local := f.rev(t, f.main, "refs/heads/main")
	testkit.GitIn(t, f.main, "tag", "origin/main", local)
	f.moveOrigin(t, 2)
	remote := f.rev(t, f.other, "HEAD")
	var buf bytes.Buffer
	if err := NewPlanJSON(f.ctx(t), "fix/x", NewOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	var p NewPlan
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.BaseCommit == nil || *p.BaseCommit != remote {
		t.Errorf("the plan's base commit is %v, want %s", jsonStr(p.BaseCommit), remote)
	}
	path, _ := f.run(t, NewOptions{})
	f.check(t, path, want{branch: remote, local: remote}, nil)
}

// A bisect started from main holds it as a checkout does: it is not moved.
func TestNewLeavesTrunkAloneWhileABisectHoldsIt(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	// A bisect needs a range with a commit in the middle.
	f.commitLocal(t, 3)
	testkit.GitIn(t, f.main, "push", "-q", "origin", "main")
	gittest.Git(t, f.other, "pull", "-q", "--ff-only")
	f.freeTrunk(t)
	other := filepath.Join(t.TempDir(), "bisecting")
	testkit.GitIn(t, f.main, "worktree", "add", "-q", other, "main")
	testkit.GitIn(t, other, "bisect", "start", "HEAD", "HEAD~2")
	local := f.rev(t, f.main, "refs/heads/main")
	f.moveOrigin(t, 2)
	remote := f.rev(t, f.other, "HEAD")
	path, out := f.run(t, NewOptions{})
	f.check(t, path, want{branch: remote, local: local}, nil)
	if !strings.Contains(out, "held by the bisect") {
		t.Errorf("output does not say a bisect holds main:\n%s", out)
	}
}

// The fast-forward is guarded by the commit that was compared: a trunk that
// moved since is not overwritten.
func TestBringTrunkUpHoldsToTheCommitCompared(t *testing.T) {
	t.Parallel()
	f := newBaseFixture(t)
	f.freeTrunk(t)
	f.moveOrigin(t, 2)
	testkit.GitIn(t, f.main, "fetch", "-q", "origin")
	ctx := f.ctx(t)
	c := chooseTrunkBase(ctx)
	if !c.behind {
		t.Fatalf("setup: %+v", c)
	}
	// Local trunk moves on after the choice was made.
	testkit.GitIn(t, f.main, "update-ref", "refs/heads/main", f.rev(t, f.other, "HEAD~1"))
	moved := f.rev(t, f.main, "refs/heads/main")
	var buf bytes.Buffer
	bringTrunkUp(ctx, c, &buf)
	if got := f.rev(t, f.main, "refs/heads/main"); got != moved {
		t.Errorf("local main was moved from %s to %s", moved, got)
	}
	if !strings.Contains(buf.String(), "could not be fast-forwarded") {
		t.Errorf("output does not say it was left:\n%s", buf.String())
	}
}
