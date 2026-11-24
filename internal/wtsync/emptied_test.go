package wtsync

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// keepTrunkYAML makes the line's version bump come out empty: v.txt's
// owned line is resolved to trunk's side.
const keepTrunkYAML = "conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: keep-trunk\n"

// droppedOf is the subjects of a list with their layers, "subject [layer]".
func droppedOf(ds []Dropped) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, strings.TrimSpace(d.Subject+" ["+d.Layer+"]"))
	}
	return out
}

// rebaseLine rebases the fixture's line onto origin/main, as a stack with
// its layers or in place.
func rebaseLine(t *testing.T, s *gittest.StackLine, stack, autosquash bool, epoch int64) Result {
	t.Helper()
	cfg, err := LoadFromTrunk(s.Dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Path: s.Paths[gittest.StackTmp], Branch: gittest.StackTmp, Trunk: "origin/main", Onto: "origin/main", Epoch: epoch}
	if stack {
		line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Stack = &StackRequest{Line: line, Autosquash: autosquash}
	}
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	if err != nil || res.Left != nil {
		t.Fatalf("err %v left %+v\n%s", err, res.Left, log.String())
	}
	return res
}

// A bump that trunk made too, resolved to trunk's side, leaves its pick
// empty: git drops it, and the result names it.
func TestFinishedRebaseNamesABumpResolvedToTrunksSide(t *testing.T) {
	t.Parallel()
	s := stackLine(t, keepTrunkYAML)
	res := rebaseLine(t, s, false, false, 51)
	if !slices.Contains(droppedOf(res.Emptied), "chore(s3): bump to 1.0.1 []") {
		t.Fatalf("emptied %q does not name the bump", droppedOf(res.Emptied))
	}
}

// A commit trunk already carries patch for patch is left out of the list by
// git; the result names it all the same.
func TestFinishedRebaseNamesCommitsTrunkAlreadyHas(t *testing.T) {
	t.Parallel()
	s := stackLine(t, keepTrunkYAML)
	res := rebaseLine(t, s, false, false, 52)
	want := []string{"feat(s2): b3.txt []", "chore(s3): bump to 1.0.1 []", "feat(s4): d1.txt []", "feat(s4): d2.txt []"}
	if got := droppedOf(res.Emptied); !slices.Equal(got, want) {
		t.Fatalf("emptied %q, want %q", got, want)
	}
	for _, d := range res.Emptied {
		if d.Unmatched || len(d.Commit) != 40 {
			t.Errorf("%+v: want a full id of the original and a match", d)
		}
	}
	if len(res.TestsOnly) != 0 {
		t.Errorf("testsOnly %q without test_paths", droppedOf(res.TestsOnly))
	}
}

// A fixup that autosquash folds into its target is meant to vanish: it is
// not named, though the commits trunk has are.
func TestFinishedRebaseDoesNotNameAFoldedFixup(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	res := rebaseLine(t, s, true, true, 53)
	want := []string{"feat(s2): b3.txt [s2]", "feat(s4): d1.txt [s4]", "feat(s4): d2.txt [s4]"}
	if got := droppedOf(res.Emptied); !slices.Equal(got, want) {
		t.Fatalf("emptied %q, want %q", got, want)
	}
}

// A stack of layers names the layer of each commit: the bump is s3's, and
// the commits trunk has are s2's and s4's.
func TestFinishedStackNamesTheLayerOfEachDroppedCommit(t *testing.T) {
	t.Parallel()
	s := stackLine(t, keepTrunkYAML)
	res := rebaseLine(t, s, true, false, 54)
	want := []string{"feat(s2): b3.txt [s2]", "chore(s3): bump to 1.0.1 [s3]", "feat(s4): d1.txt [s4]", "feat(s4): d2.txt [s4]"}
	if got := droppedOf(res.Emptied); !slices.Equal(got, want) {
		t.Fatalf("emptied %q, want %q", got, want)
	}
}

// A commit above every layer is the line branch's own.
func TestFinishedStackNamesTheLineForACommitAboveEveryLayer(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "extra.txt"), "extra\n")
	gitIn(t, tmp, "add", "-A")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "feat: extra")
	// Trunk already has it, patch for patch, so git leaves it out.
	gitIn(t, s.Dir, "cherry-pick", gitIn(t, tmp, "rev-parse", "HEAD"))
	gitIn(t, s.Dir, "fetch", "-q", "origin")
	res := rebaseLine(t, s, true, false, 55)
	if !slices.Contains(droppedOf(res.Emptied), "feat: extra [tmp/stack]") {
		t.Fatalf("emptied %q does not name the commit above every layer", droppedOf(res.Emptied))
	}
}

