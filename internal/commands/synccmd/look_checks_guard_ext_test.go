package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A temporary directory inside the repository would have the look write there:
// it is refused, and nothing is written in it.
func TestTargetsLookRefusesATemporaryDirectoryInTheSource(t *testing.T) {
	// Not parallel: sets TMPDIR.
	skipShort(t)
	for _, tc := range []struct {
		name  string
		under func(main, line string) string
	}{
		{"in the worktree", func(_, line string) string { return line }},
		{"in the git dir", func(main, _ string) string { return filepath.Join(main, ".git") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := filepath.Join(t.TempDir(), "ran")
			l := foldedLine(t, "each_commit:\n  - run: touch '"+ran+"'; exit 1\n")
			temp := filepath.Join(tc.under(l.ctx.Repo.MainRoot, l.s.Paths[gittest.StackTmp]), "scratch-tmp")
			if err := os.Mkdir(temp, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", temp)
			o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
			wantStatus(t, err, 1)
			if o.Error == nil || !strings.Contains(*o.Error, "TMPDIR") {
				t.Errorf("error %v, steps %q", o.Error, lookSteps(o))
			}
			wantStatus(t, synccmd.SyncWorktree(l.ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), &bytes.Buffer{}), 1)
			if entries, err := os.ReadDir(temp); err != nil || len(entries) != 0 {
				t.Errorf("written in the source: %v %v", entries, err)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Error("a check ran")
			}
		})
	}
}

// A per-layer step that fails and leaves what git cannot clean away is still
// the failure the look reports, and the later checks run on a clean checkout.
func TestTargetsLookKeepsALayerFailureItCannotCleanUpAfter(t *testing.T) {
	t.Parallel()
	skipShort(t)
	l := foldedLine(t, "each_commit:\n  - run: test ! -e gen\n"+
		"defer:\n  - run: mkdir gen && echo x > gen/out && chmod a-w gen; exit 3\n"+
		"    paths: [b1.txt, gen/out]\n    each: layer\n    commit: 'chore: gen'\n")
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	steps := lookSteps(o)
	failed := false
	for _, c := range o.Fold.Checks {
		if c.Kind == "layerStep" && c.Steps[0].Result == "fail" {
			failed = c.Steps[0].Log != nil
		}
	}
	if !failed || o.Error != nil {
		t.Errorf("error %v, steps %q", o.Error, steps)
	}
	for _, step := range steps {
		if strings.HasPrefix(step, "stop ") && !strings.HasSuffix(step, ": pass") {
			t.Errorf("steps %q", steps)
		}
	}
}

// Files a step hard-linked are still one file after a later failure is undone.
func TestTargetsLookPutsHardLinksBackAsOneFile(t *testing.T) {
	t.Parallel()
	skipShort(t)
	l := foldedLine(t, "each_commit:\n"+
		"  - run: test -e one || { echo x > one && ln one two; }; test one -ef two\n"+
		"  - run: |\n      test \"$(git log -1 --format=%s)\" != 'feat(s2): b1.txt'\n")
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	steps := lookSteps(o)
	if len(steps) < 3 {
		t.Fatalf("steps %q", steps)
	}
	for _, step := range steps[2:] {
		if !strings.HasSuffix(step, ": pass") {
			t.Errorf("steps %q", steps)
			break
		}
	}
}

// Hooks are the ones git would run: a disabled one is none, and a
// configuration git cannot list them under refuses the look.
func TestTargetsLookReadsGitsOwnHookList(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		name   string
		config [][2]string
		status int
	}{
		{"disabled", [][2]string{{"hook.guard.command", "false"}, {"hook.guard.event", "pre-rebase"}, {"hook.guard.enabled", "false"}}, 0},
		{"no command", [][2]string{{"hook.guard.event", "pre-rebase"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := foldedLine(t, "each_commit:\n  - run: 'true'\n")
			for _, kv := range tc.config {
				testkit.GitIn(t, l.ctx.Repo.MainRoot, "config", kv[0], kv[1])
			}
			o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
			wantStatus(t, err, tc.status)
			if o.Fold == nil || len(o.Fold.HooksNotRun) != 0 || tc.status == 1 && (o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "hook")) {
				t.Errorf("error %v, fold %+v", o.Error, o.Fold)
			}
		})
	}
}

// Replacements kept anywhere but git's own namespace are not copied, so the
// look refuses rather than check other content.
func TestTargetsLookRefusesAnotherReplaceNamespace(t *testing.T) {
	// Not parallel: sets GIT_REPLACE_REF_BASE.
	skipShort(t)
	l := foldedLine(t, "each_commit:\n  - run: 'true'\n")
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/elsewhere/")
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, 1)
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "GIT_REPLACE_REF_BASE") {
		t.Errorf("error %v, fold %+v", o.Error, o.Fold)
	}
}

// A conflict that ends the replay does not take the hooks not run out of the
// text.
func TestTargetsLookNamesTheHooksAtAConflictToo(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: 'true'\n")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "e1.txt", "feat(s4): d1.txt")
	gittest.WriteExecutable(t, filepath.Join(ctx.Repo.MainRoot, ".git", "hooks", "pre-rebase"), "#!/bin/sh\nexit 1\n")
	var text bytes.Buffer
	wantStatus(t, synccmd.SyncWorktree(ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), &text), 1)
	if !strings.Contains(text.String(), "not run here: pre-rebase") || !strings.Contains(text.String(), "not reached") {
		t.Errorf("the look says:\n%s", text.String())
	}
}
