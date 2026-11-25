package wtsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// Drop is what a drop at a stop did: the files it took the target's side of,
// the ones a strategy had answered and it left as they were, the stopped
// commit's files it kept, and the ref it kept the person's files under
// before it replaced any, with the paths that ref holds.
type Drop struct {
	Commit, TrunkCommit string
	Taken, Strategy     []string
	Kept                []string
	KeptRef             string
	Saved               []string
}

// dropPaths splits the files trunk's copy shares with the stopped commit
// into those the drop takes and those a strategy answered at the stop, which
// it leaves as they are.
func dropPaths(st State) (taken, strategy []string) {
	for _, p := range st.TrunkCopy.Taken {
		if _, ok := st.Resolved[p]; ok || slices.Contains(st.Deleted, p) {
			strategy = append(strategy, p)
			continue
		}
		taken = append(taken, p)
	}
	return taken, strategy
}

// CheckDrop says whether a drop named drop may be taken at the stop st
// hands over in the worktree at wtPath: a conflict stop at a commit trunk
// carries reworked, drop naming that very commit, the rebase still at the
// stop, nothing unmerged outside the files the drop takes, and every file it
// would replace a regular file whose staged version, when it has one, is the
// one in the worktree. It changes nothing.
func CheckDrop(wtPath string, st State, drop string) error {
	c := st.TrunkCopy
	if c == nil || st.Check != nil || st.AtEdit || st.StoppedCommit == "" {
		return errors.New("--drop is for a conflict stop at a commit trunk carries reworked, and this stop is not one")
	}
	named, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", drop+"^{commit}")
	if err != nil || named != st.StoppedCommit {
		return fmt.Errorf("--drop %s is not the commit the rebase stopped at (%s %q): only that commit can be dropped here", drop, git.ShortID(st.StoppedCommit, 7), c.Subject)
	}
	if !AtHandoverStop(wtPath, st) || rebaseHead(wtPath) != st.StoppedCommit {
		return fmt.Errorf("the rebase is no longer at the stop at %s that wt handed over, so the drop cannot be taken", git.ShortID(st.StoppedCommit, 7))
	}
	unmerged, err := StagedConflicts(wtPath)
	if err != nil {
		return err
	}
	var outside []string
	for _, u := range unmerged {
		if !slices.Contains(c.Taken, u.Path) {
			outside = append(outside, u.Path)
		}
	}
	if len(outside) > 0 {
		return fmt.Errorf("%s still unmerged, and trunk's %s does not touch it: resolve it and git add it, then run %s again",
			strings.Join(outside, ", "), git.ShortID(c.Commit, 7), DropCommand(st.Work, st.StoppedCommit))
	}
	taken, _ := dropPaths(st)
	var odd, split []string
	for _, p := range taken {
		regular, same, err := dropReady(wtPath, st, p)
		if err != nil {
			return err
		}
		switch {
		case !regular:
			odd = append(odd, p)
		case !same:
			split = append(split, p)
		}
	}
	if len(odd) > 0 {
		return fmt.Errorf("%s not a regular file here, at HEAD or in the stopped commit (a symlink or a submodule): the drop replaces only regular files, so resolve it by hand and leave it out of the drop", strings.Join(odd, ", ")+isAre(len(odd)))
	}
	if len(split) > 0 {
		return fmt.Errorf("%s staged one way and changed another in the worktree: the drop would replace both, so git add the version you want, or put the file back with git restore, then run %s again",
			strings.Join(split, ", ")+isAre(len(split)), DropCommand(st.Work, st.StoppedCommit))
	}
	return nil
}

func isAre(n int) string {
	if n == 1 {
		return " is"
	}
	return " are"
}

// dropReady reads p for the drop: regular is a regular file, or none, in the
// worktree, at HEAD, in the stopped commit and in every stage of the index;
// same is no staged version, or a staged version that is the worktree's,
// content and mode.
func dropReady(wtPath string, st State, p string) (regular, same bool, err error) {
	if fi, err := os.Lstat(filepath.Join(wtPath, p)); err == nil && !fi.Mode().IsRegular() {
		return false, false, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	for _, at := range []string{"HEAD", st.StoppedCommit} {
		e, err := entryAt(wtPath, at, p)
		if err != nil {
			return false, false, err
		}
		if e.oid != "" && !regularMode(e.mode) {
			return false, false, nil
		}
	}
	out, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", p)
	if err != nil {
		return false, false, err
	}
	staged, stagedMode := "", ""
	for _, rec := range strings.Split(out, "\x00") {
		meta, _, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			continue
		}
		if !regularMode(f[0]) {
			return false, false, nil
		}
		if f[2] == "0" {
			staged, stagedMode = f[1], f[0]
		}
	}
	if staged == "" {
		return true, true, nil
	}
	if _, err := os.Lstat(filepath.Join(wtPath, p)); errors.Is(err, os.ErrNotExist) {
		return true, false, nil
	}
	// As git add would stage it, filters and all: what differs from that is
	// a change git itself sees.
	work, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "hash-object", "--", p)
	if err != nil {
		return false, false, err
	}
	mode, err := workMode(wtPath, p)
	if err != nil {
		return false, false, err
	}
	return true, work == staged && mode == stagedMode, nil
}

