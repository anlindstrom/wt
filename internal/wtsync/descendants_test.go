package wtsync

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/gittest"
)

// sideCommit puts a commit of file on a new branch name cut from at, through
// a throwaway worktree, and returns the branch's tip.
func sideCommit(t *testing.T, dir, name, at, file string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "side")
	gitIn(t, dir, "worktree", "add", "-q", "-b", name, path, at)
	gittest.WriteFile(t, filepath.Join(path, file), name+" "+file+"\n")
	gitIn(t, path, "add", "-A")
	gitIn(t, path, "commit", "-q", "--no-verify", "-m", "feat("+name+"): "+file)
	gitIn(t, dir, "worktree", "remove", "--force", path)
	return gitIn(t, dir, "rev-parse", "refs/heads/"+name)
}

func sidesByName(sides []SideBranch) map[string]SideBranch {
	out := map[string]SideBranch{}
	for _, s := range sides {
		out[s.Branch] = s
	}
	return out
}

// What hangs off a line: a branch forked mid-line, one stacked on it, one
// at the same tip, one above the line's top, and a backup. Layers, the
// line's own branch and trunk are none of them.
func TestReadSidesFindsWhatHangsOffTheLine(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	b2 := s.Tips["s2"] + "~1"
	side := sideCommit(t, s.Dir, "side", b2, "x1.txt")
	sideCommit(t, s.Dir, "side2", "side", "x2.txt")
	gitIn(t, s.Dir, "branch", "twin", side)
	sideCommit(t, s.Dir, "above", gittest.StackTmp, "y1.txt")
	sideCommit(t, s.Dir, "backup/2026-11-01/huge-redo", "side", "z1.txt")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	sides, err := ReadSides(s.Dir, line, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, x := range sides {
		order = append(order, x.Branch)
	}
	if got := strings.Join(order, " "); got != "side backup/2026-11-01/huge-redo side2 twin above" {
		t.Errorf("order %q", got)
	}
	by := sidesByName(sides)
	fork := gitIn(t, s.Dir, "rev-parse", b2)
	for name, want := range map[string]struct {
		kind, fork, on string
		count, commits int
	}{
		"side":  {SideForked, fork, "", 4, 1},
		"side2": {SideForked, fork, "side", 4, 1},
		"twin":  {SideForked, fork, "side", 4, 0},
		"above": {SideAbove, line.Head, "", 12, 1},
	} {
		got, ok := by[name]
		switch {
		case !ok:
			t.Errorf("%s not found", name)
		case got.Kind != want.kind || got.Fork != want.fork || got.On != want.on || got.Count != want.count || got.Commits != want.commits || got.Left != "":
			t.Errorf("%s: %+v, want %+v", name, got, want)
		}
	}
	backup := by["backup/2026-11-01/huge-redo"]
	if !backup.Backup || backup.Left == "" || backup.On != "side" {
		t.Errorf("backup: %+v, want a backup left where it is, on side", backup)
	}
	for _, name := range []string{"s1", "s3", "main", gittest.StackTmp} {
		if _, ok := by[name]; ok {
			t.Errorf("%s is listed as a side branch", name)
		}
	}
}

// A backup is never what a branch is stacked on: a branch above a backup of
// its own earlier tip still hangs off the line, and is carried.
func TestReadSidesNeverStacksABranchOnABackup(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	sideCommit(t, s.Dir, "safety/x-pre-fold-1", s.Tips["s1"], "x1.txt")
	sideCommit(t, s.Dir, "x", "safety/x-pre-fold-1", "x2.txt")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	sides, err := ReadSides(s.Dir, line, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	x := sidesByName(sides)["x"]
	if x.On != "" || x.Left != "" || x.Base != s.Tips["s1"] || x.Commits != 2 {
		t.Errorf("x: %+v, want carried from s1's tip with both commits", x)
	}
}

// A side branch with a merge commit of its own is listed and left: a replay
// would drop the merge.
func TestReadSidesLeavesABranchWithAMerge(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	sideCommit(t, s.Dir, "m1", s.Tips["s1"], "x1.txt")
	sideCommit(t, s.Dir, "m2", s.Tips["s1"], "x2.txt")
	path := filepath.Join(t.TempDir(), "m")
	gitIn(t, s.Dir, "worktree", "add", "-q", path, "m1")
	gitIn(t, path, "merge", "-q", "--no-edit", "--no-verify", "m2")
	gitIn(t, s.Dir, "worktree", "remove", "--force", path)
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sides, err := ReadSides(s.Dir, line, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m1 := sidesByName(sides)["m1"]; !strings.Contains(m1.Left, "merge commit") {
		t.Errorf("m1: %+v, want it left for its merge", m1)
	}
}

// The rebase of a line marks where each fork commit lands: a commit that
// survives at its rewrite, one trunk carries at the last commit below it
// that survives, and a fork whose whole layer trunk carries where the
// layer below ends. Each is checked the way a layer's marker is.
func TestRebaseMarksWhereEachForkCommitLands(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	cfg, err := LoadFromTrunk(s.Dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b2 := gitIn(t, s.Dir, "rev-parse", s.Tips["s2"]+"~1")
	forks := []string{b2, s.Tips["s2"], s.Tips["s4"]}
	req := Request{Path: s.Paths[gittest.StackTmp], Branch: gittest.StackTmp, Trunk: "origin/main", Onto: "origin/main",
		Epoch: 31, Stack: &StackRequest{Line: line, Forks: forks}}
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	if err != nil || res.Left != nil {
		t.Fatalf("err %v left %+v\n%s", err, res.Left, log.String())
	}
	if len(res.Forks) != 3 {
		t.Fatalf("fork plans %+v", res.Forks)
	}
	var got []string
	for i, p := range res.Forks {
		if p.Old != forks[i] || p.Ref != ForkMapRef(31, i+1) {
			t.Errorf("fork %d: %+v", i, p)
		}
		tip, why, err := CheckMarker(s.Dir, res.NewTip, "origin/main", p)
		if err != nil || why != "" {
			t.Fatalf("fork %d: %v %s", i, err, why)
		}
		got = append(got, gitIn(t, s.Dir, "rev-list", "--count", "origin/main.."+tip)+" "+gitIn(t, s.Dir, "log", "-1", "--format=%s", tip))
	}
	want := []string{"4 feat(s2): b2.txt", "4 feat(s2): b2.txt", "6 chore(s3): bump to 1.0.1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("forks land at:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The layers are marked as they always were.
	if _, err := CheckLayers(s.Dir, res.NewTip, "origin/main", res.Layers); err != nil {
		t.Fatal(err)
	}
	if err := DropStackMap(s.Dir, res.Forks); err != nil {
		t.Fatal(err)
	}
}

// A fold in place: a fork at a fixup! commit, which autosquash moves up
// into its target, lands where the commit below it ends, with the fixup
// folded in below.
func TestFoldMarksAForkAtAFixupWhereTheCommitBelowEnds(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML)
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "a1.txt"), "fixed\n")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s1): a1.txt")
	fixup := gitIn(t, tmp, "rev-parse", "HEAD")
	gittest.WriteFile(t, filepath.Join(tmp, "g1.txt"), "g\n")
	gitIn(t, tmp, "add", "-A")
	gitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "feat(s6): g1.txt")
	cfg, err := LoadFromTrunk(s.Dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Path: tmp, Branch: gittest.StackTmp, Trunk: "origin/main", Onto: line.Base, Epoch: 32,
		Stack: &StackRequest{Line: line, Autosquash: true, Forks: []string{fixup}}}
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	if err != nil || res.Left != nil {
		t.Fatalf("err %v left %+v\n%s", err, res.Left, log.String())
	}
	if len(res.Forks) != 1 {
		t.Fatalf("fork plans %+v, want one", res.Forks)
	}
	tip, why, err := CheckMarker(s.Dir, res.NewTip, line.Base, res.Forks[0])
	if err != nil || why != "" {
		t.Fatalf("%v %s", err, why)
	}
	if got := gitIn(t, s.Dir, "log", "-1", "--format=%s", tip); got != "feat(s5): e3.txt" {
		t.Errorf("the fork lands at %q, want s5's tip, where the commit below the fixup ends", got)
	}
	if got := gitIn(t, s.Dir, "show", tip+":a1.txt"); got != "fixed" {
		t.Errorf("a1.txt at the fork's new commit is %q, want the fixup folded in", got)
	}
}

