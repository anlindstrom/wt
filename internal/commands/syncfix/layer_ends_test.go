package syncfix

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// endsCommit is one commit of a layer: the file it writes and its subject.
type endsCommit struct{ subject, file, content, date string }

// endsLayer is a layer branch of the line, with the commits it adds.
type endsLayer struct {
	name    string
	commits []endsCommit
}

const endsTmp = "tmp/ends"

const takeLockYAML = "conflicts:\n  - paths: [lock.txt]\n    strategy: take-trunk\n"

// endsLine builds a line on a base with lock.txt, the layers bottom first, and
// the commits the line's own branch (tmp/ends, in a worktree) adds above the
// last layer. Trunk then rewrites lock.txt and adds other.txt, and declares
// yaml. No layer has a worktree: every layer moves by its branch.
func endsLine(t *testing.T, yaml string, layers []endsLayer, top []endsCommit) (*kit.Context, string, map[string]string) {
	t.Helper()
	return endsLineWith(t, yaml, nil, nil, layers, top)
}

// endsLineWith is endsLine with files the base carries and files trunk adds
// (an .sh file is executable).
func endsLineWith(t *testing.T, yaml string, baseFiles, trunkFiles map[string]string, layers []endsLayer, top []endsCommit) (*kit.Context, string, map[string]string) {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	write := func(dir, file, content string) {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(file, ".sh") {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(dir string, c endsCommit) {
		write(dir, c.file, c.content)
		testkit.GitIn(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "-m", c.subject}
		if c.date != "" {
			args = append(args, "--date", c.date)
		}
		testkit.GitIn(t, dir, args...)
	}
	for f, c := range baseFiles {
		write(main, f, c)
	}
	commit(main, endsCommit{"base", "lock.txt", "lock 0\n", ""})
	testkit.GitIn(t, main, "checkout", "-q", "-b", endsTmp)
	tips := map[string]string{}
	for _, l := range layers {
		for _, c := range l.commits {
			commit(main, c)
		}
		testkit.GitIn(t, main, "branch", l.name)
		tips[l.name] = testkit.GitOut(t, main, "rev-parse", "HEAD")
	}
	for _, c := range top {
		commit(main, c)
	}
	tips[endsTmp] = testkit.GitOut(t, main, "rev-parse", "HEAD")
	testkit.GitIn(t, main, "checkout", "-q", "main")
	if yaml != "" {
		write(main, ".wt-sync.yaml", yaml)
		testkit.GitIn(t, main, "add", "-A")
		testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: declare")
	}
	commit(main, endsCommit{"chore: trunk reconciles the lockfile", "lock.txt", "lock trunk\n", ""})
	for f, c := range trunkFiles {
		write(main, f, c)
	}
	commit(main, endsCommit{"feat: trunk's own", "other.txt", "other\n", ""})
	testkit.GitIn(t, main, "remote", "add", "origin", main)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "tmp_ends"), endsTmp)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, filepath.Join(wtDir, "tmp_ends"), tips
}

// endsState is what a run and its undo are held to: every branch's tip, the
// stash and the line's worktree status.
func endsState(t *testing.T, ctx *kit.Context, tmp string) worktreeState {
	t.Helper()
	main := ctx.Repo.MainRoot
	return worktreeState{
		refs:   testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"),
		stash:  testkit.GitOut(t, main, "stash", "list", "--format=%H %gs"),
		status: testkit.GitOut(t, tmp, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

// endsSubjects is the subjects of the commits of rev above main, oldest first.
func endsSubjects(t *testing.T, main, rev string) string {
	t.Helper()
	return strings.ReplaceAll(testkit.GitOut(t, main, "log", "--reverse", "--format=%s", "main.."+rev), "\n", " | ")
}

// runEnds folds and moves the line onto trunk, asserts every layer moved to
// the subjects want says, the line's own tip, the clean worktree and that no
// marker is left, then undoes the run and asserts everything is back.
func runEnds(t *testing.T, ctx *kit.Context, tmp string, want, wantMapped map[string]string, wantWhy ...map[string]string) string {
	t.Helper()
	main := ctx.Repo.MainRoot
	was := endsState(t, ctx, tmp)
	res, human, err := synccmdtest.StackRun(t, ctx, endsTmp, foldOntoTrunk())
	if err != nil {
		t.Fatalf("err %v\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, endsTmp)
	if p.Stack == nil || p.Stack.Refusal != nil {
		t.Fatalf("stack %+v\n%s", p.Stack, human)
	}
	layers := stackLayers(p.Stack)
	for b, subjects := range want {
		l, ok := layers[b]
		if !ok {
			if b == endsTmp {
				continue
			}
			t.Fatalf("no layer %s in %+v", b, p.Stack.Layers)
		}
		if deref(l.Result) != wtsync.MoveMoved && deref(l.Result) != wtsync.MoveUnchanged {
			t.Errorf("%s: %s (%s)", b, deref(l.Result), deref(l.Held))
		}
		if got := testkit.GitOut(t, main, "rev-parse", b); got != deref(l.NewTip) {
			t.Errorf("%s is at %s, want its new tip %s", b, got, deref(l.NewTip))
		}
		if got := endsSubjects(t, main, b); got != subjects {
			t.Errorf("%s: commits\n got %s\nwant %s", b, got, subjects)
		}
		if m, ok := wantMapped[b]; ok && deref(l.Mapped) != m {
			t.Errorf("%s: mapped %q, want %q", b, deref(l.Mapped), m)
		}
		for _, why := range wantWhy {
			if w, ok := why[b]; ok && deref(l.PatchWhy) != w {
				t.Errorf("%s: patchWhy %q, want %q", b, deref(l.PatchWhy), w)
			}
		}
	}
	if got := endsSubjects(t, main, endsTmp); got != want[endsTmp] {
		t.Errorf("%s: commits\n got %s\nwant %s", endsTmp, got, want[endsTmp])
	}
	if st := testkit.GitOut(t, tmp, "status", "--porcelain=v1", "--untracked-files=all"); st != "" {
		t.Errorf("the line's worktree is not clean after the run: %q", st)
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left behind: %s", refs)
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, endsTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	sameState(t, "after the undo", was, endsState(t, ctx, tmp))
	return human
}

// A commit a take-trunk path empties, last in the middle layer of three:
// the layer ends at its last commit that survives, the layers above are
// untouched by the drop, and the run says which commit went and where the
// layer now ends.
func TestStackOntoMiddleLayerLosingItsLastCommitEndsAtTheLastSurvivor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, tips := endsLine(t, takeLockYAML, []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}, {"chore(e2): reconcile the lockfile", "lock.txt", "lock e2\n", ""}}},
		{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}})
	short := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "--short=7", tips["e2"])
	human := runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a | feat(e2): b",
		"e3":    "feat(e1): a | feat(e2): b | feat(e3): c",
		endsTmp: "feat(e1): a | feat(e2): b | feat(e3): c | feat: top",
	}, map[string]string{"e2": wtsync.MappedTipDropped})
	if want := "dropped as empty: " + short + " chore(e2): reconcile the lockfile; ends at "; !strings.Contains(human, want) || !strings.Contains(human, " feat(e2): b; branch moved") {
		t.Errorf("e2's line does not say which commit was dropped and where the layer ends (%q):\n%s", want, human)
	}
}