// workMode is the mode git gives the worktree's regular file p.
func workMode(wtPath, p string) (string, error) {
	fi, err := os.Lstat(filepath.Join(wtPath, p))
	if err != nil {
		return "", err
	}
	if fi.Mode()&0o111 != 0 {
		return "100755", nil
	}
	return "100644", nil
}

// TakeDrop takes the drop CheckDrop allowed, under the worktree's lock, with
// the handover in gitDir. Before it replaces anything it keeps every file it
// will replace that is not HEAD's (what a person wrote there, or the merge
// the stop left) in a commit under a ref of its own, and writes that ref and
// the taken files' resolutions into the handover: an interrupt from then on
// leaves both on disk. A file a ref of an earlier drop of the same stop
// already holds as it is now is not kept again. Then each taken file is set,
// index and file, to HEAD's (the target's side, what the line is replayed
// onto), or removed where HEAD has none; during is a test's hook after each.
// The stopped commit's other files are left as the stop and the person left
// them. It returns what it did and the handover as it now is.
func TakeDrop(wtPath, gitDir string, st State, during func(path string) error) (Drop, State, error) {
	c := st.TrunkCopy
	d := Drop{Commit: st.StoppedCommit, TrunkCommit: c.Commit, Kept: c.Kept}
	d.Taken, d.Strategy = dropPaths(st)
	keep := map[string]treeEntry{}
	var records []StopResolution
	for _, p := range d.Taken {
		e, err := entryIfNotHead(wtPath, p)
		if err != nil {
			return d, st, err
		}
		if e.oid != "" && !keptAs(wtPath, st.DropKept, p, e) {
			keep[p] = e
		}
		head, err := entryAt(wtPath, "HEAD", p)
		if err != nil {
			return d, st, err
		}
		records = append(records, StopResolution{Commit: st.StoppedCommit, Path: p, Mode: head.mode, Blob: head.oid, Drop: true})
	}
	cur, ok, err := ReadState(gitDir)
	if err != nil {
		return d, st, err
	}
	if !ok || cur.Epoch != st.Epoch {
		return d, st, errors.New("the handover changed while resume was checking it")
	}
	if len(keep) > 0 {
		ref, err := keepDropped(wtPath, gitDir, cur, keep)
		if err != nil {
			return d, st, fmt.Errorf("the files the drop would replace could not be kept (%w), so nothing is replaced", err)
		}
		cur.DropKept = ref
	}
	StampStops(cur, records, nil)
	cur.Resolutions, _ = cur.SettledAt(records, nil)
	cur.DropLeft = nil
	for _, r := range records {
		cur.DropLeft = append(cur.DropLeft, r.Path)
	}
	if err := WriteState(gitDir, cur); err != nil {
		return d, st, fmt.Errorf("what the drop keeps could not be written with the handover (%w), so nothing is replaced", err)
	}
	st = cur
	d.KeptRef = st.DropKept
	for _, p := range d.Taken {
		if keptAs(wtPath, d.KeptRef, p, treeEntry{}) {
			d.Saved = append(d.Saved, p)
		}
	}
	for _, r := range records {
		if r.Blob == "" {
			if _, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "rm", "-q", "--cached", "--ignore-unmatch", "--", r.Path); err != nil {
				return d, st, err
			}
			if err := os.Remove(filepath.Join(wtPath, r.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return d, st, err
			}
		} else if _, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "checkout", "HEAD", "--", r.Path); err != nil {
			return d, st, err
		}
		st.DropLeft = slices.DeleteFunc(slices.Clone(st.DropLeft), func(p string) bool { return p == r.Path })
		if err := WriteState(gitDir, st); err != nil {
			return d, st, fmt.Errorf("that %s is replaced could not be written with the handover: %w", r.Path, err)
		}
		if during != nil {
			if err := during(r.Path); err != nil {
				return d, st, err
			}
		}
	}
	return d, st, nil
}

