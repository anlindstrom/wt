package synccmd_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// phaseWriter keeps what is written to it, and runs then once, at the first
// write that has phase.
type phaseWriter struct {
	bytes.Buffer
	phase string
	then  func()
}

func onPhase(phase string, then func()) *phaseWriter { return &phaseWriter{phase: phase, then: then} }

func (w *phaseWriter) Write(p []byte) (int, error) {
	if w.then != nil && bytes.Contains(p, []byte(w.phase)) {
		w.then()
		w.then = nil
	}
	return w.Buffer.Write(p)
}

// divergedLine is a line on tmp/low that is behind trunk: feat: x, feat: a, a
// commit subject second with the layer low1 at it, extra more commits, and a
// fixup of feat: a. Trunk has changed trunkFile since. A script on trunk claims
// x.txt, which the line's first commit changes: with x.txt as trunkFile that
// commit stops the replay.
func divergedLine(t *testing.T, trunkFile, second string, extra int) (ctx *kit.Context, tmp string) {
	t.Helper()
	ctx, _ = synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write("x.txt", "base\n")
		write(".wt-sync.yaml", "conflicts:\n  - paths: [x.txt]\n    strategy: script\n    run: tools/own.sh\neach_commit:\n  - run: 'true'\n")
		write("tools/own.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(main, "tools/own.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		write("x.txt", "line\n")
		commit("feat: x")
		write("a.txt", "a\n")
		commit("feat: a")
		write("b.txt", "b\n")
		commit(second)
		gittest.Git(t, main, "branch", "low1")
		for i := range extra {
			write(fmt.Sprintf("n%d.txt", i), "n\n")
			commit(fmt.Sprintf("feat: n%d", i))
		}
		write("a.txt", "a fixed\n")
		commit("fixup! feat: a")
		gittest.Git(t, main, "checkout", "-q", "main")
		write(trunkFile, "trunk\n")
		commit("feat: trunk's own")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_low")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, "tmp/low")
	})
	return ctx, tmp
}

// replayStarted reports whether trace, a GIT_TRACE file, has the commit
// selection a simulated replay begins with.
func replayStarted(t *testing.T, trace string) bool {
	t.Helper()
	got, err := os.ReadFile(trace)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return bytes.Contains(got, []byte("--reverse --topo-order --right-only"))
}

// noticeWriter keeps each write that names the fixup, and notes whether it
// came ahead of the replay and of the reading of the stack.
type noticeWriter struct {
	t       *testing.T
	trace   string
	read    bool
	notices []string
	early   []bool
}

func (w *noticeWriter) Write(p []byte) (int, error) {
	w.read = w.read || bytes.Contains(p, []byte("reading side branches"))
	if bytes.Contains(p, []byte("fixup! feat: a")) {
		w.notices = append(w.notices, string(p))
		w.early = append(w.early, !w.read && !replayStarted(w.t, w.trace))
	}
	return len(p), nil
}