// With test_paths, a commit whose replay kept only test paths, though the
// original changed others too, is named.
func TestFinishedRebaseNamesACommitReplayedAsTestsOnly(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML+"test_paths: ['**/*_test.txt', '*_test.txt']\n")
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "src.txt"), "the fix\n")
	gittest.WriteFile(t, filepath.Join(tmp, "src_test.txt"), "tests the fix\n")
	gitIn(t, tmp, "add", "-A")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fix: the fix")
	// Trunk makes the same change to src.txt on its own.
	gittest.WriteFile(t, filepath.Join(s.Dir, "src.txt"), "the fix\n")
	gitIn(t, s.Dir, "add", "-A")
	gitIn(t, s.Dir, "commit", "-q", "--no-verify", "-m", "fix: the fix, trunk's")
	gitIn(t, s.Dir, "fetch", "-q", "origin")
	res := rebaseLine(t, s, false, false, 56)
	if got, want := droppedOf(res.TestsOnly), []string{"fix: the fix []"}; !slices.Equal(got, want) {
		t.Fatalf("testsOnly %q, want %q", got, want)
	}
	if slices.Contains(droppedOf(res.Emptied), "fix: the fix []") {
		t.Errorf("a commit that left a change is not emptied: %q", droppedOf(res.Emptied))
	}
}

// Writing test_paths changes what is reported and nothing the run did: the
// refs, the tree and the status come out the same, and reporting again on
// the finished result writes nothing.
func TestReportingWritesNothing(t *testing.T) {
	t.Parallel()
	state := func(s *gittest.StackLine, res Result) string {
		tmp := s.Paths[gittest.StackTmp]
		refs := gitIn(t, s.Dir, "for-each-ref", "--format=%(refname)")
		// The declaration is on trunk, so it is in the tree: leave it out.
		var files []string
		for _, l := range strings.Split(gitIn(t, tmp, "ls-tree", "-r", res.NewTip), "\n") {
			if !strings.HasSuffix(l, ".wt-sync.yaml") {
				files = append(files, l)
			}
		}
		return refs + "\n--\n" + strings.Join(files, "\n") + "\n--\n" +
			gitIn(t, tmp, "status", "--porcelain") + "\n--\n" + gitIn(t, s.Dir, "stash", "list")
	}
	without := stackLine(t, keepTrunkYAML)
	with := stackLine(t, keepTrunkYAML+"test_paths: ['**/*_test.txt']\n")
	a := rebaseLine(t, without, true, false, 57)
	b := rebaseLine(t, with, true, false, 57)
	if state(without, a) != state(with, b) {
		t.Fatalf("test_paths changed the run:\n%s\nvs\n%s", state(without, a), state(with, b))
	}
	if !slices.Equal(droppedOf(a.Emptied), droppedOf(b.Emptied)) {
		t.Errorf("emptied differs: %q vs %q", droppedOf(a.Emptied), droppedOf(b.Emptied))
	}
	before := state(with, b)
	line, err := ReadLine(with.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err == nil {
		_, err = ReportReplay(with.Paths[gittest.StackTmp], ReplayInput{Cutoff: "origin/main", Old: with.Tips[gittest.StackTmp], Onto: "origin/main",
			New: b.NewTip, TestPaths: []string{"**/*_test.txt"}, Line: &line})
	}
	if err != nil {
		t.Fatal(err)
	}
	if after := state(with, b); after != before {
		t.Fatalf("reporting wrote something:\n%s\nvs\n%s", before, after)
	}
}

// Two commits of one author, date and subject, of which one came out: which
// one is not known, so both are named as unmatched rather than guessed at.
func TestAnAmbiguousMatchIsListedAsUnmatched(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "repo")
	write := func(name string) {
		gittest.WriteFile(t, filepath.Join(dir, name), name+"\n")
		gitIn(t, dir, "add", "-A")
	}
	write("base.txt")
	gitIn(t, dir, "commit", "-q", "--no-verify", "-m", "base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	same := func(name string) {
		write(name)
		gitIn(t, dir, "-c", "user.name=A", "-c", "user.email=a@example.com", "commit", "-q", "--no-verify", "--date=2020-01-01T00:00:00Z", "--author=A <a@example.com>", "-m", "same subject")
	}
	same("one.txt")
	first := gitIn(t, dir, "rev-parse", "HEAD")
	same("two.txt")
	old := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "--detach", base)
	gitIn(t, dir, "cherry-pick", first)
	got, err := ReportReplay(dir, ReplayInput{Cutoff: base, Old: old, Onto: base, New: gitIn(t, dir, "rev-parse", "HEAD")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Emptied) != 2 || !got.Emptied[0].Unmatched || !got.Emptied[1].Unmatched {
		t.Fatalf("emptied %+v, want both commits unmatched", got.Emptied)
	}
}

