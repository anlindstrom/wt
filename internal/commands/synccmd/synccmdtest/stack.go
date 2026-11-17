package synccmdtest

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// StackYAML is what StackLine declares on trunk: the owned-line rule that
// resolves s3's bump of v.txt against trunk's.
const StackYAML = "conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: max-plus-patch\n"

// StackLine is gittest's StackLine in a repository wt opens, with yaml
// declared on trunk (StackYAML when empty).
func StackLine(t *testing.T, yaml string) (*kit.Context, *gittest.StackLine) {
	t.Helper()
	if yaml == "" {
		yaml = StackYAML
	}
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := gittest.NewStackLine(t, main, wtDir, yaml)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s
}

// StackRun runs wt sync rebase <work> --stack --json with opts and returns
// its checked result and what it printed for a person.
func StackRun(t *testing.T, ctx *kit.Context, work string, opts synccmd.RunOptions) (syncrun.RunResult, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	command := "sync rebase"
	if opts.Steps != nil {
		command = "sync steps"
	}
	opts.Journal = syncrun.NewSyncRunJournal(&out, command)
	if opts.Stack == nil {
		opts.Stack = &synccmd.StackOptions{}
	}
	err := synccmd.SyncRebase(ctx, []string{work}, opts, &human)
	return DecodeSyncRun(t, out.Bytes()), human.String(), err
}

// Participant is the run's row for branch.
func Participant(t *testing.T, r syncrun.RunResult, branch string) *syncrun.SyncParticipant {
	t.Helper()
	for _, p := range r.Worktrees {
		if p.Branch == branch {
			return p
		}
	}
	t.Fatalf("no row for %s in %+v", branch, r.Worktrees)
	return nil
}

// FoldLine is gittest's FoldLine in a repository wt opens, with yaml
// declared on trunk.
func FoldLine(t *testing.T, yaml string) (*kit.Context, *gittest.FoldLine) {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := gittest.NewFoldLine(t, main, wtDir, yaml)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s
}

// FakeDocker puts a docker on PATH that answers docker info, for a step
// that needs docker. The test cannot run in parallel.
func FakeDocker(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

// CheckedLine is StackLine with an each_commit step on trunk running
// bin/check.sh, which appends the subject it ran at to the returned log and
// fails at every commit that has b1.txt until a commit has fix.txt: the shape
// of a line whose early commits do not pass a check that its tip does. The
// file logPath+".pass", when it exists, makes the script pass at every commit.
func CheckedLine(t *testing.T) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	return CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n")
}

// CheckedLineWith is CheckedLine with its own each_commit declaration.
func CheckedLineWith(t *testing.T, eachCommit string) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	return CheckedLineYAML(t, StackYAML+eachCommit)
}

// CheckedLineYAML is CheckedLine with the whole declaration its own.
func CheckedLineYAML(t *testing.T, yaml string) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "checked.log")
	ctx, s = StackLine(t, yaml)
	main := ctx.Repo.MainRoot
	script := "#!/bin/sh\ngit log -1 --format=%s >> '" + logPath + "'\n" +
		"if [ -f b1.txt ] && [ ! -f fix.txt ] && [ ! -f '" + logPath + ".pass' ]; then echo 'b1.txt needs fix.txt'; exit 1; fi\n"
	if err := os.MkdirAll(filepath.Join(main, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "bin", "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, main, "add", "-A")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: add the check script")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	return ctx, s, logPath
}

// CheckedSubjects is the subjects a CheckedLine's script ran at, in order.
func CheckedSubjects(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return lines(string(raw))
}

func lines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
