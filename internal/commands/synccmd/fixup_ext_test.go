package synccmd_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

type fixupJSON struct {
	DryRun bool       `json:"dryRun"`
	Target *commitRef `json:"target"`
	Commit *struct {
		SHA     *string `json:"sha"`
		Subject string  `json:"subject"`
	} `json:"commit"`
	Files []struct {
		Status string `json:"status"`
		Path   string `json:"path"`
	} `json:"files"`
	Refusal *string `json:"refusal"`
}

func fixupOf(t *testing.T, ctx *kit.Context, rev string, dryRun bool) (fixupJSON, error) {
	t.Helper()
	var out bytes.Buffer
	err := synccmd.SyncFixup(ctx, synccmd.FixupOptions{Rev: rev, DryRun: dryRun, JSON: true}, &out)
	testkit.ValidateJSON(t, "sync-fixup", out.Bytes())
	var res fixupJSON
	if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out.String())
	}
	return res, err
}

// snapshot is everything a fixup that writes nothing leaves as it was: HEAD,
// every ref, the index and the worktree of dir.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join([]string{
		testkit.GitOut(t, dir, "rev-parse", "--symbolic-full-name", "HEAD"),
		testkit.GitOut(t, dir, "for-each-ref"),
		testkit.GitOut(t, dir, "ls-files", "--stage"),
		testkit.GitOut(t, dir, "diff"),
		testkit.GitOut(t, dir, "status", "--porcelain"),
	}, "\n--\n")
}

// A fixup commits the staged version of the current worktree, and nothing of
// the unstaged edits or of another checkout's index; its dry run says the same
// and writes nothing; and the real fold puts it in its target.
func TestSyncFixupCommitsWhatIsStagedInTheCurrentWorktree(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
	testkit.WriteFile(t, main, "base.txt", "staged in the main checkout\n")
	testkit.GitIn(t, main, "add", "base.txt")
	staged := doorFile(map[int]string{5: "d5 body", 11: "staged"})
	testkit.WriteFile(t, tmp, "pkg/door.txt", staged)
	testkit.GitIn(t, tmp, "add", "-A")
	unstaged := doorFile(map[int]string{2: "unstaged", 5: "d5 body", 11: "staged"})
	testkit.WriteFile(t, tmp, "pkg/door.txt", unstaged)
	mainBefore, before := snapshot(t, main), snapshot(t, tmp)
	look, err := fixupOf(t, ctx, "l1", true)
	if err != nil || !look.DryRun || look.Commit == nil || look.Commit.SHA != nil {
		t.Fatalf("dry run: %v %+v", err, look)
	}
	if after := snapshot(t, tmp); after != before {
		t.Fatalf("the dry run changed\n%s\ninto\n%s", before, after)
	}
	old := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	got, err := fixupOf(t, ctx, "l1", false)
	if err != nil || got.Commit == nil || got.Commit.SHA == nil {
		t.Fatalf("%v %+v", err, got)
	}
	head := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	if *got.Commit.SHA != head || testkit.GitOut(t, tmp, "rev-parse", "HEAD^") != old {
		t.Errorf("committed %s, HEAD %s", *got.Commit.SHA, head)
	}
	if s := testkit.GitOut(t, tmp, "log", "-1", "--format=%s"); s != "fixup! feat: door" || got.Commit.Subject != s || look.Commit.Subject != s {
		t.Errorf("subject %q, said %q, dry run %q", s, got.Commit.Subject, look.Commit.Subject)
	}
	if *look.Target != *got.Target || !slices.Equal(look.Files, got.Files) || len(got.Files) != 1 || got.Files[0].Path != "pkg/door.txt" || got.Files[0].Status != "M" {
		t.Errorf("dry run %+v, real %+v", look, got)
	}
	if c := testkit.GitOut(t, tmp, "show", "HEAD:pkg/door.txt"); c+"\n" != staged {
		t.Errorf("committed\n%s", c)
	}
	if now := read(t, filepath.Join(tmp, "pkg/door.txt")); now != unstaged || testkit.GitOut(t, tmp, "status", "--porcelain") != " M pkg/door.txt" {
		t.Errorf("the unstaged edit: %q, status %q", now, testkit.GitOut(t, tmp, "status", "--porcelain"))
	}
	if after := snapshot(t, main); strings.Split(after, "\n--\n")[2] != strings.Split(mainBefore, "\n--\n")[2] {
		t.Errorf("the main checkout's index changed:\n%s", after)
	}
	testkit.GitIn(t, tmp, "checkout", "-q", "--", ".")
	testkit.GitIn(t, main, "reset", "-q", "--hard")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, openAt(t, main), ownTmp, opts); err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	if c := testkit.GitOut(t, main, "show", "l1:pkg/door.txt"); !strings.Contains(c, "staged") {
		t.Errorf("the fold did not put the fixup in l1:\n%s", c)
	}
}