func TestTestPathsAreParsed(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte("test_paths: ['**/*Test.java', 'src/test/**']\n"))
	if err != nil || !slices.Equal(cfg.TestPaths, []string{"**/*Test.java", "src/test/**"}) {
		t.Fatalf("%+v, %v", cfg, err)
	}
	if _, err := Parse([]byte("test_paths: ['']\n")); err == nil || !strings.Contains(err.Error(), "test_paths[0]") {
		t.Fatalf("err %v, want test_paths[0] refused", err)
	}
}

// An amend! folded into its target changes the target's subject: both
// commits survive, so the target is not a drop and the amend! is not named.
func TestFinishedRebaseDoesNotNameATargetWhoseAmendChangedItsSubject(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "amend! feat(s2): b1.txt", "-m", "feat(s2): b1 renamed")
	res := rebaseLine(t, s, true, true, 58)
	want := []string{"feat(s2): b3.txt [s2]", "feat(s4): d1.txt [s4]", "feat(s4): d2.txt [s4]"}
	if got := droppedOf(res.Emptied); !slices.Equal(got, want) {
		t.Fatalf("emptied %q, want %q", got, want)
	}
}

// A fixup git left out because trunk carries its patch is dropped, not
// folded: the result names it.
func TestFinishedRebaseNamesAFixupGitLeftOutBecauseTrunkCarriesIt(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "d1.txt"), "s4 d1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s4): d1.txt")
	gitIn(t, s.Dir, "cherry-pick", gitIn(t, tmp, "rev-parse", "HEAD"))
	gitIn(t, s.Dir, "fetch", "-q", "origin")
	res := rebaseLine(t, s, true, true, 59)
	if !slices.Contains(droppedOf(res.Emptied), "fixup! feat(s4): d1.txt [tmp/stack]") {
		t.Fatalf("emptied %q does not name the fixup trunk carries", droppedOf(res.Emptied))
	}
}

// Without a record of the rebase's own plan, an original whose exact key is
// missing but whose author and date, or author and subject, a commit of the
// result still has, is not called a drop: it is unmatched.
func TestWithoutARecordAChangedKeyIsUnmatchedNotEmptied(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		change    []string
		unmatched bool
	}{
		"subject changed": {[]string{"commit", "-q", "--amend", "--no-verify", "-m", "renamed"}, true},
		"date changed":    {[]string{"commit", "-q", "--amend", "--no-verify", "--no-edit", "--date=2021-02-03T04:05:06Z"}, true},
		"nothing like it": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := gittest.NewRepo(t, parent, "repo")
			gittest.WriteFile(t, filepath.Join(dir, "base.txt"), "base\n")
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "--no-verify", "-m", "base")
			base := gitIn(t, dir, "rev-parse", "HEAD")
			gittest.WriteFile(t, filepath.Join(dir, "x.txt"), "x\n")
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "--no-verify", "--date=2020-01-01T00:00:00Z", "-m", "feat: x")
			old := gitIn(t, dir, "rev-parse", "HEAD")
			gitIn(t, dir, "checkout", "-q", "--detach", base)
			if tc.change != nil {
				gitIn(t, dir, "cherry-pick", old)
				gitIn(t, dir, tc.change...)
			}
			got, err := ReportReplay(dir, ReplayInput{Cutoff: base, Old: old, Onto: base, New: gitIn(t, dir, "rev-parse", "HEAD")})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Emptied) != 1 || got.Emptied[0].Unmatched != tc.unmatched {
				t.Fatalf("emptied %+v, want one with unmatched %v", got.Emptied, tc.unmatched)
			}
		})
	}
}

