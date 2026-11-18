package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// dropsRepo is a line whose fixup, written at its top, adds A and B to
// a.txt below ctx, a line only the later "feat: ctx" adds, and changes
// img.bin, a binary file. after makes the commits above the fixup. The fold
// is run with git's own autosquash and stops at the fixup; the returned
// worktree is there, with the line's old tip and the fixup's id.
func dropsRepo(t *testing.T, after func(write func(path, content, subject string), git func(args ...string))) (dir, oldTip, fixup string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	write := func(path, content, subject string) {
		gittest.WriteFile(t, filepath.Join(dir, path), content)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject)
	}
	gittest.WriteFile(t, filepath.Join(dir, "img.bin"), "PNG\x00\x01old\x00")
	write("a.txt", "x\n", "base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	write("a.txt", "x\nt1\n", "feat: t1")
	write("a.txt", "x\nt1\nctx\n", "feat: ctx")
	gittest.WriteFile(t, filepath.Join(dir, "img.bin"), "PNG\x00\x02new\x00")
	write("a.txt", "x\nt1\nctx\nA\nB\n", "fixup! feat: t1")
	fixup = gitIn(t, dir, "rev-parse", "HEAD")
	if after != nil {
		after(write, func(args ...string) { gitIn(t, dir, args...) })
	}
	oldTip = gitIn(t, dir, "rev-parse", "HEAD")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", base); err == nil {
		t.Fatalf("the fold did not stop at the fixup:\n%s", out)
	}
	return dir, oldTip, fixup
}

// resolve stages content as the stop's a.txt.
func resolve(t *testing.T, dir, content string) {
	t.Helper()
	gittest.WriteFile(t, filepath.Join(dir, "a.txt"), content)
	gitIn(t, dir, "add", "a.txt")
}

// A later commit that rewrites A but not B leaves B a loss when the
// resolution drops both: the old tip still has B, and nothing to come adds
// it. A is not, since the old tip no longer has it.
func TestResolutionDropsCountsEachLineAPartialRewriteLeaves(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, func(write func(string, string, string), _ func(...string)) {
		write("a.txt", "x\nt1\nctx\nA2\nB\n", "feat: A2")
	})
	resolve(t, dir, "x\nt1\n")
	drops, _, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "a.txt" || drops[0].Lines != "5" || drops[0].N != 1 {
		t.Errorf("drops %+v, want B alone, a.txt:5", drops)
	}
}

// A and B kept, with a line of the resolution's own between them, are not
// dropped: each line is there.
func TestResolutionDropsKeepsLinesTheResolutionSetsApart(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, nil)
	resolve(t, dir, "x\nt1\nA\nsep\nB\n")
	drops, unchecked, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil || len(drops) != 0 || len(unchecked) != 0 {
		t.Errorf("drops %+v, unchecked %q, err %v; want none", drops, unchecked, err)
	}
}

// A commit after the fixup renames a.txt to b.txt: the fixup's lines are
// looked for in the old tip's b.txt, and B dropped is named there.
func TestResolutionDropsFollowsALaterRename(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, func(_ func(string, string, string), git func(...string)) {
		git("mv", "a.txt", "b.txt")
		git("commit", "-q", "-m", "refactor: a.txt is b.txt")
	})
	resolve(t, dir, "x\nt1\nA\n")
	drops, _, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "b.txt" || drops[0].Lines != "5" || drops[0].N != 1 {
		t.Errorf("drops %+v, want B, as b.txt:5 in the old tip", drops)
	}
}

// A file the old tip no longer has under any name it can follow is not
// passed: it is named as not checked.
func TestResolutionDropsNamesWhatItCannotFollow(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, func(write func(string, string, string), git func(...string)) {
		git("rm", "-q", "a.txt")
		write("c.txt", "something else entirely\n", "refactor: no more a.txt")
	})
	resolve(t, dir, "x\nt1\n")
	drops, unchecked, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 0 || len(unchecked) != 1 || !strings.Contains(unchecked[0], "a.txt") {
		t.Errorf("drops %+v, unchecked %q; want a.txt named as not checked", drops, unchecked)
	}
}

// img.bin, a binary change of the fixup, taken back to the target's
// version is a drop: the old tip has the fixup's blob, and nothing to come
// writes it.
func TestResolutionDropsSeesABinaryChangeDropped(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, nil)
	resolve(t, dir, "x\nt1\nA\nB\n")
	gitIn(t, dir, "checkout", "HEAD", "--", "img.bin")
	drops, _, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "img.bin" || !drops[0].Binary {
		t.Errorf("drops %+v, want img.bin's binary change", drops)
	}
	if line := DropsLine(drops, fixup, "fixup! feat: t1"); !strings.Contains(line, "img.bin (binary)") {
		t.Errorf("line %q does not name img.bin", line)
	}
}

// plainRepo is a repository with a write that commits one file and a git
// that runs in it.
func plainRepo(t *testing.T) (dir string, write func(path, content, subject string), git func(args ...string) string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	git = func(args ...string) string { return gitIn(t, dir, args...) }
	write = func(path, content, subject string) {
		gittest.WriteFile(t, filepath.Join(dir, path), content)
		git("add", "-A")
		git("commit", "-q", "-m", subject)
	}
	return dir, write, git
}

// foldStop folds dir's branch above base with git's own autosquash, onto
// onto when it is not empty, and checks it stopped.
func foldStop(t *testing.T, dir, base, onto string) {
	t.Helper()
	args := []string{"rebase", "-q", "-i", "--autosquash"}
	if onto != "" {
		args = append(args, "--onto", onto)
	}
	if out, err := gittest.Try(t, dir, append(args, base)...); err == nil {
		t.Fatalf("the fold did not stop at the fixup:\n%s", out)
	}
}