// stage commits nothing: it stages a new file in dir.
func stage(t *testing.T, dir, name string) {
	t.Helper()
	testkit.WriteFile(t, dir, name, name+"\n")
	testkit.GitIn(t, dir, "add", name)
}

func emptyCommit(t *testing.T, dir, subject string) string {
	t.Helper()
	testkit.GitIn(t, dir, "commit", "-q", "--no-verify", "--allow-empty", "-m", subject)
	return testkit.GitOut(t, dir, "rev-parse", "HEAD")
}

// The subject names exactly the commit asked for: its id where another commit
// shares its subject, and rev is read as git reads it, a tag before a subject.
func TestSyncFixupNamesExactlyTheCommit(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	emptyCommit(t, tmp, "chore: bump")
	second := emptyCommit(t, tmp, "chore: bump")
	emptyCommit(t, tmp, "release")
	tagged := emptyCommit(t, tmp, "feat: tagged")
	testkit.GitIn(t, tmp, "tag", "release", tagged)
	for _, tc := range []struct{ rev, target, subject string }{
		{"HEAD~2", second, "fixup! " + second[:10]},
		{"release", tagged, "fixup! feat: tagged"},
	} {
		stage(t, tmp, tc.target[:8]+".txt")
		got, err := fixupOf(t, ctx, tc.rev, false)
		if err != nil || got.Target == nil || got.Target.SHA != tc.target || got.Commit.Subject != tc.subject {
			t.Errorf("%s: %v %+v %+v, want %s %q", tc.rev, err, got.Target, got.Commit, tc.target, tc.subject)
		}
		testkit.GitIn(t, tmp, "reset", "-q", "--hard", tagged)
	}
}

// No spelling selects a commit whose every id is another commit's subject: the
// fixup is refused rather than aimed elsewhere.
func TestSyncFixupRefusesWhenNoSubjectSelectsTheCommit(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	emptyCommit(t, tmp, "chore: bump")
	target := emptyCommit(t, tmp, "chore: bump")
	for n := 4; n <= len(target); n++ {
		emptyCommit(t, tmp, target[:n])
	}
	stage(t, tmp, "s.txt")
	before := snapshot(t, tmp)
	got, err := fixupOf(t, ctx, target, false)
	if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, "no fixup subject") {
		t.Errorf("%v %+v", err, got)
	}
	if after := snapshot(t, tmp); after != before {
		t.Errorf("a refusal changed\n%s", after)
	}
}

// The commit is built from the staged tree: no commit hook runs, so none can
// change what it holds or its subject, or keep it from being made.
func TestSyncFixupRunsNoCommitHooks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
	for hook, script := range map[string]string{
		"pre-commit":  "echo more > hooked.txt && git add hooked.txt",
		"commit-msg":  `echo "ABC-1 $(cat "$1")" > "$1"`,
		"post-commit": "echo ran > hooked.txt",
	} {
		gittest.WriteExecutable(t, filepath.Join(main, ".git", "hooks", hook), "#!/bin/sh\n"+script+"\n")
	}
	stage(t, tmp, "s.txt")
	testkit.WriteFile(t, tmp, "later.txt", "meant to be added\n")
	testkit.GitIn(t, tmp, "add", "--intent-to-add", "later.txt")
	if got, err := fixupOf(t, ctx, "l1", false); err != nil {
		t.Fatalf("%v %+v", err, got)
	}
	if got := testkit.GitOut(t, tmp, "diff-tree", "--no-commit-id", "--name-status", "-r", "HEAD"); got != "A\ts.txt" {
		t.Errorf("committed %q", got)
	}
	if s := testkit.GitOut(t, tmp, "log", "-1", "--format=%B"); s != "fixup! feat: door" {
		t.Errorf("message %q", s)
	}
	if got := testkit.GitOut(t, tmp, "status", "--porcelain"); got != " A later.txt" {
		t.Errorf("status %q", got)
	}
}