// With no record of the plan, an amend! in the range may have changed any
// subject: a commit whose key is missing is unmatched. With the plan, a
// fold that happened is left out, and one that did not is named.
func TestWithoutARecordAFoldCommitIsUnmatchedAndWithOneItIsExact(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := line.Head
	res := rebaseLine(t, s, true, true, 60)
	in := ReplayInput{Cutoff: "origin/main", Old: old, Onto: "origin/main", New: res.NewTip, Line: &line}
	got, err := ReportReplay(tmp, in)
	if err != nil {
		t.Fatal(err)
	}
	var fixup *Dropped
	for i := range got.Emptied {
		if strings.HasPrefix(got.Emptied[i].Subject, "fixup! ") {
			fixup = &got.Emptied[i]
		}
	}
	if fixup == nil || !fixup.Unmatched {
		t.Fatalf("emptied %+v: the fixup, which a plan would show folded, must be unmatched without one", got.Emptied)
	}
	in.Plan = res.Replay
	if got, err = ReportReplay(tmp, in); err != nil {
		t.Fatal(err)
	}
	for _, d := range got.Emptied {
		if strings.HasPrefix(d.Subject, "fixup! ") {
			t.Errorf("with the plan, the folded fixup is named: %+v", d)
		}
	}
}

// Two originals of one author and subject whose dates differ, one of which
// came out with the other's date, both survive under one key: the one left
// without a partner is not called dropped, it is unmatched.
func TestASurplusUnderOneKeyLeavesTheOriginalWithoutAPartnerUnmatched(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "repo")
	gittest.WriteFile(t, filepath.Join(dir, "base.txt"), "base\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "--no-verify", "-m", "base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	same := func(file, date string) string {
		gittest.WriteFile(t, filepath.Join(dir, file), file+"\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "--no-verify", "--date="+date, "-m", "feat: same subject")
		return gitIn(t, dir, "rev-parse", "HEAD")
	}
	a := same("a.txt", "2020-01-01T00:00:00Z")
	b := same("b.txt", "2020-01-02T00:00:00Z")
	old := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "--detach", base)
	gitIn(t, dir, "cherry-pick", a)
	// A is amended during a handover to B's date.
	gitIn(t, dir, "commit", "-q", "--amend", "--no-verify", "--no-edit", "--date=2020-01-02T00:00:00Z")
	gitIn(t, dir, "cherry-pick", b)
	got, err := ReportReplay(dir, ReplayInput{Cutoff: base, Old: old, Onto: base, New: gitIn(t, dir, "rev-parse", "HEAD"), Plan: &ReplayPlan{Folds: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Emptied) != 1 || got.Emptied[0].Commit != a || !got.Emptied[0].Unmatched {
		t.Fatalf("emptied %+v, want A alone, unmatched", got.Emptied)
	}
}

// A fold the plan lists is hidden only when it is confirmed: a plan that was
// not carried out start to finish by wt is checked against the result, and a
// fixup whose change the result lacks is unmatched.
func TestAPlannedFoldIsHiddenOnlyWhenItsChangeIsInTheResult(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	fixup := gitIn(t, tmp, "rev-parse", "HEAD")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := line.Head
	res := rebaseLine(t, s, true, true, 61)
	if res.Replay == nil || !res.Replay.Confirmed || !slices.Contains(res.Replay.Folds, fixup) {
		t.Fatalf("plan %+v: a rebase wt drove from start to end confirms its folds", res.Replay)
	}
	in := ReplayInput{Cutoff: "origin/main", Old: old, Onto: "origin/main", New: res.NewTip, Line: &line,
		Plan: &ReplayPlan{Folds: res.Replay.Folds}}
	named := func(rep ReplayReport) string {
		for _, d := range rep.Emptied {
			if d.Commit == fixup {
				return "unmatched=" + map[bool]string{true: "true", false: "false"}[d.Unmatched]
			}
		}
		return "hidden"
	}
	got, err := ReportReplay(tmp, in)
	if err != nil {
		t.Fatal(err)
	}
	if n := named(got); n != "unmatched=true" {
		t.Errorf("an unconfirmed fold whose change is in the result: %s, want unmatched", n)
	}
	in.Plan = res.Replay
	if got, err = ReportReplay(tmp, in); err != nil {
		t.Fatal(err)
	}
	if n := named(got); n != "hidden" {
		t.Errorf("a confirmed fold: %s, want hidden", n)
	}
	in.Plan = &ReplayPlan{Folds: res.Replay.Folds}
	// The same plan against a result that lacks the change: the fixup was
	// skipped.
	// The commit before b1's replay has no b1.txt, so not the fixup's change.
	in.New = gitIn(t, tmp, "log", "-1", "--format=%H", "--grep=^feat(s2): b1.txt$", res.NewTip) + "^"
	if got, err = ReportReplay(tmp, in); err != nil {
		t.Fatal(err)
	}
	if n := named(got); n != "unmatched=true" {
		t.Errorf("a fold whose change is not in the result: %s, want unmatched", n)
	}
}

// A read made only for the report, and failing, does not change the rebase:
// it is noted, the plan is unknown, and the folds come out unmatched.
func TestAFailingReportReadDoesNotChangeTheRebase(t *testing.T) {
	t.Parallel()
	run := func(fail bool) (Result, string, string) {
		s := stackLine(t, stackYAML)
		tmp := s.Paths[gittest.StackTmp]
		gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
		gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
		cfg, err := LoadFromTrunk(s.Dir, "main")
		if err != nil {
			t.Fatal(err)
		}
		line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := Request{Path: tmp, Branch: gittest.StackTmp, Trunk: "origin/main", Onto: "origin/main", Epoch: 62,
			Stack: &StackRequest{Line: line, Autosquash: true}}
		if fail {
			req.planReplay = func(string, Line, string) (*ReplayPlan, error) { return nil, errors.New("a read failed") }
		}
		var log bytes.Buffer
		res, err := Rebase(s.Dir, cfg, req, &log)
		if err != nil || res.Left != nil || res.Restored {
			t.Fatalf("fail=%v: err %v restored %v left %+v\n%s", fail, err, res.Restored, res.Left, log.String())
		}
		return res, gitIn(t, tmp, "rev-parse", res.NewTip+"^{tree}"), log.String()
	}
	good, goodTree, _ := run(false)
	bad, badTree, log := run(true)
	if badTree != goodTree || bad.Replayed != good.Replayed || len(bad.Layers) != len(good.Layers) {
		t.Errorf("the rebase changed: tree %s vs %s, replayed %d vs %d", badTree, goodTree, bad.Replayed, good.Replayed)
	}
	if !strings.Contains(log, "note:") {
		t.Errorf("no note in the log:\n%s", log)
	}
	if bad.Replay != nil {
		t.Errorf("plan %+v, want unknown", bad.Replay)
	}
	found := false
	for _, d := range bad.Emptied {
		if strings.HasPrefix(d.Subject, "fixup! ") {
			found = d.Unmatched
		}
	}
	if !found {
		t.Errorf("emptied %+v: the fixup must be unmatched under an unknown plan", bad.Emptied)
	}
}

// A fold whose fixup changes no file (a message-only amend!) leaves no
// patch to find in the result: it is hidden when wt carried the rebase out
// start to finish, and unmatched otherwise.
func TestAFoldWithAnEmptyDiffIsHiddenOnlyWhenWtDroveTheRebase(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gitIn(t, tmp, "commit", "-q", "--no-verify", "--allow-empty", "-m", "amend! feat(s2): b1.txt", "-m", "feat(s2): b1 renamed")
	reword := gitIn(t, tmp, "rev-parse", "HEAD")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := line.Head
	res := rebaseLine(t, s, true, true, 63)
	if res.Replay == nil || !res.Replay.Confirmed || !slices.Contains(res.Replay.Folds, reword) {
		t.Fatalf("plan %+v: want the reword listed as folded and confirmed", res.Replay)
	}
	listed := func(plan *ReplayPlan) (found, unmatched bool) {
		rep, err := ReportReplay(tmp, ReplayInput{Cutoff: "origin/main", Old: old, Onto: "origin/main", New: res.NewTip, Line: &line, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range rep.Emptied {
			if d.Commit == reword {
				return true, d.Unmatched
			}
		}
		return false, false
	}
	if found, _ := listed(res.Replay); found {
		t.Error("a confirmed fold with an empty diff is named")
	}
	if found, unmatched := listed(&ReplayPlan{Folds: res.Replay.Folds}); !found || !unmatched {
		t.Errorf("an unconfirmed fold with an empty diff: found %v unmatched %v, want unmatched", found, unmatched)
	}
}