// A fixup that also deletes an image, resolved with the deletion kept, is
// no drop and no error: the image's absence is read as absence.
func TestResolutionDropsTakesAKeptDeletion(t *testing.T) {
	t.Parallel()
	dir, write, git := plainRepo(t)
	gittest.WriteFile(t, filepath.Join(dir, "img.bin"), "PNG\x00\x01old\x00")
	write("a.txt", "x\n", "base")
	base := git("rev-parse", "HEAD")
	write("a.txt", "x\nt1\n", "feat: t1")
	write("a.txt", "x\nt1\nctx\n", "feat: ctx")
	git("rm", "-q", "img.bin")
	write("a.txt", "x\nt1\nctx\nA\n", "fixup! feat: t1")
	fixup := git("rev-parse", "HEAD")
	foldStop(t, dir, base, "")
	resolve(t, dir, "x\nt1\nA\n")
	drops, unchecked, err := ResolutionDrops(dir, fixup, fixup)
	if err != nil || len(drops) != 0 || len(unchecked) != 0 {
		t.Errorf("drops %+v, unchecked %q, err %v; want none", drops, unchecked, err)
	}
}

// Onto a branch that renamed a.txt to z.txt, git carries the fixup and its
// resolution into z.txt: the lines kept there are kept, not dropped.
func TestResolutionDropsFollowsTheResolutionsRename(t *testing.T) {
	t.Parallel()
	dir, write, git := plainRepo(t)
	write("a.txt", "x\n", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "dest")
	git("mv", "a.txt", "z.txt")
	git("commit", "-q", "-m", "refactor: a.txt is z.txt")
	git("checkout", "-q", "main")
	write("a.txt", "x\nt1\n", "feat: t1")
	write("a.txt", "x\nt1\nctx\n", "feat: ctx")
	write("a.txt", "x\nt1\nctx\nA\nB\n", "fixup! feat: t1")
	fixup := git("rev-parse", "HEAD")
	foldStop(t, dir, base, "dest")
	gittest.WriteFile(t, filepath.Join(dir, "z.txt"), "x\nt1\nA\nB\n")
	git("add", "z.txt")
	drops, _, err := ResolutionDrops(dir, fixup, fixup)
	if err != nil || len(drops) != 0 {
		t.Errorf("drops %+v, err %v; want none: A and B are in z.txt", drops, err)
	}
}

// A README-only commit to come inherits the fixup's image in its tree but
// does not write it: it does not bring back an image the resolution drops.
func TestResolutionDropsCreditsOnlyACommitThatWritesTheBlob(t *testing.T) {
	t.Parallel()
	dir, oldTip, fixup := dropsRepo(t, func(write func(string, string, string), _ func(...string)) {
		write("README", "readme\n", "docs: readme")
	})
	resolve(t, dir, "x\nt1\nA\nB\n")
	gitIn(t, dir, "checkout", "HEAD", "--", "img.bin")
	drops, _, err := ResolutionDrops(dir, fixup, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "img.bin" || !drops[0].Binary {
		t.Errorf("drops %+v, want img.bin's binary change", drops)
	}
}

// T, then C adds enabled=true, D turns it false, and the fixup of T, made at
// the top, turns it true again. Dropping true at the fixup's stop is a loss:
// C adds it back but D takes it away again.
func TestResolutionDropsCountsWhatTheCommitsToComeTakeAway(t *testing.T) {
	t.Parallel()
	dir, write, git := plainRepo(t)
	write("c.txt", "conf\n", "base")
	base := git("rev-parse", "HEAD")
	write("c.txt", "conf\nt\n", "feat: t")
	write("c.txt", "conf\nt\nenabled=true\n", "feat: enable")
	write("c.txt", "conf\nt\nenabled=false\n", "feat: disable")
	write("c.txt", "conf\nt\nenabled=true\n", "fixup! feat: t")
	fixup := git("rev-parse", "HEAD")
	foldStop(t, dir, base, "")
	gittest.WriteFile(t, filepath.Join(dir, "c.txt"), "conf\nt\n")
	git("add", "c.txt")
	drops, _, err := ResolutionDrops(dir, fixup, fixup)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "c.txt" || drops[0].N != 1 {
		t.Errorf("drops %+v, want enabled=true in c.txt", drops)
	}
}

// The target already logs done; the fixup adds a second log("done") inside a
// handler only a later commit adds. Dropping the fixup's is a loss, the one
// already there notwithstanding: the old tip has two.
func TestResolutionDropsCountsACopyAlreadyThere(t *testing.T) {
	t.Parallel()
	dir, write, git := plainRepo(t)
	write("h.js", "start\n", "base")
	base := git("rev-parse", "HEAD")
	write("h.js", "start\nfunction a() {\nlog(\"done\")\n}\n", "feat: a")
	write("h.js", "start\nfunction a() {\nlog(\"done\")\n}\nfunction handler() {\nwork()\n}\n", "feat: handler")
	write("h.js", "start\nfunction a() {\nlog(\"done\")\n}\nfunction handler() {\nwork()\nlog(\"done\")\n}\n", "fixup! feat: a")
	fixup := git("rev-parse", "HEAD")
	foldStop(t, dir, base, "")
	gittest.WriteFile(t, filepath.Join(dir, "h.js"), "start\nfunction a() {\nlog(\"done\")\n}\n")
	git("add", "h.js")
	drops, _, err := ResolutionDrops(dir, fixup, fixup)
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 || drops[0].Path != "h.js" || drops[0].N != 1 {
		t.Errorf("drops %+v, want the second log(\"done\") in h.js", drops)
	}
}