// A side branch is replayed in the object store alone: its own commits on
// the new base, with their authors and messages, no ref and no worktree
// touched; a commit the new base already has drops, and a conflict leaves
// nothing and says where.
func TestReplaySideReplaysOnlyItsOwnCommits(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	b2 := gitIn(t, s.Dir, "rev-parse", s.Tips["s2"]+"~1")
	sideCommit(t, s.Dir, "side", b2, "x1.txt")
	tip := sideCommit(t, s.Dir, "side-more", "side", "x2.txt")
	newBase := gitIn(t, s.Dir, "rev-parse", "origin/main")
	got, conflict, err := ReplaySide(s.Dir, s.Base, tip, newBase)
	if err != nil || conflict != "" {
		t.Fatalf("%v %s", err, conflict)
	}
	if n := gitIn(t, s.Dir, "rev-list", "--count", newBase+".."+got); n != "6" {
		t.Errorf("%s commits on the new base, want the 6 of the line below b2's fork and the side's", n)
	}
	if a, b := gitIn(t, s.Dir, "log", "-1", "--format=%an %ae %ad %B", tip), gitIn(t, s.Dir, "log", "-1", "--format=%an %ae %ad %B", got); a != b {
		t.Errorf("author or message changed: %q, now %q", a, b)
	}
	if gitIn(t, s.Dir, "rev-parse", "refs/heads/side-more") != tip {
		t.Error("the branch moved")
	}
	// x1.txt on a base that already has it, the same: dropped.
	path := filepath.Join(t.TempDir(), "same")
	gitIn(t, s.Dir, "worktree", "add", "-q", "--detach", path, s.Base)
	gitIn(t, path, "cherry-pick", "side")
	same := gitIn(t, path, "rev-parse", "HEAD")
	got, conflict, err = ReplaySide(s.Dir, b2, tip, same)
	if err != nil || conflict != "" {
		t.Fatalf("%v %s", err, conflict)
	}
	if n := gitIn(t, s.Dir, "rev-list", "--count", same+".."+got); n != "1" {
		t.Errorf("%s commits above the base, want x1.txt dropped, x2.txt kept", n)
	}
	// x2.txt with other content on the base: a conflict.
	other := sideCommit(t, s.Dir, "x2", s.Base, "x2.txt")
	got, conflict, err = ReplaySide(s.Dir, "side", tip, other)
	if err != nil || got != "" || !strings.Contains(conflict, "x2.txt") {
		t.Errorf("got %q conflict %q err %v, want a conflict in x2.txt", got, conflict, err)
	}
}