// fakeSigner is a gpg.program that signs anything, and answers a check of a
// signature as gpg does.
const fakeSigner = `case "$*" in *--verify*) echo '[GNUPG:] GOODSIG 0 t' ; echo 'gpg: Good signature' >&2; exit 0;; esac
cat >/dev/null
echo '[GNUPG:] SIG_CREATED ' >&2
printf -- '-----BEGIN PGP SIGNATURE-----\nx\n-----END PGP SIGNATURE-----\n'`

// commit.gpgSign signs the fixup, and a signer that fails leaves the branch and
// the index as they were.
func TestSyncFixupSignsAsConfigured(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, script := range map[string]string{
		"signs":            fakeSigner,
		"signs, and shown": fakeSigner,
		"fails":            "exit 1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
			signer := filepath.Join(t.TempDir(), "gpg")
			gittest.WriteExecutable(t, signer, "#!/bin/sh\n"+script+"\n")
			testkit.GitIn(t, main, "config", "gpg.program", signer)
			testkit.GitIn(t, main, "config", "commit.gpgsign", "true")
			testkit.GitIn(t, main, "config", "log.showSignature", fmt.Sprint(name == "signs, and shown"))
			stage(t, tmp, "s.txt")
			before := snapshot(t, tmp)
			got, err := fixupOf(t, ctx, "l1", false)
			if name == "fails" {
				if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, "nothing is committed") {
					t.Errorf("%v %+v", err, got)
				}
				if after := snapshot(t, tmp); after != before {
					t.Errorf("changed\n%s", after)
				}
				return
			}
			if err != nil || !strings.Contains(testkit.GitOut(t, tmp, "cat-file", "commit", "HEAD"), "gpgsig ") {
				t.Errorf("%v: unsigned %+v", err, got)
			}
		})
	}
}

// A branch git will not move is reported as unmoved, and the index is free
// again.
func TestSyncFixupSaysTheBranchDidNotMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
	hook := filepath.Join(main, ".git", "hooks", "reference-transaction")
	gittest.WriteExecutable(t, hook, "#!/bin/sh\ntest \"$1\" != prepared\n")
	stage(t, tmp, "s.txt")
	before := snapshot(t, tmp)
	got, err := fixupOf(t, ctx, "l1", false)
	if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, "nothing is committed") {
		t.Errorf("%v %+v", err, got)
	}
	if after := snapshot(t, tmp); after != before {
		t.Errorf("changed\n%s", after)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	stage(t, tmp, "s2.txt")
}

// The branch moves only while HEAD is still bound to it and it is still a
// branch of its own: both are checked where git holds their locks, so what a
// slow signer saw change is seen.
func TestSyncFixupPublishesOnlyToTheBranchCheckedOut(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct{ name, meanwhile string }{
		{"HEAD bound elsewhere", "git symbolic-ref HEAD refs/heads/elsewhere"},
		{"a symbolic ref", "git symbolic-ref refs/heads/" + ownTmp + " refs/heads/elsewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
			testkit.GitIn(t, main, "branch", "elsewhere", ownTmp)
			signer := filepath.Join(t.TempDir(), "gpg")
			gittest.WriteExecutable(t, signer, "#!/bin/sh\n"+tc.meanwhile+"\n"+fakeSigner+"\n")
			testkit.GitIn(t, main, "config", "gpg.program", signer)
			testkit.GitIn(t, main, "config", "commit.gpgsign", "true")
			stage(t, tmp, "s.txt")
			old := testkit.GitOut(t, main, "rev-parse", ownTmp)
			got, err := fixupOf(t, ctx, "l1", false)
			if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, "nothing is committed") {
				t.Errorf("%v %+v", err, got)
			}
			for _, branch := range []string{ownTmp, "elsewhere"} {
				if now := testkit.GitOut(t, main, "rev-parse", branch); now != old {
					t.Errorf("%s moved to %s", branch, now)
				}
			}
		})
	}
}

