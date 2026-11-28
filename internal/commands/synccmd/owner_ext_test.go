package synccmd_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

const ownTmp = "tmp/own"

// doorFile is pkg/door.txt: twelve lines, d1 to d12, with those of changed
// rewritten.
func doorFile(changed map[int]string) string {
	var b strings.Builder
	for n := 1; n <= 12; n++ {
		line, ok := changed[n]
		if !ok {
			line = fmt.Sprintf("d%d", n)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// ownLine is a line on tmp/own, in a linked worktree: "feat: door" (layer l1)
// adds pkg/door.txt, "feat: body" (layer l2) rewrites its line 5, and "feat:
// other" is the top's own. base.txt is trunk's, and backup-door is a backup
// name on l1's commit. It returns the context opened in the line's worktree.
func ownLine(t *testing.T, yaml string) (ctx *kit.Context, main, tmp string) {
	t.Helper()
	_, main = synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write("base.txt", "t1\nt2\n")
		if yaml != "" {
			write(".wt-sync.yaml", yaml)
		}
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", ownTmp)
		write("pkg/door.txt", doorFile(nil))
		commit("feat: door")
		gittest.Git(t, main, "branch", "l1")
		gittest.Git(t, main, "branch", "backup-door")
		write("pkg/door.txt", doorFile(map[int]string{5: "d5 body"}))
		commit("feat: body")
		gittest.Git(t, main, "branch", "l2")
		write("other.txt", "other\n")
		commit("feat: other")
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_own")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, ownTmp)
	})
	return openAt(t, tmp), main, tmp
}

func openAt(t *testing.T, dir string) *kit.Context {
	t.Helper()
	ctx, err := kit.Open(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

type commitRef struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Layer   string `json:"layer"`
}

type spanJSON struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type ownerJSON struct {
	Branch *string `json:"branch"`
	Path   *string `json:"path"`
	Edited bool    `json:"edited"`
	Owners []struct {
		commitRef
		FoldsInto *commitRef `json:"foldsInto"`
		Lines     []spanJSON `json:"lines"`
	} `json:"owners"`
	Unowned []spanJSON `json:"unowned"`
	Into    *struct {
		commitRef
		Verdict string     `json:"verdict"`
		Needs   *commitRef `json:"needs"`
		Why     *string    `json:"why"`
	} `json:"into"`
	Refusal *string `json:"refusal"`
}

func owner(t *testing.T, ctx *kit.Context, path string, from, to int, into string) (ownerJSON, error) {
	t.Helper()
	var out bytes.Buffer
	err := synccmd.SyncOwner(ctx, synccmd.OwnerOptions{Lines: wtsync.LineRange{Path: path, From: from, To: to}, Into: into, JSON: true}, &out)
	testkit.ValidateJSON(t, "sync-owner", out.Bytes())
	var res ownerJSON
	if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out.String())
	}
	return res, err
}

// ownersOf is each owner as "subject (layer) lines", in the order given.
func ownersOf(res ownerJSON) []string {
	var out []string
	for _, o := range res.Owners {
		out = append(out, fmt.Sprintf("%s (%s) %v", o.Subject, o.Layer, o.Lines))
	}
	return out
}

// The newest commit on the line that changed each line owns it, in the layer
// the stack reader puts it in: a backup name on the line is no layer. Lines no
// commit of the line changed are unowned.
func TestSyncOwnerNamesTheOwnerOfEachLineAndItsLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, main, _ := ownLine(t, synccmdtest.StackYAML)
	res, err := owner(t, ctx, "pkg/door.txt", 4, 6, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"feat: body (l2) [{5 5}]", "feat: door (l1) [{4 4} {6 6}]"}
	if got := ownersOf(res); !slices.Equal(got, want) || len(res.Unowned) != 0 || res.Into != nil || res.Edited {
		t.Errorf("owners %q unowned %v, want %q", got, res.Unowned, want)
	}
	if res.Owners[0].SHA != testkit.GitOut(t, main, "rev-parse", "l2") {
		t.Errorf("owner %s, want l2's tip", res.Owners[0].SHA)
	}
	if res, err := owner(t, ctx, "other.txt", 0, 0, ""); err != nil || !slices.Equal(ownersOf(res), []string{"feat: other (tmp/own) [{1 1}]"}) {
		t.Errorf("the top's own commit: %v %q", err, ownersOf(res))
	}
	var text bytes.Buffer
	// Named from the main checkout, the path is the worktree's.
	opts := synccmd.OwnerOptions{Work: ownTmp, Lines: wtsync.LineRange{Path: "base.txt"}}
	if err := synccmd.SyncOwner(openAt(t, main), opts, &text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "no commit on the line owns lines 1-2") {
		t.Errorf("a trunk-owned range:\n%s", text.String())
	}
}

