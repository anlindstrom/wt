package wtsync

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/anlindstrom/wt/internal/git"
)

// commitTimeout is the deadline of the commit a fixup makes: signing may wait
// for a person.
const commitTimeout = 10 * time.Minute

// FixupSubject is the subject of a new fixup at the top of the line commits
// that a fold takes into commits[target] and no other commit: its subject
// when no other commit of the line has it, else an id of it. why is set when
// commits[target] takes no fixup, or no subject selects it. Revisions are
// read in dir, as the run reads them in the line's worktree.
func FixupSubject(dir string, commits []string, target int) (subject, why string, err error) {
	read, err := readMoveCommits(dir, commits)
	if err != nil {
		return "", "", err
	}
	all := make([]lineCommit, len(read))
	index := map[string]int{}
	shared := 0
	for i, c := range read {
		all[i] = lineCommit{c.sha, c.subject}
		index[c.sha] = i
		if c.subject == read[target].subject {
			shared++
		}
	}
	c := all[target]
	who := fmt.Sprintf("%s %q", git.ShortID(c.sha, 7), oneLinePlan(c.subject))
	switch {
	case isMove(c.subject):
		return "", who + " is a move! commit, which the fold drops: it takes no fixup", nil
	case isFold(c.subject):
		j, ok := foldTarget(dir, all[:target], c.subject, index)
		for ok && isFold(all[j].subject) {
			j, ok = foldTarget(dir, all[:j], all[j].subject, index)
		}
		if !ok {
			return "", who + " is itself folded into another commit, which its subject does not name alone: it takes no fixup", nil
		}
		return "", fmt.Sprintf("%s is itself folded into %s %q: name that commit", who, git.ShortID(all[j].sha, 7), oneLinePlan(all[j].subject)), nil
	}
	selects := func(name string) bool {
		j, ok := foldTarget(dir, all, "fixup! "+name, index)
		return ok && j == target
	}
	if shared == 1 && selects(c.subject) {
		return "fixup! " + c.subject, "", nil
	}
	if id := offeredID(dir, all, index, target); id != "" && selects(id) {
		return "fixup! " + id, "", nil
	}
	return "", "no fixup subject selects " + who + " alone: other commits' subjects spell its subject and every id of it", nil
}

// StagedFile is a path the index changes from HEAD, and git's status letter
// for the change: A, M, D or T.
type StagedFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// IndexOverride is the environment variable that points git at another index
// or repository than the worktree's own, "" for none.
func IndexOverride() string {
	for _, name := range []string{"GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		if _, set := os.LookupEnv(name); set {
			return name
		}
	}
	return ""
}

// HeldIndex is a worktree's index held still: git's own index lock is taken,
// so no git command stages, merges, rebases or switches branches there until
// Release, and Copy is the index as it was then.
type HeldIndex struct {
	Copy    string
	once    sync.Once
	release []func()
}

// Release lets go of the index. Only the first call does anything: a second
// would remove a lock another git has taken since.
func (h *HeldIndex) Release() {
	h.once.Do(func() {
		for i := len(h.release) - 1; i >= 0; i-- {
			h.release[i]()
		}
	})
}

// indexFiles is the worktree's index and its lock, and why no commit can be
// made of it: there is no index.
func indexFiles(wtPath string) (index, lock, why string, err error) {
	paths, err := sequencerDirs(wtPath, "index", "index.lock")
	if err != nil {
		return "", "", "", err
	}
	if _, err := os.Stat(paths[0]); errors.Is(err, os.ErrNotExist) {
		why = "there is no index at " + paths[0] + " (no index; nothing is staged)"
	} else if err != nil {
		return "", "", "", err
	}
	return paths[0], paths[1], why, nil
}