// The emptied commit is the only commit of its layer: the layer ends where
// the layer below ends, and the layers above are unaffected.
func TestStackOntoLayerLosingItsOnlyCommitEndsWhereTheOneBelowEnds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, tips := endsLine(t, takeLockYAML, []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"chore(e2): reconcile the lockfile", "lock.txt", "lock e2\n", ""}}},
		{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}})
	short := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "--short=7", tips["e2"])
	human := runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a",
		"e3":    "feat(e1): a | feat(e3): c",
		endsTmp: "feat(e1): a | feat(e3): c | feat: top",
	}, map[string]string{"e2": wtsync.MappedEmptied})
	if want := "dropped as empty: " + short + " chore(e2): reconcile the lockfile; ends where e1 ends"; !strings.Contains(human, want) {
		t.Errorf("e2's line does not say where it ends (%q):\n%s", want, human)
	}
}

// A top layer whose last commit is a fixup! of an earlier layer's commit, and
// a middle layer ending on one: both fold, and every layer moves, each
// ending at its last commit that stands on its own.
func TestStackOntoLayersEndingOnAFixupFoldAndMove(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}, {"feat(e1): b", "b.txt", "b\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): c", "c.txt", "c\n", ""}, {"fixup! feat(e1): a", "a.txt", "fixed\n", ""}}},
		{"e3", []endsCommit{{"feat(e3): d", "d.txt", "d\n", ""}, {"fixup! feat(e2): c", "c.txt", "fixed\n", ""}}},
	}, nil)
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a | feat(e1): b",
		"e2":    "feat(e1): a | feat(e1): b | feat(e2): c",
		"e3":    "feat(e1): a | feat(e1): b | feat(e2): c | feat(e3): d",
		endsTmp: "feat(e1): a | feat(e1): b | feat(e2): c | feat(e3): d",
	}, nil)
}

// A layer made only of fixups folds into the layers below, and ends where
// the layer below it ends.
func TestStackOntoLayerOfOnlyFixupsEndsWhereTheOneBelowEnds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}}},
		{"e3", []endsCommit{{"fixup! feat(e1): a", "a.txt", "fixed\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}})
	human := runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a | feat(e2): b",
		"e3":    "feat(e1): a | feat(e2): b",
		endsTmp: "feat(e1): a | feat(e2): b | feat: top",
	}, map[string]string{"e3": wtsync.MappedEmptied}, map[string]string{"e3": "every commit of it was folded into an earlier commit"})
	if !strings.Contains(human, "folded: every commit of it went into an earlier commit; ends where e2 ends") || strings.Contains(human, "carries every commit") {
		t.Errorf("e3's line does not say it was folded:\n%s", human)
	}
}

// The line's own branch ending on a fixup folds like any layer's tip.
func TestStackOntoLineEndingOnAFixupFoldsAndMoves(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}, {"fixup! feat(e1): a", "a.txt", "fixed\n", ""}})
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a | feat(e2): b",
		endsTmp: "feat(e1): a | feat(e2): b | feat: top",
	}, nil)
}

// An amend! folded into the first layer's commit renames it; a commit the
// middle layer loses as empty in the same run does not make the renamed
// commit look dropped, and every layer ends where the rule says.
func TestStackOntoAnAmendAndADropInOneRunEndEachLayerRight(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLine(t, takeLockYAML, []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}, {"chore(e2): reconcile the lockfile", "lock.txt", "lock e2\n", ""}}},
	}, []endsCommit{{"amend! feat(e1): a\n\nfeat(e1): a, renamed", "a.txt", "amended\n", ""}})
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a, renamed",
		"e2":    "feat(e1): a, renamed | feat(e2): b",
		endsTmp: "feat(e1): a, renamed | feat(e2): b",
	}, map[string]string{"e2": wtsync.MappedTipDropped})
}