// A path is read from the caller's directory, and the answer is for the
// committed lines, saying when an edit, staged or not, is in them.
func TestSyncOwnerAnswersForTheCommittedLinesAndSaysTheyAreEdited(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, _, tmp := ownLine(t, synccmdtest.StackYAML)
	ctx := openAt(t, filepath.Join(tmp, "pkg"))
	edited := func(from, to int) bool {
		t.Helper()
		res, err := owner(t, ctx, "door.txt", from, to, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := ownersOf(res); res.Path == nil || *res.Path != "pkg/door.txt" || len(got) == 0 {
			t.Fatalf("%+v", res)
		}
		return res.Edited
	}
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "unstaged"}))
	if res, err := owner(t, ctx, "door.txt", 11, 11, "l1"); err != nil || res.Into == nil || res.Into.Verdict != "folds" {
		t.Errorf("the change asked about, not yet committed: %v %+v", err, res.Into)
	}
	if !edited(5, 5) || edited(9, 10) {
		t.Error("an unstaged edit of line 5")
	}
	testkit.GitIn(t, tmp, "add", "-A")
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body"}))
	if !edited(5, 5) {
		t.Error("a staged edit of line 5, undone in the worktree")
	}
}

// --into folds only where the real fold takes a change to those lines, and
// names the later commit that carries its context where it does not.
func TestSyncOwnerIntoSaysWhatTheRealFoldDoes(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		line          int
		verdict, need string
	}{{11, "folds", ""}, {5, "does-not-fold", "feat: body"}} {
		t.Run(tc.verdict, func(t *testing.T) {
			t.Parallel()
			ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
			before := testkit.GitOut(t, main, "for-each-ref")
			res, err := owner(t, ctx, "pkg/door.txt", tc.line, tc.line, "l1")
			if err != nil || res.Into == nil || res.Into.Verdict != tc.verdict || res.Into.Subject != "feat: door" || res.Into.Layer != "l1" {
				t.Fatalf("%v: %+v", err, res.Into)
			}
			if need := res.Into.Needs; (need == nil) != (tc.need == "") || need != nil && (need.Subject != tc.need || need.Layer != "l2") {
				t.Errorf("needs %+v, want %q", need, tc.need)
			}
			if after := testkit.GitOut(t, main, "for-each-ref"); after != before || testkit.GitOut(t, tmp, "status", "--porcelain") != "" {
				t.Errorf("the look wrote something:\n%s", after)
			}
			changed := map[int]string{5: "d5 body", tc.line: "changed"}
			testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(changed))
			testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat: door")
			opts := synccmdtest.NoAgents()
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			_, human, err := synccmdtest.StackRun(t, openAt(t, main), ownTmp, opts)
			if (err == nil) != (tc.verdict == "folds") {
				t.Fatalf("owner said %s, the run: %v\n%s", tc.verdict, err, human)
			}
			if err == nil {
				if got := testkit.GitOut(t, main, "show", "l1:pkg/door.txt"); !strings.Contains(got, "changed") {
					t.Errorf("l1 lacks the change:\n%s", got)
				}
			}
		})
	}
}

// A change its target takes clean does not fold when a commit above conflicts
// with it, as the run finds.
func TestSyncOwnerIntoSeesAConflictAboveTheTarget(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, main, tmp := ownLine(t, synccmdtest.StackYAML)
	for _, line9 := range []string{"away", "d9"} {
		testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body", 9: line9}))
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: line 9 "+line9)
	}
	res, err := owner(t, openAt(t, tmp), "pkg/door.txt", 9, 9, "l1")
	if err != nil || res.Into == nil || res.Into.Verdict != "cannot-tell" || res.Into.Why == nil || !strings.Contains(*res.Into.Why, `"feat: line 9 away"`) {
		t.Fatalf("%v %+v", err, res.Into)
	}
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body", 9: "changed"}))
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat: door")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, openAt(t, main), ownTmp, opts); err == nil {
		t.Errorf("the run folded what owner would not promise:\n%s", human)
	}
}