// IndexBlocked is why the index of the worktree at wtPath cannot be held, ""
// when it can: what HoldIndex would say, read without writing anything.
func IndexBlocked(wtPath string) (string, error) {
	_, lock, why, err := indexFiles(wtPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(lock); errors.Is(err, os.ErrNotExist) {
		return why, nil
	} else if err != nil {
		return "", err
	}
	return indexLockedBy(lock), nil
}

func indexLockedBy(lock string) string {
	return lock + " exists: another git is writing the index, or one was killed and left it"
}

// HoldIndex takes the index lock of the worktree at wtPath. why is set when
// someone else holds it, or there is no index. A caller that a signal may end
// releases it then too.
func HoldIndex(wtPath string) (held *HeldIndex, why string, err error) {
	index, lock, why, err := indexFiles(wtPath)
	if err != nil {
		return nil, "", err
	}
	h := &HeldIndex{}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, indexLockedBy(lock), nil
	} else if err != nil {
		return nil, "", err
	}
	h.release = append(h.release, func() { _ = os.Remove(lock) })
	defer func() {
		if held == nil {
			h.Release()
		}
	}()
	if err := f.Close(); err != nil || why != "" {
		return nil, why, err
	}
	copied, cleanup, err := scratchIndex()
	if err != nil {
		return nil, "", err
	}
	h.release = append(h.release, cleanup)
	data, err := os.ReadFile(index)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(copied, data, 0o600); err != nil { //nolint:gosec // a file in wt's own temporary directory
		return nil, "", err
	}
	h.Copy = copied
	return h, "", nil
}

// Staged is what a commit of an index of the worktree at wtPath would change:
// the worktree's own, or the copy index names when not "". why is set when
// there is nothing to commit, or a path is unmerged.
func Staged(wtPath, index string) (files []StagedFile, why string, err error) {
	var env []string
	if index != "" {
		env = []string{"GIT_INDEX_FILE=" + index}
	}
	unmerged, err := gitEnv(wtPath, env, nil, "ls-files", "--unmerged")
	if err != nil {
		return nil, "", err
	}
	if unmerged != "" {
		return nil, "the index has unmerged paths: resolve them first", nil
	}
	out, err := gitEnv(wtPath, env, nil, "diff-index", "--cached", "--name-status", "-z", "--no-renames",
		"--ignore-submodules=none", "--ita-invisible-in-index", "HEAD", "--")
	if err != nil {
		return nil, "", err
	}
	f := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		files = append(files, StagedFile{Status: f[i], Path: f[i+1]})
	}
	if len(files) == 0 {
		return nil, "nothing is staged: stage what the fixup holds (git add) first", nil
	}
	return files, "", nil
}

// CommitStaged commits the tree of the held index on tip, the commit branch is
// at, with the subject, and moves branch to it, only from tip. The commit is
// built in the object store, signed when commit.gpgSign says so, and checked
// before the branch moves: no commit hook runs. proceed is asked last, with
// the branch ready to move, and false leaves it. The index is not written: it
// holds what was committed. A commit returned with an error is on the branch;
// an error alone says what became of the branch.
func CommitStaged(wtPath string, held *HeldIndex, branch, tip, subject string, proceed func() bool) (string, error) {
	nothing := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf(format+"; nothing is committed", a...)
	}
	tree, err := gitEnv(wtPath, []string{"GIT_INDEX_FILE=" + held.Copy}, nil, "write-tree")
	if err != nil {
		return nothing("the staged tree could not be written: %s", git.Reason(err))
	}
	sign, code, err := gitEnvAllow(wtPath, nil, nil, 1, "config", "--type=bool", "--get", "commit.gpgsign")
	if err != nil {
		return nothing("commit.gpgsign could not be read: %s", git.Reason(err))
	}
	args := []string{"commit-tree", tree, "-p", tip, "-F", "-"}
	if code == 0 && sign == "true" {
		args = append(args, "-S")
	}
	out, err := git.Exec(git.Opts{Dir: wtPath, Stdin: strings.NewReader(subject + "\n"), Timeout: commitTimeout}, args...)
	if err != nil {
		return nothing("git commit-tree failed: %s", git.Reason(err))
	}
	commit := strings.TrimSpace(string(out))
	made, err := gitEnv(wtPath, nil, nil, "log", "-1", "--format=%T%x00%P%x00%s", commit)
	if err != nil {
		return nothing("the commit built could not be read: %s", git.Reason(err))
	}
	if made != tree+"\x00"+tip+"\x00"+subject {
		return nothing("the commit built is not the one asked for")
	}
	ref := "refs/heads/" + branch
	moveErr := publish(wtPath, ref, tip, commit, proceed)
	if moveErr == nil {
		return commit, nil
	}
	// A git that failed or timed out may have moved the branch all the same.
	switch at, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", ref); {
	case err != nil:
		return "", fmt.Errorf("moving %s failed (%s), and where it is now could not be read: look before trying again", branch, git.Reason(moveErr))
	case at == commit:
		return commit, fmt.Errorf("%s is committed, but git update-ref reported a failure: %s", git.ShortID(commit, 7), git.Reason(moveErr))
	case at != tip:
		return nothing("%s moved to %s meanwhile", branch, git.ShortID(at, 7))
	}
	return nothing("%s", git.Reason(moveErr))
}

