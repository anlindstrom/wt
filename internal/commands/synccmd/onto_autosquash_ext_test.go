package synccmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