// An index or a repository named by the environment is not the worktree's own,
// whose lock the commit holds.
//
// Not parallel: sets GIT_INDEX_FILE.
func TestSyncFixupRefusesAnIndexOfTheEnvironments(t *testing.T) {
	skipShort(t)
	ctx, _, tmp := ownLine(t, synccmdtest.StackYAML)
	stage(t, tmp, "s.txt")
	old := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "other-index"))
	for _, dry := range []bool{true, false} {
		got, err := fixupOf(t, ctx, "l1", dry)
		if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, "GIT_INDEX_FILE") {
			t.Errorf("dry %v: %v %+v", dry, err, got)
		}
	}
	if err := os.Unsetenv("GIT_INDEX_FILE"); err != nil {
		t.Fatal(err)
	}
	if now := testkit.GitOut(t, tmp, "rev-parse", "HEAD"); now != old {
		t.Errorf("HEAD moved to %s", now)
	}
}

// A signal while the signer waits stops it, publishes nothing and leaves no
// lock behind.
func TestSyncFixupInterruptedWhileSigningCommitsNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	if os.Getenv(testkit.InterruptChild) == "" {
		out, status := testkit.InterruptedChild(t)
		_, rest, found := strings.Cut(out, "FIXTURE ")
		if !found || status != kit.InterruptStatus {
			t.Fatalf("status %d:\n%s", status, out)
		}
		line, _, _ := strings.Cut(rest, "\n")
		tmp, old, _ := strings.Cut(line, " ")
		if now := testkit.GitOut(t, tmp, "rev-parse", "HEAD"); now != old {
			t.Errorf("HEAD moved to %s", now)
		}
		for _, lock := range []string{"index.lock", wtsync.LockName} {
			if path := testkit.GitOut(t, tmp, "rev-parse", "--path-format=absolute", "--git-path", lock); exists(path) {
				t.Errorf("%s is left", path)
			}
		}
		_, object, found := strings.Cut(out, "{\n")
		if !found {
			t.Fatalf("no result object:\n%s", out)
		}
		object, _, _ = strings.Cut("{\n"+object, "\n}")
		testkit.ValidateJSON(t, "sync-fixup", []byte(object+"\n}"))
		if !strings.Contains(object, "nothing is committed") {
			t.Errorf("%s", object)
		}
		return
	}
	// The parent reads the repository after this process is gone, so it is
	// not under a directory the test removes.
	ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
	signing := filepath.Join(t.TempDir(), "signing")
	signer := filepath.Join(t.TempDir(), "gpg")
	gittest.WriteExecutable(t, signer, "#!/bin/sh\ntouch '"+signing+"'\nsleep 60\n"+fakeSigner+"\n")
	testkit.GitIn(t, main, "config", "gpg.program", signer)
	testkit.GitIn(t, main, "config", "commit.gpgsign", "true")
	stage(t, tmp, "s.txt")
	fmt.Printf("FIXTURE %s %s\n", tmp, testkit.GitOut(t, tmp, "rev-parse", "HEAD"))
	go func() {
		for !exists(signing) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	err := synccmd.SyncFixup(ctx, synccmd.FixupOptions{Rev: "l1", JSON: true}, os.Stdout)
	t.Fatalf("the fixup returned (%v) instead of being interrupted", err)
}

// A revision that searches messages names a commit here as it does to git.
func TestSyncFixupReadsARevisionSearch(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, main, tmp := ownLine(t, synccmdtest.StackYAML)
	stage(t, tmp, "s.txt")
	got, err := fixupOf(t, ctx, ":/feat: door", false)
	if err != nil || got.Target == nil || got.Target.SHA != testkit.GitOut(t, main, "rev-parse", "l1") {
		t.Errorf("%v %+v", err, got)
	}
}