// A fixup the run would refuse is named on the progress writer before the
// look's slow phases and any replay, in the words the run refuses with; where
// the run stops for something else first, it is not named. A fold in place
// replays nothing onto trunk; a fold with --onto replays onto its target.
//
// Not parallel: GIT_TRACE is set for the whole process.
func TestFoldLookNamesAnAmbiguousFixupAhead(t *testing.T) {
	skipShort(t)
	for _, c := range []struct {
		name, second, onto string
		prepare            func(t *testing.T, main, tmp string)
		notice             bool
		// refusal is what the run says, "" where the case does not run it.
		refusal string
		// noLook is a look that ends with the run's refusal.
		noLook bool
	}{
		{name: "ambiguous", second: "feat: a", notice: true, refusal: "names more than one commit"},
		{name: "one target", second: "feat: b"},
		{name: "onto with no move", second: "feat: a", onto: "main"},
		{name: "onto with a move", second: "feat: a", onto: "main", notice: true, refusal: "names more than one commit",
			prepare: func(t *testing.T, _, tmp string) {
				testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--allow-empty", "-m", "move! feat: x")
			}},
		{name: "no layer", second: "feat: a", refusal: "no layer branch", noLook: true, prepare: func(t *testing.T, main, _ string) {
			testkit.GitIn(t, main, "branch", "-q", "-D", "low1")
		}},
		{name: "dirty", second: "feat: a", refusal: "tracked changes", prepare: func(t *testing.T, _, tmp string) {
			gittest.WriteFile(t, filepath.Join(tmp, "b.txt"), "in progress\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, tmp := divergedLine(t, "x.txt", c.second, 0)
			if c.prepare != nil {
				c.prepare(t, ctx.Repo.MainRoot, tmp)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE", trace)
			progress := &noticeWriter{t: t, trace: trace}
			var look bytes.Buffer
			opts := synccmd.SyncOptions{NoFetch: true, Onto: c.onto, Progress: progress,
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
			lookErr := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look)
			if c.noLook != (lookErr != nil) || c.noLook && !strings.Contains(lookErr.Error(), c.refusal) {
				t.Fatalf("the look: %v\n%s", lookErr, look.String())
			}
			var want []bool
			if c.notice {
				want = []bool{true}
			}
			if replayStarted(t, trace) != (c.onto != "") {
				t.Errorf("the look replayed the line: %v, want %v\n%s", c.onto == "", c.onto != "", look.String())
			}
			if fmt.Sprint(progress.early) != fmt.Sprint(want) {
				t.Errorf("notices ahead of the replay: %v, want %v", progress.early, want)
			}
			// A dirty worktree is refused before its fixups are, by the run too.
			refused := c.second == "feat: a" && !c.noLook && (c.onto == "" || c.notice) && c.name != "dirty"
			if strings.Contains(look.String(), "names more than one commit") != refused {
				t.Errorf("the look's stdout refuses the fixup: %v, want %v\n%s", !refused, refused, look.String())
			}
			if c.refusal == "" {
				return
			}
			run := synccmdtest.NoAgents()
			run.Onto = c.onto
			run.Stack = &synccmd.StackOptions{Autosquash: true}
			_, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", run)
			if err == nil {
				t.Fatalf("the run was not refused\n%s", human)
			}
			said := human + err.Error()
			if !strings.Contains(said, c.refusal) {
				t.Errorf("the run does not say %q:\n%s", c.refusal, said)
			}
			for _, n := range progress.notices {
				_, why, _ := strings.Cut(strings.TrimSpace(n), "refuse the fold: ")
				if why == "" || !strings.Contains(said, why) {
					t.Errorf("the notice %q is not the run's refusal:\n%s", n, said)
				}
			}
		})
	}
}

// The same line's plain look replays it onto trunk, which the look of its
// fold in place does not (TestFoldLookNamesAnAmbiguousFixupAhead).
//
// Not parallel: GIT_TRACE is set for the whole process.
func TestPlainLookReplaysTheLineOntoTrunk(t *testing.T) {
	skipShort(t)
	ctx, _ := divergedLine(t, "x.txt", "feat: b", 0)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	var look bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true}, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	if !replayStarted(t, trace) {
		t.Errorf("the plain look never replayed the line:\n%s", look.String())
	}
}

// The last layer leaving the line after the early look at the fixup targets is
// seen: the look reads the line as it is afterwards, and refuses it as the run
// does.
func TestFoldLookReadsTheLayersAfterTheEarlyRead(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := divergedLine(t, "x.txt", "feat: b", 0)
	var look bytes.Buffer
	progress := onPhase("assessing the worktree", func() {
		testkit.GitIn(t, ctx.Repo.MainRoot, "branch", "-q", "-f", "low1", "main")
	})
	opts := synccmd.SyncOptions{NoFetch: true, Progress: progress, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
	lookErr := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look)
	if lookErr == nil {
		t.Fatalf("the look promises a fold of a line with no layer:\n%s", look.String())
	}
	run := synccmdtest.NoAgents()
	run.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", run)
	if err == nil || err.Error() != lookErr.Error() || !strings.Contains(err.Error(), "no layer branch") {
		t.Errorf("the look refuses with %q, the run with %v\n%s", lookErr, err, human)
	}
}

// A fixup that gets its target after the early look at the targets is not
// refused: the look checks them again afterwards, and the run takes it.
func TestFoldLookChecksTheTargetsAfterTheEarlyRead(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := divergedLine(t, "x.txt", "feat: b", 0)
	fixup(t, tmp, "a.txt", "release-point")
	var look bytes.Buffer
	// The tag names feat: a, two below the tip, as the fixup's target.
	progress := onPhase("assessing the worktree", func() {
		testkit.GitIn(t, ctx.Repo.MainRoot, "tag", "-f", "release-point", "tmp/low~3")
	})
	opts := synccmd.SyncOptions{NoFetch: true, Progress: progress, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	if !strings.Contains(progress.String(), "refuse the fold") {
		t.Fatalf("the fixup had a target before the replay:\n%s", progress.String())
	}
	if !strings.Contains(look.String(), "2 fixups fold in place") {
		t.Errorf("the look still refuses the fold:\n%s", look.String())
	}
	run := synccmdtest.NoAgents()
	run.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", run); err != nil {
		t.Errorf("the run: %v\n%s", err, human)
	}
}

// A look says what it is doing on the progress writer, in plain lines, and
// prints the same on stdout as without one: a look that replays the line has
// a replay count every tenth commit, and a fold in place names no replay.
func TestLookWritesProgressApartFromStdout(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := divergedLine(t, "t.txt", "feat: b", 40)
	look := func(autosquash bool, opts synccmd.SyncOptions) string {
		opts.Stack = &synccmd.StackLook{Work: "tmp/low", Autosquash: autosquash}
		var out bytes.Buffer
		if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return out.String()
	}
	// A clock that stands still: no count is written for time passed.
	frozen := time.Now()
	watch := func(autosquash bool, phases ...string) string {
		var progress bytes.Buffer
		watched := look(autosquash, synccmd.SyncOptions{Progress: &progress, Clock: func() time.Time { return frozen }})
		if silent := look(autosquash, synccmd.SyncOptions{}); watched != silent {
			t.Errorf("stdout with a progress writer:\n%s\nwithout:\n%s", watched, silent)
		}
		got := progress.String()
		rest := got
		for _, phase := range phases {
			_, after, ok := strings.Cut(rest, phase)
			if !ok {
				t.Fatalf("progress lacks %q, or has it out of order:\n%s", phase, got)
			}
			rest = after
		}
		if strings.Contains(got, "\r") {
			t.Errorf("a carriage return in the progress:\n%q", got)
		}
		return got
	}
	got := watch(false, "fetching", "assessing a rebase onto origin/main",
		"replaying onto origin/main", "reading side branches", "each_commit")
	var counts []string
	for _, m := range regexp.MustCompile(`replaying onto origin/main (\S+)`).FindAllStringSubmatch(got, -1) {
		counts = append(counts, m[1])
	}
	if want := "0/44 10/44 20/44 30/44 40/44"; strings.Join(counts, " ") != want {
		t.Errorf("replay counts %v, want %s", counts, want)
	}
	got = watch(true, "fetching", "resolving fixup targets", "assessing the worktree",
		"reading side branches", "analysing fixup context", "each_commit")
	for _, bad := range []string{"assessing a rebase", "replaying"} {
		if strings.Contains(got, bad) {
			t.Errorf("a fold in place says %q:\n%s", bad, got)
		}
	}
}
