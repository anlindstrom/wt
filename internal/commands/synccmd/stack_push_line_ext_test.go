package synccmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// pushLines is the push: lines a run ends with, in order: those not indented
// under a layer.
func pushLines(human string) []string {
	var out []string
	for _, l := range strings.Split(human, "\n") {
		if strings.HasPrefix(l, "push: ") {
			out = append(out, l)
		}
	}
	return out
}

// noPush is a --stack --no-push run of the mapped line, with its layers
// named by a pattern.
func (m *mappedLine) noPush(t *testing.T, edit func(o *synccmd.RunOptions)) string {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	if edit != nil {
		edit(&opts)
	}
	_, human, err := synccmdtest.StackRun(t, m.ctx, mappedTop, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	return human
}

// A --stack --no-push run ends with the push of its record. The line's own
// branch gets a git push of its own only where one creates nothing nobody
// asked for.
func TestNoPushStackRunEndsWithTheStackPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const later = "push: wt sync push tmp/x --stack"
	raw := func(line string) bool {
		return strings.HasPrefix(line, "push: git -C ") && strings.Contains(line, "refs/heads/tmp/x:")
	}
	t.Run("a local line", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		human := m.noPush(t, nil)
		if got := pushLines(human); len(got) != 1 || got[0] != later || !strings.HasSuffix(human, later+"\n") {
			t.Errorf("push lines %q, want only %q, last\n%s", got, later, human)
		}
	})
	t.Run("a remote branch since deleted", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		main := m.ctx.Repo.MainRoot
		testkit.GitIn(t, main, "push", "-q", "origin", "refs/heads/tmp/x:refs/heads/tmp/x")
		// Gone on the remote; the remote-tracking ref stays, and the run's
		// fetch learns it is not there.
		testkit.GitIn(t, m.bare, "update-ref", "-d", "refs/heads/tmp/x")
		human := m.noPush(t, func(o *synccmd.RunOptions) { o.NoFetch = false })
		// The line has no safe push, so no record is kept for a later one
		// either.
		if got := pushLines(human); len(got) != 0 || !strings.Contains(human, "tmp/x not pushed, and no push of it is printed") {
			t.Errorf("push lines %q, want none and the line said to be left\n%s", got, human)
		}
	})
	for name, change := range map[string][]string{
		"seen on another remote":  {"config", "branch.tmp/x.pushRemote", "backup"},
		"seen at another address": {"remote", "set-url", "origin", "elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newMappedLine(t)
			main := m.ctx.Repo.MainRoot
			testkit.GitIn(t, main, "push", "-q", "origin", "refs/heads/tmp/x:refs/heads/tmp/x")
			testkit.GitIn(t, main, "remote", "add", "backup", "backup-repo")
			human := m.noPush(t, func(o *synccmd.RunOptions) {
				// Where the line goes changes after the run read it.
				o.Stack.BeforePush = func() { testkit.GitIn(t, main, change...) }
			})
			if got := pushLines(human); slices.ContainsFunc(got, raw) {
				t.Errorf("push lines %q, want no git push of the line\n%s", got, human)
			}
		})
	}
	for name, c := range map[string]struct {
		setup func(t *testing.T, m *mappedLine)
		edit  func(o *synccmd.RunOptions)
	}{
		"--push-line": {edit: func(o *synccmd.RunOptions) { o.PushLine = true }},
		"a recorded destination": {setup: func(t *testing.T, m *mappedLine) {
			testkit.GitIn(t, m.ctx.Repo.MainRoot, "config", "branch.tmp/x.wtPushTo", "origin feat_wt/x")
		}},
		"a remote branch": {setup: func(t *testing.T, m *mappedLine) {
			testkit.GitIn(t, m.ctx.Repo.MainRoot, "push", "-q", "origin", "refs/heads/tmp/x:refs/heads/tmp/x")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newMappedLine(t)
			if c.setup != nil {
				c.setup(t, m)
			}
			human := m.noPush(t, c.edit)
			got := pushLines(human)
			advice := strings.Index(human, "↪ ")
			if len(got) != 2 || !raw(got[0]) || got[1] != later || advice < 0 || advice > strings.Index(human, got[0]) {
				t.Errorf("push lines %q, want the advice, the line's git push, then %q\n%s", got, later, human)
			}
		})
	}
	t.Run("a record that cannot be saved", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		common := testkit.GitOut(t, m.ctx.Repo.MainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err := os.WriteFile(filepath.Join(common, "wt-sync-push"), []byte("in the way\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		human := m.noPush(t, nil)
		if got := pushLines(human); len(got) != 0 || !strings.Contains(human, "tmp/x not pushed, and no push of it is printed") {
			t.Errorf("push lines %q, want none and the line said to be left\n%s", got, human)
		}
	})
}

// Runs that recorded two destinations for a layer leave a record no push
// takes: the run ends pointing at the dry run, not at a push.
func TestNoPushStackRunWithADestinationConflictPointsAtTheDryRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	p.runAt(t, epochA)
	testkit.GitIn(t, main, "push", "-q", "origin", "refs/heads/s4:refs/heads/s4-other")
	testkit.GitIn(t, main, "config", "branch.s4.wtPushTo", "origin s4-other")
	commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	opts := synccmdtest.At(synccmdtest.NoAgents(), epochB)
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: []string{"s4"}}
	res, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	const look = "wt sync push tmp/stack --stack --dry-run"
	lines := strings.Split(strings.TrimSpace(human), "\n")
	if !strings.Contains(lines[len(lines)-1], look) || strings.Contains(human, "pushes them later") || strings.Contains(human, "push: wt sync push") {
		t.Errorf("want %q last and no push of the record offered\n%s", look, human)
	}
	// --json names the record's push as it does for any record kept.
	want := []string{"wt", "sync", "push", gittest.StackTmp, "--stack"}
	if s := synccmdtest.Participant(t, res, gittest.StackTmp).Stack; s == nil || !slices.Equal(s.PushLaterCommand, want) {
		t.Errorf("pushLaterCommand, want %v: %+v\n%s", want, s, human)
	}
}

