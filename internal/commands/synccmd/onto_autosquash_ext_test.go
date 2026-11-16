package synccmd_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// fixup commits file's new content onto the line's tip as a fixup! of the
// commit with subject.
func fixup(t *testing.T, dir, file, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, dir, "commit", "-q", "--no-verify", "-am", "fixup! "+subject)
}

// foldOnto is --stack --autosquash --onto ref.
func foldOnto(ref string) synccmd.RunOptions {
	opts := synccmdtest.Onto(ref, "")
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	return opts
}

// subjectsAbove is the subjects of the commits of rev above base, oldest first.
func subjectsAbove(t *testing.T, dir, base, rev string) []string {
	t.Helper()
	out := testkit.GitOut(t, dir, "log", "--reverse", "--format=%s", base+".."+rev)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// One run folds the line's fixups and moves it onto trunk: a fixup whose
// target is in a layer below the tip folds there, every layer above carries
// it, every layer is re-pointed with its own subjects and content, a commit
// trunk already has is dropped as in a plain --stack onto trunk, and no
// fixup is left.
func TestOntoAutosquashFoldsAndMovesTheLineInOneRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	fixup(t, tmp, "e1.txt", "feat(s5): e1.txt")
	main := ctx.Repo.MainRoot
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOnto("main"))
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the two held layers\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebased || p.Stack == nil || !p.Stack.Autosquash {
		t.Fatalf("line %+v\n%s", p, human)
	}
	if !testkit.GitAncestor(t, main, "main", gittest.StackTmp) {
		t.Fatalf("the line is not on trunk\n%s", human)
	}
	if out := testkit.GitOut(t, main, "log", "--format=%s", "main.."+gittest.StackTmp, "--grep=^fixup!"); out != "" {
		t.Errorf("a fixup was left on the line: %s", out)
	}
	layers := layersOf(p.Stack)
	// Every layer's commits include the ones below it; s2's tip, b3, and
	// s4's commits are on trunk and drop out.
	s1 := s.Subjects["s1"]
	s2 := append(append([]string{}, s1...), s.Subjects["s2"][:2]...)
	s3 := append(append([]string{}, s2...), s.Subjects["s3"]...)
	s5 := append(append([]string{}, s3...), s.Subjects["s5"]...)
	for b, want := range map[string]struct {
		result   string
		subjects []string
	}{
		"s1": {wtsync.MoveMoved, s1},
		"s2": {wtsync.MoveMoved, s2},
		"s3": {wtsync.MoveHeld, s3},
		"s4": {wtsync.MoveMoved, s3},
		"s5": {wtsync.MoveHeld, s5},
	} {
		l := layers[b]
		if str(l.Result) != want.result {
			t.Errorf("%s: %s (%s), want %s", b, str(l.Result), str(l.Held), want.result)
		}
		got := subjectsAbove(t, main, "main", str(l.NewTip))
		if strings.Join(got, "|") != strings.Join(want.subjects, "|") {
			t.Errorf("%s: subjects\n got %q\nwant %q", b, got, want.subjects)
		}
	}
	for b, files := range map[string]map[string]string{
		"s1": {"a1.txt": "s1 a1.txt"},
		"s2": {"b1.txt": "fixed", "b2.txt": "s2 b2.txt"},
		"s3": {"b1.txt": "fixed", "c1.txt": "s3 c1.txt"},
		"s5": {"b1.txt": "fixed", "e1.txt": "fixed", "e2.txt": "s5 e2.txt"},
	} {
		for f, want := range files {
			if got := testkit.GitOut(t, main, "show", str(layers[b].NewTip)+":"+f); got != want {
				t.Errorf("%s's new tip has %s %q, want %q", b, f, got, want)
			}
		}
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != str(layers[b].NewTip) {
			t.Errorf("%s at %s, want its new tip %s", b, got, str(layers[b].NewTip))
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("held %s moved to %s", b, got)
		}
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left behind: %s", refs)
	}
}

