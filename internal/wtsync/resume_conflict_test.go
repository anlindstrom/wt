package wtsync

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// resumeConflictLine is a stack held on one line with two fixups that each
// conflict when folded into their targets, two picks apart: a1's fixup, then
// a2 and b1 (clean), then b1's fixup. The declaration checks every commit
// touching a2.txt or b1.txt, logging the subject it ran at.
func resumeConflictLine(t *testing.T, paths string, fixupFile bool) (s *gittest.StackLine, cfg *Config, req Request, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "checks.log")
	yaml := stackYAML + "each_commit:\n  - run: git log -1 --format=%s >> '" + logPath + "'\n    paths: [" + paths + "]\n"
	s = stackLine(t, yaml)
	// Trunk moved a1.txt too: the first stop is a plain pick's conflict.
	gittest.WriteFile(t, filepath.Join(s.Dir, "a1.txt"), "trunk a1\n")
	gitIn(t, s.Dir, "add", "a1.txt")
	gitIn(t, s.Dir, "commit", "-q", "--no-verify", "-m", "feat: trunk a1")
	gitIn(t, s.Dir, "fetch", "-q", "origin")
	tmp := s.Paths[gittest.StackTmp]
	commit := func(msg string, files map[string]string) {
		for f, c := range files {
			gittest.WriteFile(t, filepath.Join(tmp, f), c)
		}
		gitIn(t, tmp, "add", "-A")
		gitIn(t, tmp, "commit", "-q", "--no-verify", "-m", msg)
	}
	commit("feat(s6): mid a1", map[string]string{"a1.txt": "s1 a1.txt\nmid1\n"})
	commit("feat(s6): mid b1", map[string]string{"b1.txt": "s2 b1.txt\nmid2\n"})
	fx := map[string]string{"a1.txt": "s1 a1.txt FIXED\nmid1\n"}
	if fixupFile {
		fx["fx.txt"] = "fx\n"
	}
	commit("fixup! feat(s1): a1.txt", fx)
	commit("fixup! feat(s2): b1.txt", map[string]string{"b1.txt": "s2 b1.txt FIXED\nmid2\n"})
	var err error
	if cfg, err = LoadFromTrunk(s.Dir, "main"); err != nil {
		t.Fatal(err)
	}
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req = Request{Path: tmp, Branch: gittest.StackTmp, Trunk: "origin/main", Onto: "origin/main", Epoch: 31,
		Stack: &StackRequest{Line: line, Autosquash: true}}
	return s, cfg, req, logPath
}

// resolveWith writes content over path in dir and stages it, as a person
// resolving a stop does.
func resolveWith(t *testing.T, dir, path, content string) {
	t.Helper()
	gittest.WriteFile(t, filepath.Join(dir, path), content)
	gitIn(t, dir, "add", path)
}