// A work name the shell would read is quoted in the commands printed.
func TestNoPushStackRunQuotesTheWorkName(t *testing.T) {
	t.Parallel()
	skipShort(t)
	m := newMappedLineNamed(t, "tmp/x$y")
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	_, human, err := synccmdtest.StackRun(t, m.ctx, "tmp/x$y", opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if want := "push: wt sync push 'tmp/x$y' --stack"; !slices.Equal(pushLines(human), []string{want}) ||
		strings.Contains(human, "wt sync push tmp/x$y") {
		t.Errorf("want %q and the name quoted throughout\n%s", want, human)
	}
}

// A resume of a --no-push run ends the same way: with the line's git push
// before it where the line is pushed somewhere, and alone where layers it
// held end the resume early.
func TestNoPushResumeEndsWithTheStackPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const later = "push: wt sync push tmp/stack --stack"
	t.Run("layers held", func(t *testing.T) {
		t.Parallel()
		p := newTwoStopLine(t)
		_, human, _ := p.noPushRunResumed(t, yesFlag())
		if got := pushLines(human); len(got) != 1 || got[0] != later {
			t.Errorf("push lines %q\n%s", got, human)
		}
	})
	t.Run("a recorded destination", func(t *testing.T) {
		t.Parallel()
		p := newTwoStopLine(t)
		main := p.ctx.Repo.MainRoot
		testkit.GitIn(t, main, "worktree", "remove", "--force", p.s.Paths["s3"])
		testkit.GitIn(t, main, "worktree", "remove", "--force", p.s.Paths["s5"])
		testkit.GitIn(t, main, "config", "branch.tmp/stack.wtPushTo", "origin feat/line")
		_, human, err := p.noPushRunResumed(t, yesFlag())
		got := pushLines(human)
		if err != nil || len(got) != 2 || !strings.HasPrefix(got[0], "push: git -C ") || got[1] != later ||
			strings.Index(human, "↪ ") > strings.Index(human, got[0]) {
			t.Errorf("err %v, push lines %q\n%s", err, got, human)
		}
	})
}