// entryIfNotHead writes the worktree's file p, its bytes as they are with no
// filter applied, as a blob, and returns it with the file's mode, when it is
// there and is not HEAD's in content or mode: the zero entry for a file that
// is HEAD's, or absent.
func entryIfNotHead(wtPath, p string) (treeEntry, error) {
	data, err := os.ReadFile(filepath.Join(wtPath, p))
	if errors.Is(err, os.ErrNotExist) {
		return treeEntry{}, nil
	} else if err != nil {
		return treeEntry{}, err
	}
	blob, err := hashObject(wtPath, data)
	if err != nil {
		return treeEntry{}, err
	}
	mode, err := workMode(wtPath, p)
	if err != nil {
		return treeEntry{}, err
	}
	head, err := entryAt(wtPath, "HEAD", p)
	if err != nil {
		return treeEntry{}, err
	}
	if head.oid == blob && head.mode == mode {
		return treeEntry{}, nil
	}
	return treeEntry{mode: mode, oid: blob}, nil
}

// keptAs says whether ref holds p, as e when e is not the zero entry.
func keptAs(wtPath, ref, p string, e treeEntry) bool {
	if ref == "" {
		return false
	}
	got, err := entryAt(wtPath, ref, p)
	return err == nil && got.oid != "" && (e == treeEntry{} || got == e)
}

// keepDropped commits the entries, by path, on top of the commit an earlier
// drop of the same stop kept, and points a new ref of the run at it:
// refs/wt-sync-kept/<branch>/<epoch>-drop, -drop-2 and so on. The ref is
// created, never moved, so an earlier one is never overwritten.
func keepDropped(wtPath, gitDir string, st State, blobs map[string]treeEntry) (string, error) {
	index := filepath.Join(gitDir, fmt.Sprintf("wt-drop-index-%d", os.Getpid()))
	defer func() { _ = os.Remove(index) }()
	env := []string{"GIT_INDEX_FILE=" + index}
	var parents []string
	if st.DropKept != "" {
		if _, err := gitEnv(wtPath, env, nil, "read-tree", st.DropKept); err != nil {
			return "", err
		}
		parents = []string{"-p", st.DropKept}
	} else if _, err := gitEnv(wtPath, env, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(blobs))
	for p := range blobs {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if _, err := gitEnv(wtPath, env, nil, "update-index", "--add", "--cacheinfo", blobs[p].mode+","+blobs[p].oid+","+p); err != nil {
			return "", err
		}
	}
	tree, err := gitEnv(wtPath, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("wt sync resume --drop: the files kept at the stop at %s before trunk's side replaced them", git.ShortID(st.StoppedCommit, 7))
	args := append(append([]string{"commit-tree", tree}, parents...), "-m", msg)
	commit, err := gitEnv(wtPath, nil, nil, args...)
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf("%s%s/%d-drop", EditKeptPrefix, st.Branch, st.Epoch)
	for n := 1; n < 100; n++ {
		ref := base
		if n > 1 {
			ref = fmt.Sprintf("%s-%d", base, n)
		}
		if _, err := gitEnv(wtPath, nil, nil, "update-ref", "-m", "wt sync resume --drop", ref, commit, strings.Repeat("0", len(commit))); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("no free ref under %s", base)
}

// Lines is what the drop says, a line each: what it took and kept, and where
// the person's files are kept, with the command that shows each.
func (d Drop) Lines(wtPath string) []string {
	kept := "nothing of it, so the commit is dropped"
	if len(d.Kept) > 0 {
		kept = "its " + strings.Join(d.Kept, ", ")
	}
	lines := []string{fmt.Sprintf("dropped %s where trunk's %s reworked it: took trunk's %s; kept %s",
		git.ShortID(d.Commit, 7), git.ShortID(d.TrunkCommit, 7), strings.Join(d.Taken, ", "), kept)}
	if len(d.Strategy) > 0 {
		lines = append(lines, fmt.Sprintf("left as the strategies resolved them: %s", strings.Join(d.Strategy, ", ")))
	}
	for _, p := range d.Saved {
		lines = append(lines, fmt.Sprintf("%s as you left it is kept under %s: git -C %s show %s:%s", p, d.KeptRef, wtPath, d.KeptRef, p))
	}
	return lines
}
