package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// stackSummaryRun is a --stack run of work with opts, summarized: its row's
// stack, whatever the run's own error, and what it printed.
func stackSummaryRun(t *testing.T, ctxRun func(opts synccmd.RunOptions, human *bytes.Buffer) error, work string, opts synccmd.RunOptions) (*syncrun.Stack, string) {
	t.Helper()
	p, human := summaryParticipant(t, ctxRun, work, opts)
	return p.Stack, human
}

func summaryParticipant(t *testing.T, ctxRun func(opts synccmd.RunOptions, human *bytes.Buffer) error, work string, opts synccmd.RunOptions) (*syncrun.SyncParticipant, string) {
	t.Helper()
	var out, human bytes.Buffer
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync rebase")
	opts.Journal.Summarize()
	err := ctxRun(opts, &human)
	t.Logf("run: %v\n%s", err, human.String())
	p := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, out.Bytes()), work)
	if p.Stack == nil {
		t.Fatalf("no stack: %+v", p)
	}
	return p, human.String()
}

func (m *mappedLine) summaryRun(t *testing.T, push syncrun.PushMode, autosquash bool) (*syncrun.Stack, string) {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Push = push
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}, Autosquash: autosquash}
	return stackSummaryRun(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(m.ctx, []string{mappedTop}, opts, human)
	}, mappedTop, opts)
}

// onlyTheLineMoves puts a commit and its fixup above the top layer, so that
// a fold moves the line's own branch and nothing else.
func (m *mappedLine) onlyTheLineMoves(t *testing.T) {
	t.Helper()
	x := testkit.GitOut(t, m.ctx.Repo.MainRoot, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+mappedTop)
	testkit.WriteFile(t, x, "top.txt", "top\n")
	testkit.GitIn(t, x, "add", "-A")
	testkit.GitIn(t, x, "commit", "-q", "--no-verify", "-m", "feat: top")
	fixup(t, x, "top.txt", "feat: top")
}

// The line's own branch has no remote branch, so only --push-line publishes
// it, and the result never offers a command that does: with layers also left
// the command is the plain one, and with the line alone left there is none,
// and push says why.
func TestPushLaterCommandNeverPublishesTheLinesOwnBranch(t *testing.T) {
	t.Parallel()
	skipShort(t)
	plain := []string{"wt", "sync", "push", mappedTop, "--stack"}
	lineAlone := func(t *testing.T, m *mappedLine, s *syncrun.Stack, human string) {
		t.Helper()
		rec, ok, err := wtsync.ReadPushRecord(m.ctx.Repo.MainRoot, mappedTop)
		if err != nil || !ok || strings.Join(rec.Branches(), ",") != mappedTop {
			t.Fatalf("the record is not the line alone: %v %v %+v\n%s", ok, err, rec, human)
		}
		if s.PushLaterCommand != nil {
			t.Errorf("pushLaterCommand %v, want none\n%s", s.PushLaterCommand, human)
		}
		if s.Push == nil || s.Push.LineLeftOut == nil || !strings.Contains(*s.Push.LineLeftOut, "--push-line") {
			t.Errorf("push does not say the line is left out: %+v", s.Push)
		}
	}
	t.Run("layers and the line left", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		s, human := m.summaryRun(t, syncrun.PushNever, false)
		if !slices.Equal(s.PushLaterCommand, plain) {
			t.Errorf("pushLaterCommand %v, want %v\n%s", s.PushLaterCommand, plain, human)
		}
	})
	t.Run("pushed but for the line", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		s, human := m.summaryRun(t, syncrun.PushAlways, false)
		lineAlone(t, m, s, human)
	})
	t.Run("only the line moved", func(t *testing.T) {
		t.Parallel()
		m := newMappedLine(t)
		m.onlyTheLineMoves(t)
		s, human := m.summaryRun(t, syncrun.PushNever, true)
		lineAlone(t, m, s, human)
		if got := pushLines(human); len(got) != 0 || !strings.Contains(human, "tmp/x kept for wt sync push tmp/x --stack --push-line") {
			t.Errorf("push lines %q, want none and the line named as kept\n%s", got, human)
		}
	})
}