// A change that merges clean is not "folds" where the run refuses the fold of
// the line, or where wt cannot say what a run makes of it.
func TestSyncOwnerIntoSaysRefusedAndCannotTell(t *testing.T) {
	t.Parallel()
	skipShort(t)
	step := "defer:\n  - run: 'true'\n    paths: [pkg/**]\n    commit: \"chore: regenerate\"\n"
	for _, tc := range []struct{ name, yaml, verdict, why string }{
		{"a fold the declaration refuses", synccmdtest.StackYAML + step + "    fold: owner\n", "refused", "without each: layer"},
		{"no declaration", "", "refused", ".wt-sync.yaml"},
		{"a layer step over the file", synccmdtest.StackYAML + step + "    each: layer\n", "cannot-tell", "layer step"},
		{"a layer step over a file a commit above changes", synccmdtest.StackYAML +
			strings.Replace(step, "pkg/**", "other.txt", 1) + "    each: layer\n", "cannot-tell", "a commit above"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, _, _ := ownLine(t, tc.yaml)
			res, err := owner(t, ctx, "pkg/door.txt", 11, 11, "l1")
			if err != nil {
				t.Fatal(err)
			}
			if in := res.Into; in == nil || in.Verdict != tc.verdict || in.Why == nil || !strings.Contains(*in.Why, tc.why) {
				t.Errorf("into %+v, want %s saying %q", res.Into, tc.verdict, tc.why)
			}
		})
	}
}

// A fixup takes no fixup: --into one is refused, naming its target.
func TestSyncOwnerIntoAFixupNamesItsTarget(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body", 12: "fixed"}))
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat: door")
	res, err := owner(t, ctx, "pkg/door.txt", 12, 12, "HEAD")
	if err != nil || res.Into == nil || res.Into.Verdict != "refused" || res.Into.Why == nil || !strings.Contains(*res.Into.Why, `"feat: door": name that commit`) {
		t.Errorf("%v %+v", err, res.Into)
	}
	if len(res.Owners) != 1 || res.Owners[0].FoldsInto == nil || res.Owners[0].FoldsInto.Subject != "feat: door" {
		t.Errorf("a line a fixup owns: %+v", res.Owners)
	}
}

// A fixup that names its target by a revision the new commit would move is not
// read as it stands: the fold after the commit finds another commit, or none.
func TestSyncOwnerIntoCannotTellWhereARevisionWouldMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, main, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, tmp, "other.txt", "fixed\n")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! HEAD~0^")
	res, err := owner(t, openAt(t, tmp), "pkg/door.txt", 11, 11, "l1")
	if err != nil || res.Into == nil || res.Into.Verdict != "cannot-tell" || res.Into.Why == nil || !strings.Contains(*res.Into.Why, "HEAD~0^") {
		t.Fatalf("%v %+v", err, res.Into)
	}
	into, err := owner(t, openAt(t, tmp), "pkg/door.txt", 11, 11, "HEAD")
	if err != nil || into.Into == nil || into.Into.Verdict != "refused" || into.Into.Why == nil || !strings.Contains(*into.Into.Why, `"feat: other": name that commit`) {
		t.Errorf("--into the fixup itself: %v %+v", err, into.Into)
	}
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body", 11: "changed"}))
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat: door")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, openAt(t, main), ownTmp, opts); err == nil {
		t.Errorf("the run folded:\n%s", human)
	}
}

// What the run refuses before it folds anything is refused, with the owner
// still told: the main checkout, and a need the new commit's checks have.
func TestSyncOwnerIntoRefusesWhatTheRunRefuses(t *testing.T) {
	t.Parallel()
	skipShort(t)
	check := "each_commit:\n  - run: 'true'\n    paths: [pkg/**]\n    needs: [docker]\n"
	for _, tc := range []struct {
		name, yaml, want string
		main             bool
	}{
		{"the main checkout", synccmdtest.StackYAML, "the main checkout is not a worktree", true},
		{"an unmet need", synccmdtest.StackYAML + check, "docker", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, main, tmp := ownLine(t, tc.yaml)
			dir := tmp
			if tc.main {
				testkit.GitIn(t, main, "worktree", "remove", tmp)
				testkit.GitIn(t, main, "checkout", "-q", ownTmp)
				dir = main
			}
			var out bytes.Buffer
			opts := synccmd.OwnerOptions{Lines: wtsync.LineRange{Path: "pkg/door.txt", From: 11, To: 11}, Into: "l1", JSON: true,
				Probes: synccmdtest.DockerDown().Probes()}
			if err := synccmd.SyncOwner(openAt(t, dir), opts, &out); err != nil {
				t.Fatal(err)
			}
			var res ownerJSON
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if in := res.Into; in == nil || in.Verdict != "refused" || in.Why == nil || !strings.Contains(*in.Why, tc.want) || len(res.Owners) != 1 {
				t.Errorf("%s", out.String())
			}
		})
	}
}