// publish moves ref, which HEAD is bound to, from tip to commit in one
// transaction of git update-ref. Prepared, git holds the locks of ref and of
// HEAD, so neither binding can change: both are read then, and the
// transaction is committed only when they hold and proceed agrees.
func publish(wtPath, ref, tip, commit string, proceed func() bool) error {
	cmd := exec.Command("git", "update-ref", "--no-deref", "-m", "wt sync fixup", "--stdin")
	cmd.Dir = wtPath
	cmd.Env = git.Environ("GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	ran := make(chan error, 1)
	go func() {
		timedOut, _, err := git.RunBounded(gitDeadline, cmd)
		switch {
		case timedOut:
			err = fmt.Errorf("git update-ref did not answer in %s", gitDeadline)
		case err != nil:
			err = fmt.Errorf("git update-ref: %s (%w)", strings.TrimSpace(stderr.String()), err)
		}
		ran <- err
	}()
	// end sends the transaction's last word and waits for git to leave.
	end := func(word string) error {
		_, werr := io.WriteString(in, word)
		cerr := in.Close()
		if err := <-ran; err != nil {
			return err
		}
		return errors.Join(werr, cerr)
	}
	if _, err := fmt.Fprintf(in, "start\nupdate %s %s %s\nprepare\n", ref, commit, tip); err != nil {
		return errors.Join(end(""), err)
	}
	for answers := bufio.NewScanner(out); ; {
		if !answers.Scan() {
			if err := end(""); err != nil {
				return err
			}
			return errors.New("git update-ref did not prepare the move")
		}
		if answers.Text() == "prepare: ok" {
			break
		}
	}
	refused := func(why string) error {
		return errors.Join(errors.New(why), end("abort\n"))
	}
	if _, code, err := gitEnvAllow(wtPath, nil, nil, 1, "symbolic-ref", "--quiet", ref); err != nil {
		return errors.Join(err, end("abort\n"))
	} else if code == 0 {
		return refused(ref + " is a symbolic ref now, not a branch of its own")
	}
	if head, err := gitEnv(wtPath, nil, nil, "symbolic-ref", "--quiet", "HEAD"); err != nil || head != ref {
		return refused(strings.TrimPrefix(ref, "refs/heads/") + " is no longer checked out here")
	}
	if !proceed() {
		return refused("interrupted")
	}
	return end("commit\n")
}

// Locked is why a run holds the worktree whose git dir is gitDir, "" when
// none does: what Acquire would refuse with, read without writing anything.
func Locked(gitDir string, now time.Time) (string, error) {
	l, ok, err := ReadLock(gitDir)
	if err != nil || !ok || !LockLive(l, now) {
		return "", err
	}
	return lockHeld(l, now).Error(), nil
}
