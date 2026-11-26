package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// moveRepo is a repository with a source of two hunks and no move! commit,
// as the current directory.
func moveRepo(t *testing.T) (main string, s *gittest.MoveLine) {
	t.Helper()
	ctx, line, _ := synccmdtest.MoveLineShaped(t, gittest.MoveShape{TwoHunks: true}, "", false)
	t.Chdir(ctx.Repo.MainRoot)
	return ctx.Repo.MainRoot, line
}

// runStreams runs wt with stdout and stderr captured apart.
func runStreams(args ...string) (stdout string, err error) {
	var out, errOut bytes.Buffer
	root := newRootCmd(args...)
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}

type moveJSON struct {
	Command string `json:"command"`
	Commit  *struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
	} `json:"commit"`
	From, Into *struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
	}
	Paths []string `json:"paths"`
	Lines []struct {
		Path     string `json:"path"`
		From, To int
	} `json:"lines"`
	Refusal *string `json:"refusal"`
}

func decodeMove(t *testing.T, out string) moveJSON {
	t.Helper()
	testkit.ValidateJSON(t, "sync-move", []byte(out))
	var m moveJSON
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return m
}

// --lines writes the Move-Lines lines, beside any paths, and --json says what
// was written.
func TestSyncMoveWritesLinesAndSaysSoInJSON(t *testing.T) {
	main, _ := moveRepo(t)
	old := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp)
	out, err := runStreams("sync", "move", gittest.MoveTmp, "--lines", "door/refuse.go:2-2",
		"--from", gittest.MoveSource, "--into", gittest.MoveTarget, "--json")
	if err != nil {
		t.Fatalf("move: %v\n%s", err, out)
	}
	m := decodeMove(t, out)
	tip := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp)
	want := "move! " + gittest.MoveTarget + "\n\n" + gittest.MoveLinesBody(gittest.MoveSource, "door/refuse.go:2-2")
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%B", tip); strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Errorf("the move! commit says\n%s\nwant\n%s", got, want)
	}
	if testkit.GitOut(t, main, "rev-parse", tip+"^") != old {
		t.Errorf("the move! commit is not on the old tip")
	}
	if m.Command != "sync move" || m.Refusal != nil || m.Commit == nil || m.Commit.SHA != tip || m.Commit.Message != want {
		t.Errorf("json %s", out)
	}
	if m.From == nil || m.From.SHA != testkit.GitOut(t, main, "rev-parse", "l3") || m.From.Subject != gittest.MoveSource ||
		m.Into == nil || m.Into.SHA != testkit.GitOut(t, main, "rev-parse", "l1") || m.Into.Subject != gittest.MoveTarget {
		t.Errorf("json from and into: %s", out)
	}
	if len(m.Paths) != 0 || len(m.Lines) != 1 || m.Lines[0].Path != "door/refuse.go" || m.Lines[0].From != 2 || m.Lines[0].To != 2 {
		t.Errorf("json paths and lines: %s", out)
	}
}

// A refused move writes nothing, and --json says why; the exit is a failure.
func TestSyncMoveRefusalInJSON(t *testing.T) {
	main, s := moveRepo(t)
	testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
	old := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp)
	out, err := runStreams("sync", "move", gittest.MoveTmp, "feature.txt", "--lines", "door/refuse.go:1-1",
		"--from", gittest.MoveSource, "--into", gittest.MoveTarget, "--json")
	if err == nil || errorStatus(err) != 1 {
		t.Fatalf("err %v, want exit 1\n%s", err, out)
	}
	m := decodeMove(t, out)
	if m.Commit != nil || m.Refusal == nil || !strings.Contains(*m.Refusal, "door/refuse.go:@@ -1,5 +1,5 @@") || m.Refusal != nil && !strings.Contains(err.Error(), *m.Refusal) {
		t.Errorf("json %s, err %v", out, err)
	}
	if m.From == nil || m.Into == nil || len(m.Paths) != 1 || len(m.Lines) != 1 {
		t.Errorf("a refusal does not say what was asked: %s", out)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp); got != old {
		t.Errorf("a refused move wrote %s", got)
	}
	if got := testkit.GitOut(t, s.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Errorf("status %q", got)
	}
	// A name that finds no commit leaves from null.
	out, err = runStreams("sync", "move", gittest.MoveTmp, "feature.txt", "--from", "feat: nothing", "--into", gittest.MoveTarget, "--json")
	if m := decodeMove(t, out); err == nil || m.From != nil || m.Refusal == nil || !strings.Contains(*m.Refusal, "--from") {
		t.Errorf("err %v, json %s", err, out)
	}
}

func TestSyncMoveUsageMistakes(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sync", "move", "w", "--from", "a", "--into", "b"}, "a path or --lines"},
		{[]string{"sync", "move", "w", "--lines", "a.go:3", "--from", "a", "--into", "b", "--json"}, "<path>:<from>-<to>"},
	} {
		out, err := runStreams(c.args...)
		var u usageError
		if !errors.As(err, &u) || !strings.Contains(err.Error(), c.want) || strings.HasPrefix(out, "{") {
			t.Errorf("%v: err %v, stdout %q, want a usage mistake saying %q", c.args, err, out, c.want)
		}
	}
}