// The look writes its scratch indexes without the repository's hooks, which
// run in the real worktree.
func TestSyncOwnerRunsNoHooks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, main, tmp := ownLine(t, synccmdtest.StackYAML)
	gittest.WriteExecutable(t, filepath.Join(main, ".git", "hooks", "post-index-change"), "#!/bin/sh\necho ran >> '"+filepath.Join(tmp, "other.txt")+"'\n")
	// A commit above that conflicts takes the analysis through its own scratch
	// index too.
	res, err := owner(t, openAt(t, tmp), "pkg/door.txt", 5, 5, "l1")
	if err != nil || res.Into == nil || res.Into.Subject != "feat: door" {
		t.Fatalf("%v %+v", err, res.Into)
	}
	if got := read(t, filepath.Join(tmp, "other.txt")); got != "other\n" {
		t.Errorf("a hook ran: other.txt is %q", got)
	}
}

// A directory the worktree has turned into a symlink does not lead the question
// to the file it points at.
func TestSyncOwnerAsksForTheCommittedPath(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, tmp, "elsewhere/door.txt", "e\n")
	testkit.GitIn(t, tmp, "add", "-A")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "feat: elsewhere")
	if err := os.RemoveAll(filepath.Join(tmp, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(tmp, "pkg")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"pkg/door.txt", filepath.Join(tmp, "pkg/door.txt")} {
		res, err := owner(t, ctx, path, 1, 1, "")
		if err != nil || res.Path == nil || *res.Path != "pkg/door.txt" || !slices.Equal(ownersOf(res), []string{"feat: door (l1) [{1 1}]"}) {
			t.Errorf("%s: %v %q", path, err, ownersOf(res))
		}
	}
}

// A session listing that fails and another run's lock keep the run off the
// worktree, so no change is said to fold there.
func TestSyncOwnerIntoRefusesAWorktreeTheRunStaysOff(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		name, want string
		make       func(t *testing.T, ctx *kit.Context, tmp string)
	}{
		{"sessions unknown", "cannot list agent sessions", func(_ *testing.T, ctx *kit.Context, _ string) {
			testkit.AgentsOf(ctx).List = func() ([]agents.Agent, error) { return nil, errors.New("boom") }
		}},
		{"a run's lock", "locked", func(t *testing.T, _ *kit.Context, tmp string) { holdLock(t, tmp) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
			tc.make(t, ctx, tmp)
			res, err := owner(t, ctx, "pkg/door.txt", 11, 11, "l1")
			if err != nil || res.Into == nil || res.Into.Verdict != "refused" || res.Into.Why == nil || !strings.Contains(*res.Into.Why, tc.want) || len(res.Owners) != 1 {
				t.Errorf("%v %+v", err, res.Into)
			}
		})
	}
}

// A line with a move! commit is refused with the run's words, which name the
// flag it needs.
func TestSyncOwnerIntoSaysWhatAMoveLineNeeds(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, s, _ := synccmdtest.MoveLine(t, false, true)
	res, err := owner(t, openAt(t, s.Path), "door/door.go", 1, 1, "l1")
	if err != nil || res.Into == nil || res.Into.Verdict != "refused" || res.Into.Why == nil || !strings.Contains(*res.Into.Why, "--each-commit=targets") {
		t.Errorf("%v %+v", err, res.Into)
	}
}

// A line that already reads like a probe's own replacement is still changed by
// the probe.
func TestSyncOwnerIntoChangesEveryLineItAsksAbout(t *testing.T) {
	t.Parallel()
	skipShort(t)
	_, _, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, tmp, "pkg/door.txt", doorFile(map[int]string{5: "d5 body", 11: "wt sync owner: a changed line 11"}))
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: line 11")
	res, err := owner(t, openAt(t, tmp), "pkg/door.txt", 11, 11, "l1")
	if err != nil || res.Into == nil || res.Into.Verdict != "does-not-fold" {
		t.Errorf("%v %+v", err, res.Into)
	}
}

// What the command cannot answer is refused, with nothing claimed.
func TestSyncOwnerRefusals(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, tmp, "untracked.txt", "u\n")
	for _, tc := range []struct {
		path     string
		from, to int
		into     string
		want     string
	}{
		{"untracked.txt", 0, 0, "", "not committed"},
		{"pkg", 0, 0, "", "not a file"},
		{"pkg/door.txt", 12, 13, "", "12 lines"},
		{"pkg/door.txt", 1, 1, "main", "not a commit of the line"},
	} {
		res, err := owner(t, ctx, tc.path, tc.from, tc.to, tc.into)
		if err == nil || res.Refusal == nil || !strings.Contains(*res.Refusal, tc.want) || res.Into != nil {
			t.Errorf("%+v: err %v, %+v", tc, err, res)
		}
	}
}