// A record somebody else took or replaced before the run settled its push is
// not one to name, and neither are the layers' own commands, which push what
// the branches hold now: the row says where to look.
func TestARecordTakenOrReplacedLeavesNoPushToNameOnlyWhereToLook(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, take := range map[string]func(main string) error{
		"taken": func(main string) error { return wtsync.ForgetPushRecord(main, gittest.StackTmp) },
		"replaced": func(main string) error {
			rec, _, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
			if err != nil {
				return err
			}
			return wtsync.WritePushRecord(main, rec)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPushedLine(t)
			opts := synccmdtest.NoAgents()
			opts.Push = syncrun.PushNever
			opts.Stack = &synccmd.StackOptions{BeforePush: func() {
				if err := take(p.ctx.Repo.MainRoot); err != nil {
					t.Error(err)
				}
			}}
			row, human := summaryParticipant(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
				return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
			}, gittest.StackTmp, opts)
			noCommands(t, row, human)
			says(t, row.Recovery, "push record changed", "wt sync push "+gittest.StackTmp+" --stack --dry-run")
		})
	}
}

// A partial push after which the record cannot be read leaves nothing to
// name either.
func TestARecordThatCannotBeCheckedLeavesNoPushToName(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	backup := p.addRemote(t, "backup")
	testkit.GitIn(t, main, "config", "branch.s1.pushRemote", "backup")
	record := filepath.Join(main, ".git", "wt-sync-push", "tmp%2Fstack.json")
	gittest.WriteExecutable(t, filepath.Join(p.bare, "hooks", "pre-receive"), "#!/bin/sh\necho junk > '"+record+"'\n")
	gittest.WriteExecutable(t, filepath.Join(backup, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushAlways
	opts.Stack = &synccmd.StackOptions{}
	row, human := summaryParticipant(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
	}, gittest.StackTmp, opts)
	if row.Stack.Counts.Pushed == 0 || !strings.Contains(human, "could not be updated") {
		t.Fatalf("not a partial push over an unreadable record: %+v\n%s", row.Stack.Counts, human)
	}
	if row.Stack.PushLaterCommand != nil {
		t.Errorf("pushLaterCommand %v\n%s", row.Stack.PushLaterCommand, human)
	}
	says(t, row.Recovery, "could not be checked", "--stack --dry-run")
}

// The line alone left, and its record gone: nothing is said to be left out of
// a push there is no record for.
func TestAForgottenRecordOfTheLineAloneSaysNothingIsLeftOut(t *testing.T) {
	t.Parallel()
	skipShort(t)
	m := newMappedLine(t)
	m.onlyTheLineMoves(t)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}, Autosquash: true, BeforePush: func() {
		if err := wtsync.ForgetPushRecord(m.ctx.Repo.MainRoot, mappedTop); err != nil {
			t.Error(err)
		}
	}}
	row, human := summaryParticipant(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(m.ctx, []string{mappedTop}, opts, human)
	}, mappedTop, opts)
	if s := row.Stack; s.PushLaterCommand != nil || s.Push != nil && s.Push.LineLeftOut != nil {
		t.Errorf("later %v push %+v\n%s", s.PushLaterCommand, s.Push, human)
	}
	says(t, row.Recovery, "push record changed")
}

// A line whose own push was tried and failed was not left out.
func TestAFailedPushOfTheLinesOwnBranchIsNotCalledLeftOut(t *testing.T) {
	t.Parallel()
	skipShort(t)
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	backup := filepath.Join(t.TempDir(), "backup.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", backup)
	gittest.WriteExecutable(t, filepath.Join(backup, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	testkit.GitIn(t, main, "remote", "add", "backup", backup)
	testkit.GitIn(t, main, "config", "branch."+mappedTop+".pushRemote", "backup")
	opts := synccmdtest.NoAgents()
	opts.Push, opts.PushLine = syncrun.PushAlways, true
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	s, human := stackSummaryRun(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(m.ctx, []string{mappedTop}, opts, human)
	}, mappedTop, opts)
	if s.Push == nil || !slices.ContainsFunc(s.Push.Branches, func(b syncrun.StackPushed) bool {
		return b.Branch == mappedTop && b.Outcome != syncrun.PushedOK
	}) {
		t.Fatalf("the line's push did not fail: %+v\n%s", s.Push, human)
	}
	if s.Push.LineLeftOut != nil {
		t.Errorf("lineLeftOut %q for a branch that is in branches", *s.Push.LineLeftOut)
	}
}

// A signal between the record's saving and its settling ends the run with
// nothing verified: no command, and the row says where to look.
func TestARunInterruptedBeforeItSettlesItsPushSaysWhereToLook(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("interrupts a real run with a signal")
	}
	if os.Getenv(testkit.InterruptChild) == "" {
		out, status := testkit.InterruptedChild(t)
		at := strings.Index(out, `{"schema":`)
		if status != kit.InterruptStatus || at < 0 {
			t.Fatalf("status %d, no result object:\n%s", status, out)
		}
		line, _, _ := strings.Cut(out[at:], "\n")
		row := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, []byte(line)), gittest.StackTmp)
		if row.Stack == nil || row.Stack.PushLaterCommand != nil {
			t.Fatalf("stack %+v", row.Stack)
		}
		says(t, row.Recovery, "before it settled its push", "--stack --dry-run")
		return
	}
	p := newPushedLine(t)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Journal = syncrun.NewSyncRunJournal(os.Stdout, "sync rebase")
	opts.Journal.Summarize()
	opts.Stack = &synccmd.StackOptions{BeforePush: func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
		time.Sleep(time.Minute)
	}}
	err := synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, os.Stdout)
	t.Fatalf("the run returned (%v) instead of being interrupted", err)
}