// scriptedLine is a stack on an old trunk commit whose trunk later adds
// bin/check.sh and an each_commit step running it at every commit; the
// script appends the subject it ran at to the returned log. Two fixups sit
// at the tip.
func scriptedLine(t *testing.T) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "checked.log")
	yaml := synccmdtest.StackYAML + "each_commit:\n  - run: bin/check.sh\n"
	ctx, s = synccmdtest.StackLine(t, yaml)
	main := ctx.Repo.MainRoot
	script := "#!/bin/sh\ngit log -1 --format=%s >> '" + logPath + "'\n"
	if err := os.MkdirAll(filepath.Join(main, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "bin", "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, main, "add", "-A")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: add the check script")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	fixup(t, tmp, "e1.txt", "feat(s5): e1.txt")
	return ctx, s, logPath
}

// The case of the issue: a stack on an old trunk commit, trunk since added a
// script and a step running it, fixups at the tip. One run folds and moves
// it: trunk's script is in every replayed tree, so nothing exits 127, each
// commit is checked once, and the layers come out right.
func TestOntoAutosquashChecksEachCommitOnceWithTrunksScript(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := scriptedLine(t)
	main := ctx.Repo.MainRoot
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOnto("main"))
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want only the two held layers\n%s", err, human)
	}
	if strings.Contains(human, "127") || strings.Contains(human, "owed") {
		t.Errorf("the run stopped or owes a check:\n%s", human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebased || p.Checked == 0 {
		t.Fatalf("line %+v\n%s", p, human)
	}
	final := testkit.GitOut(t, main, "rev-list", "--count", "main.."+gittest.StackTmp)
	raw, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	checked := strings.Split(strings.TrimSpace(string(raw)), "\n")
	seen := map[string]bool{}
	for _, subject := range checked {
		if seen[subject] {
			t.Errorf("%q was checked twice", subject)
		}
		seen[subject] = true
		if strings.HasPrefix(subject, "fixup!") {
			t.Errorf("a fixup was checked: %q", subject)
		}
	}
	if got := len(checked); strconv.Itoa(got) != final {
		t.Errorf("checked %d commits, the folded line has %s", got, final)
	}
	layers := layersOf(p.Stack)
	for b, file := range map[string]string{"s2": "b1.txt", "s5": "e1.txt"} {
		if got := testkit.GitOut(t, main, "show", str(layers[b].NewTip)+":"+file); got != "fixed" {
			t.Errorf("%s's new tip has %s %q", b, file, got)
		}
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != str(layers[b].NewTip) {
			t.Errorf("%s at %s, want %s", b, got, str(layers[b].NewTip))
		}
	}
	_ = s
}

// The same stack folded in place, onto its own base, replays trees that
// predate the script: the step is not run and not failed, it is reported as
// not checked and owed, and the fold finishes with every layer re-pointed.
func TestAutosquashInPlaceOwesAStepWhoseScriptTheTreePredates(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := scriptedLine(t)
	main := ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "bin/check.sh") {
		t.Fatalf("err %v, want the owed step named\n%s", err, human)
	}
	if strings.Contains(human, "exit 127") || strings.Contains(human, "needs you") {
		t.Errorf("the run stopped:\n%s", human)
	}
	for _, want := range []string{"not checked", "bin/check.sh is not in the tree", "owed"} {
		if !strings.Contains(human, want) {
			t.Errorf("the output lacks %q:\n%s", want, human)
		}
	}
	if _, serr := os.Stat(logPath); serr == nil {
		t.Error("the script ran in a tree that predates it")
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Stack == nil || !p.Stack.Autosquash {
		t.Fatalf("stack %+v\n%s", p.Stack, human)
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", s.Base+".."+gittest.StackTmp); n != "12" {
		t.Errorf("the line has %s commits from its base, want 12", n)
	}
	layers := layersOf(p.Stack)
	if got := testkit.GitOut(t, main, "show", str(layers["s2"].NewTip)+":b1.txt"); got != "fixed" {
		t.Errorf("s2's new tip has b1.txt %q", got)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "s2"); got != str(layers["s2"].NewTip) {
		t.Errorf("s2 not re-pointed: %s", got)
	}
}