// What cannot be committed as a fixup of that commit is refused, dry run or
// not, and HEAD, the index, the worktree and git's own markers survive.
func TestSyncFixupRefusals(t *testing.T) {
	t.Parallel()
	skipShort(t)
	side := func(t *testing.T, dir, file, content string) {
		t.Helper()
		testkit.GitIn(t, dir, "checkout", "-q", "-b", "side", "l2")
		testkit.WriteFile(t, dir, file, content)
		testkit.GitIn(t, dir, "add", "-A")
		testkit.GitIn(t, dir, "commit", "-q", "--no-verify", "-m", "feat: side")
		testkit.GitIn(t, dir, "checkout", "-q", ownTmp)
	}
	for _, tc := range []struct {
		name, rev, want, marker string
		make                    func(t *testing.T, dir string)
	}{
		{"nothing staged", "l1", "nothing is staged", "", func(t *testing.T, dir string) {
			testkit.WriteFile(t, dir, "other.txt", "unstaged\n")
		}},
		{"a merge, resolved", "l1", "a merge", "MERGE_HEAD", func(t *testing.T, dir string) {
			side(t, dir, "side.txt", "s\n")
			testkit.GitIn(t, dir, "merge", "-q", "--no-ff", "--no-commit", "side")
		}},
		{"unmerged paths", "l1", "unmerged", "", func(t *testing.T, dir string) {
			side(t, dir, "other.txt", "theirs\n")
			_, _ = gittest.Try(t, dir, "merge", "-q", "side")
			testkit.GitIn(t, dir, "merge", "--quit")
		}},
		{"a cherry-pick", "l1", "a cherry-pick", "CHERRY_PICK_HEAD", func(t *testing.T, dir string) {
			side(t, dir, "side.txt", "s\n")
			testkit.GitIn(t, dir, "cherry-pick", "--no-commit", "side")
			testkit.GitIn(t, dir, "update-ref", "CHERRY_PICK_HEAD", "side")
		}},
		{"a revert", "l1", "a revert", "REVERT_HEAD", func(t *testing.T, dir string) {
			testkit.GitIn(t, dir, "revert", "--no-commit", "HEAD")
			testkit.GitIn(t, dir, "update-ref", "REVERT_HEAD", "HEAD")
		}},
		{"a rebase", "l1", "a rebase", "rebase-merge", func(t *testing.T, dir string) {
			_, _ = gittest.Try(t, dir, "rebase", "--exec", "false", "HEAD~1")
			stage(t, dir, "s.txt")
		}},
		{"a commit off the line", "main", "not a commit of the line", "", func(t *testing.T, dir string) {
			stage(t, dir, "s.txt")
		}},
		{"a fixup", "HEAD", "feat: door", "", func(t *testing.T, dir string) {
			emptyCommit(t, dir, "fixup! feat: door")
			stage(t, dir, "s.txt")
		}},
		{"no index", "l1", "no index", "", func(t *testing.T, dir string) {
			if err := os.Remove(testkit.GitOut(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")); err != nil {
				t.Fatal(err)
			}
		}},
		{"another git's index lock", "l1", "index.lock", "index.lock", func(t *testing.T, dir string) {
			stage(t, dir, "s.txt")
			gittest.WriteFile(t, testkit.GitOut(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index.lock"), "")
		}},
		{"a run's lock", "l1", "locked", "wt-sync.lock", func(t *testing.T, dir string) {
			stage(t, dir, "s.txt")
			holdLock(t, dir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, tmp := ownLine(t, synccmdtest.StackYAML)
			tc.make(t, tmp)
			ctx := openAt(t, tmp)
			marker := ""
			if tc.marker != "" {
				marker = testkit.GitOut(t, tmp, "rev-parse", "--path-format=absolute", "--git-path", tc.marker)
			}
			before := snapshot(t, tmp)
			for _, dry := range []bool{true, false} {
				got, err := fixupOf(t, ctx, tc.rev, dry)
				if err == nil || got.Commit != nil || got.Refusal == nil || !strings.Contains(*got.Refusal, tc.want) {
					t.Errorf("dry %v: %v %+v, want a refusal saying %q", dry, err, got.Refusal, tc.want)
				}
			}
			if after := snapshot(t, tmp); after != before {
				t.Errorf("a refusal changed\n%s\ninto\n%s", before, after)
			}
			if marker != "" && !exists(marker) {
				t.Errorf("%s is gone", marker)
			}
		})
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// holdLock takes the sync lock of the worktree at dir for the test.
func holdLock(t *testing.T, dir string) {
	t.Helper()
	gitDir, err := wtsync.GitDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := wtsync.Acquire(gitDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
}