// noCommands holds a row to naming no push, its layers' included.
func noCommands(t *testing.T, row *syncrun.SyncParticipant, human string) {
	t.Helper()
	if row.Stack.PushLaterCommand != nil {
		t.Errorf("pushLaterCommand %v\n%s", row.Stack.PushLaterCommand, human)
	}
	for _, l := range row.Stack.Layers {
		if l.Result != nil && *l.Result == wtsync.MoveMoved && len(l.PushCommand) > 0 {
			t.Errorf("the summary keeps %s and its command %v", l.Branch, l.PushCommand)
		}
	}
}

func says(t *testing.T, text *string, want ...string) {
	t.Helper()
	for _, w := range want {
		if text == nil || !strings.Contains(*text, w) {
			t.Errorf("recovery %q does not say %q", jsonout.Deref(text), w)
		}
	}
}

// A record that cannot be saved leaves no command to name, so the summary
// keeps what pushes each moved layer: its own row's.
func TestASummaryKeepsTheLayersPushCommandsWhenTheRecordCannotBeSaved(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	// A directory where the record's file goes.
	record := filepath.Join(p.ctx.Repo.MainRoot, ".git", "wt-sync-push", "tmp%2Fstack.json")
	if err := os.MkdirAll(record, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{}
	s, human := stackSummaryRun(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
	}, gittest.StackTmp, opts)
	if !strings.Contains(human, "could not be kept for a later push") || s.PushLaterCommand != nil {
		t.Fatalf("later %v\n%s", s.PushLaterCommand, human)
	}
	keptCommands(t, s)
}

// keptCommands holds s to a row with its push command for every moved layer.
func keptCommands(t *testing.T, s *syncrun.Stack) {
	t.Helper()
	rows := 0
	for _, l := range s.Layers {
		if l.Result != nil && *l.Result == wtsync.MoveMoved && len(l.PushCommand) > 0 {
			rows++
		}
	}
	if moved := s.Counts.LayerResults[wtsync.MoveMoved]; moved == 0 || rows != moved {
		t.Errorf("%d moved layers, %d rows with their push command", moved, rows)
	}
}

// A run whose own branches all went, in a push of the whole record that
// failed for a branch only an earlier run recorded, has nothing of its own to
// name.
func TestPushLaterCommandIsNotNamedWhenOnlyAnEarlierRunsBranchIsLeft(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	backup := p.addRemote(t, "backup")
	testkit.GitIn(t, main, "config", "branch.s1.pushRemote", "backup")
	p.runAt(t, epochA)
	gittest.WriteExecutable(t, filepath.Join(backup, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	opts := synccmdtest.At(synccmdtest.NoAgents(), epochB)
	opts.Push = syncrun.PushAlways
	opts.Stack = &synccmd.StackOptions{Layers: []string{"s4"}}
	s, human := stackSummaryRun(t, func(opts synccmd.RunOptions, human *bytes.Buffer) error {
		return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
	}, gittest.StackTmp, opts)
	rec, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if err != nil || !ok || strings.Join(rec.Branches(), ",") != "s1" {
		t.Fatalf("the record is not the earlier run's s1: %v %v %+v\n%s", ok, err, rec, human)
	}
	if s.PushLaterCommand != nil {
		t.Errorf("pushLaterCommand %v for a run that pushed all it moved\n%s", s.PushLaterCommand, human)
	}
}
