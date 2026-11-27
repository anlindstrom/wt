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

// divergedLine is a line on tmp/low that is behind trunk: feat: x, feat: a, a
// commit subject second with the layer low1 at it, extra more commits, and a
// fixup of feat: a. Trunk has changed trunkFile since. A script on trunk claims
// x.txt, which the line's first commit changes: with x.txt as trunkFile that
// commit stops the replay, where the script runs the shell lines of then.
func divergedLine(t *testing.T, trunkFile, second string, extra int, then string) (ctx *kit.Context, tmp string) {
	t.Helper()
	ctx, _ = synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write("x.txt", "base\n")
		write(".wt-sync.yaml", "conflicts:\n  - paths: [x.txt]\n    strategy: script\n    run: tools/own.sh\neach_commit:\n  - run: 'true'\n")
		write("tools/own.sh", "#!/bin/sh\n"+then)
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
// came ahead of the replay.
type noticeWriter struct {
	t       *testing.T
	trace   string
	notices []string
	early   []bool
}

func (w *noticeWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("fixup! feat: a")) {
		w.notices = append(w.notices, string(p))
		w.early = append(w.early, !replayStarted(w.t, w.trace))
	}
	return len(p), nil
}

// A fixup the run would refuse is named on the progress writer before the
// assessment starts its replay, in the words the run refuses with;
// where the run stops for something else first, it is not named.
//
// Not parallel: GIT_TRACE is set for the whole process.
func TestFoldLookNamesAnAmbiguousFixupBeforeTheReplay(t *testing.T) {
	skipShort(t)
	for _, c := range []struct {
		name, second, onto string
		prepare            func(t *testing.T, main, tmp string)
		notice             bool
		// refusal is what the run says, "" where the case does not run it.
		refusal string
	}{
		{name: "ambiguous", second: "feat: a", notice: true, refusal: "names more than one commit"},
		{name: "one target", second: "feat: b"},
		{name: "onto with no move", second: "feat: a", onto: "main"},
		{name: "no layer", second: "feat: a", refusal: "no layer branch", prepare: func(t *testing.T, main, _ string) {
			testkit.GitIn(t, main, "branch", "-q", "-D", "low1")
		}},
		{name: "dirty", second: "feat: a", refusal: "tracked changes", prepare: func(t *testing.T, _, tmp string) {
			gittest.WriteFile(t, filepath.Join(tmp, "b.txt"), "in progress\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, tmp := divergedLine(t, "x.txt", c.second, 0, "")
			if c.prepare != nil {
				c.prepare(t, ctx.Repo.MainRoot, tmp)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE", trace)
			progress := &noticeWriter{t: t, trace: trace}
			var look bytes.Buffer
			opts := synccmd.SyncOptions{NoFetch: true, Onto: c.onto, Progress: progress,
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
			if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look); err != nil {
				t.Fatalf("%v\n%s", err, look.String())
			}
			var want []bool
			if c.notice {
				want = []bool{true}
			}
			if !replayStarted(t, trace) {
				t.Fatalf("the look never replayed the line:\n%s", look.String())
			}
			if fmt.Sprint(progress.early) != fmt.Sprint(want) {
				t.Errorf("notices ahead of the replay: %v, want %v", progress.early, want)
			}
			refused := c.second == "feat: a" && c.onto == ""
			if strings.Contains(look.String(), "names more than one commit") != refused {
				t.Errorf("the look's stdout refuses the fixup: %v, want %v\n%s", !refused, refused, look.String())
			}
			if c.refusal == "" {
				return
			}
			run := synccmdtest.NoAgents()
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

// A layer that leaves the line while the assessment replays is not promised a
// move: the fold look reads the line as it is afterwards.
func TestFoldLookReadsTheLayersAfterTheReplay(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := divergedLine(t, "x.txt", "feat: b", 0, "git branch -q -f low1 main\n")
	var look bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	if !strings.Contains(look.String(), "re-pointed: none") || strings.Contains(look.String(), "low1") {
		t.Errorf("the look still has the layer that left the line:\n%s", look.String())
	}
}

// A fixup that gets its target while the assessment replays is not refused:
// the look checks the targets again afterwards, and the run takes it.
func TestFoldLookChecksTheTargetsAfterTheReplay(t *testing.T) {
	t.Parallel()
	skipShort(t)
	// The script names feat: a, two below the tip, as the fixup's target.
	ctx, tmp := divergedLine(t, "x.txt", "feat: b", 0, "git tag -f release-point tmp/low~3 >/dev/null\n")
	fixup(t, tmp, "a.txt", "release-point")
	var look, progress bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Progress: &progress, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
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

// A look says what it is doing on the progress writer, in plain lines with a
// replay count every tenth commit, and prints the same on stdout as without
// one.
func TestLookWritesProgressApartFromStdout(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := divergedLine(t, "t.txt", "feat: b", 40, "")
	look := func(opts synccmd.SyncOptions) string {
		opts.Stack = &synccmd.StackLook{Work: "tmp/low", Autosquash: true}
		var out bytes.Buffer
		if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return out.String()
	}
	var progress bytes.Buffer
	// A clock that stands still: no count is written for time passed.
	frozen := time.Now()
	watched := look(synccmd.SyncOptions{Progress: &progress, Clock: func() time.Time { return frozen }})
	if silent := look(synccmd.SyncOptions{}); watched != silent {
		t.Errorf("stdout with a progress writer:\n%s\nwithout:\n%s", watched, silent)
	}
	got := progress.String()
	rest := got
	for _, phase := range []string{"fetching", "resolving fixup targets", "assessing a rebase onto origin/main",
		"replaying onto origin/main", "reading side branches", "analysing fixup context", "each_commit"} {
		_, after, ok := strings.Cut(rest, phase)
		if !ok {
			t.Fatalf("progress lacks %q, or has it out of order:\n%s", phase, got)
		}
		rest = after
	}
	var counts []string
	for _, m := range regexp.MustCompile(`replaying onto origin/main (\S+)`).FindAllStringSubmatch(got, -1) {
		counts = append(counts, m[1])
	}
	if want := "0/44 10/44 20/44 30/44 40/44"; strings.Join(counts, " ") != want {
		t.Errorf("replay counts %v, want %s", counts, want)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("a carriage return in the progress:\n%q", got)
	}
}