// A resume that runs into a new conflict hands it over like any other stop.
func TestResumeThatMeetsANewConflictAtAFixupStopsLikeAnyOtherStop(t *testing.T) {
	t.Parallel()
	s, cfg, req, logPath := resumeConflictLine(t, "a2.txt, b1.txt", false)
	tmp := req.Path
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	if err != nil || res.Left == nil {
		t.Fatalf("first run: err %v left %+v\n%s", err, res.Left, log.String())
	}
	if res.Left.Index != 1 {
		t.Fatalf("first stop at %d, want 1\n%s", res.Left.Index, log.String())
	}
	resolveWith(t, tmp, "a1.txt", "s1 a1.txt\n")
	old := res.OldTip
	req.Resumed = res.LayerState
	var rlog bytes.Buffer
	res2, err := Resume(s.Dir, cfg, req, old, res.Safety, &rlog)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, rlog.String())
	}
	if res2.Left == nil {
		t.Fatalf("the resume did not stop at the second fixup:\n%s", rlog.String())
	}
	if res2.Left.Subject != "fixup! feat(s1): a1.txt" {
		t.Errorf("stopped at %q", res2.Left.Subject)
	}
	if strings.Contains(rlog.String(), "no record") {
		t.Errorf("log: %s", rlog.String())
	}
	// The first resume's stop is recorded as the new stop: the rebase sits
	// at the fixup, and a second resume goes on from there.
	resolveWith(t, tmp, "a1.txt", "s1 a1.txt FIXED\nmid1\n")
	req.Resumed = res2.LayerState
	res3, err := Resume(s.Dir, cfg, req, old, res2.Safety, &rlog)
	if err != nil {
		t.Fatalf("second resume: %v\n%s", err, rlog.String())
	}
	if res3.Left == nil || res3.Left.Subject != "fixup! feat(s2): b1.txt" {
		t.Fatalf("the second resume did not stop at the b1 fixup: %+v\n%s", res3.Left, rlog.String())
	}
	// The rest is conflicts too, since the commits after a fixup's target
	// were written against the unfixed one: the person ends each with the
	// line's final file.
	final := map[string]string{"a1.txt": "s1 a1.txt FIXED\nmid1\n", "b1.txt": "s2 b1.txt FIXED\nmid2\n"}
	last := res3
	for n := 0; last.Left != nil; n++ {
		if n > 6 {
			t.Fatalf("still stopping after %d resumes at %+v", n, last.Left)
		}
		for _, f := range last.Left.Left {
			resolveWith(t, tmp, f, final[f])
		}
		req.Resumed = last.LayerState
		if last, err = Resume(s.Dir, cfg, req, old, last.Safety, &rlog); err != nil {
			t.Fatalf("resume: %v\n%s", err, rlog.String())
		}
	}
	// Each commit that touches a listed path is checked: a2 where the first
	// resume passed it, b1 once its fixup is in.
	if got, want := strings.Join(checkedSubjects(t, logPath), " "), "feat(s1): a2.txt feat(s2): b1.txt"; got != want {
		t.Errorf("checked at %q, want %q\n%s", got, want, rlog.String())
	}
	for f, want := range final {
		if got := gitIn(t, tmp, "show", "HEAD:"+f); got+"\n" != want {
			t.Errorf("%s is %q, want %q", f, got, want)
		}
	}
}

// Only the fixup touches the checked path, not the pick it folds into: the
// group the resume completes is still checked once.
func TestResumeChecksAGroupWhoseOnlyTheFixupTouchesTheCheckedPath(t *testing.T) {
	t.Parallel()
	s, cfg, req, logPath := resumeConflictLine(t, "fx.txt", true)
	tmp := req.Path
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	final := map[string]string{"a1.txt": "s1 a1.txt FIXED\nmid1\n", "b1.txt": "s2 b1.txt FIXED\nmid2\n"}
	for n := 0; err == nil && res.Left != nil; n++ {
		if n > 8 {
			t.Fatalf("still stopping after %d resumes at %+v", n, res.Left)
		}
		for _, f := range res.Left.Left {
			content := final[f]
			if n == 0 {
				// The pick's own conflict, resolved to what the fixup conflicts with.
				content = "s1 a1.txt\n"
			}
			resolveWith(t, tmp, f, content)
		}
		req.Resumed = res.LayerState
		res, err = Resume(s.Dir, cfg, req, res.OldTip, res.Safety, &log)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if got, want := strings.Join(checkedSubjects(t, logPath), " "), "feat(s1): a1.txt"; got != want {
		t.Errorf("checked at %q, want %q\n%s", got, want, log.String())
	}
}

// The subject of the last command run, in both forms git writes a list line
// in: with the subject after " # ", and as the words after the id.
func TestLastDoneSubjectReadsBothFormsOfAListLine(t *testing.T) {
	t.Parallel()
	want := map[string]string{"hash in it": "fixup! feat: one # note"}
	for _, n := range []string{"comment form", "plain form", "with -C"} {
		want[n] = "fixup! feat: one"
	}
	for name, done := range map[string]string{
		"comment form": "pick abc123 # feat: one\nfixup abc124 # fixup! feat: one\n",
		"plain form":   "pick abc123 feat: one\nfixup abc124 fixup! feat: one\n",
		"with -C":      "pick abc123 feat: one\nfixup -C abc124 fixup! feat: one\n",
		"hash in it":   "pick abc123 feat: one\nfixup abc124 fixup! feat: one # note\n",
	} {
		if got := lastDoneSubject(done); got != want[name] {
			t.Errorf("%s: %q", name, got)
		}
	}
}
