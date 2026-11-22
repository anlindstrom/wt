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

// The tree guard holds a rebased line's content to the old line's, outside
// what a fold may change. It is a safety rule: a fold that loses a hunk must
// be refused before any layer moves, whoever runs it.

// GuardInput is what TreeGuard compares: the line's base and what it was
// rebased onto (equal for a fold in place), the line's tip before and after,
// the paths a per-layer step may change (Folds) and the paths a stop resolved
// (Stopped). Resolved is what a person resolved at the conflict stops wt
// handed over, which the guard takes as intended for exactly those paths.
type GuardInput struct {
	Base, Onto     string
	OldTip, NewTip string
	Folds, Stopped []string
	Resolved       []StopResolution
}

// GuardResult is what TreeGuard found and what it let through to find it:
// Why is the difference it could not account for, "" when there is none;
// Excluded the declared step paths a changed file matched, and
// ConflictAllowed the conflicted files a stop resolved that differ from the
// merge, which it held only to keeping the merge's lines outside the conflict.
// AtStop is the paths it took as a person resolved them at a stop, where the
// merge would have given other content.
type GuardResult struct {
	Why             string
	Excluded        []string
	ConflictAllowed []string
	AtStop          []string
}

// TreeGuard is how a fold changed the line's content, outside the per-layer
// steps' paths (Folds), which their output may change; Why is "" for not at
// all, and never "" for a difference it cannot account for.
//
// A fold in place (Onto is the line's own base) keeps the tree.
//
// A fold that moves the line onto another commit is held to what git merges
// the old line onto that commit with: every file that merges cleanly comes
// out as merged, so a hunk lost in a file the target also changed is caught.
// A file that does not merge cleanly and that a stop resolved, by a strategy
// or a person, is theirs in its conflicting region only; the rest of the
// merge must still be in it. A path a person resolved at a stop wt handed
// over (Resolved) is theirs whole, at exactly that commit: the content they
// resolved to, with what the old line changed in it after that commit, is
// what the new line must have there (checkResolved). The steps' own paths
// are the steps'. A moving
// fold is never accepted on the old tree alone.
func TreeGuard(main string, in GuardInput) (res GuardResult) {
	base, onto, oldTip, newTip, folds, stopped := in.Base, in.Onto, in.OldTip, in.NewTip, in.Folds, in.Stopped
	excluded := func(p string) bool {
		g := GlobMatching(folds, p)
		if g != "" && !slices.Contains(res.Excluded, g) {
			res.Excluded = append(res.Excluded, g)
		}
		return g != ""
	}
	was, err1 := git.Run(main, "rev-parse", oldTip+"^{tree}")
	is, err2 := git.Run(main, "rev-parse", newTip+"^{tree}")
	if onto == base && err1 == nil && err2 == nil && was == is {
		return res
	}
	tree := fmt.Sprintf("tree %s before, %s after", git.ShortID(was, 7), git.ShortID(is, 7))
	if err1 != nil || err2 != nil {
		res.Why = tree
		return res
	}
	if onto == base {
		paths, err := ChangedPaths(main, was, newTip)
		if err != nil {
			res.Why = tree + ": " + err.Error()
			return res
		}
		for _, p := range paths {
			if !excluded(p) {
				res.Why = tree + ", " + p + " among them"
				return res
			}
		}
		return res
	}
	ref, conflicted, err := MergedTree(main, base, onto, oldTip)
	if err != nil {
		res.Why = tree + ": the old line merged onto the target could not be read: " + err.Error()
		return res
	}
	paths, err := ChangedPaths(main, ref, newTip)
	if err != nil {
		res.Why = tree + ": " + err.Error()
		return res
	}
	// Every recorded path is held to its record, whether or not it differs
	// from the merge; a declared step path stays the step's.
	var taken []string
	for _, r := range lastResolutions(in.Resolved) {
		if GlobMatching(folds, r.Path) != "" {
			continue
		}
		if why := checkResolved(main, oldTip, newTip, r); why != "" {
			res.Why = tree + ", " + r.Path + ": " + why
			return res
		}
		taken = append(taken, r.Path)
	}
	for _, p := range paths {
		switch {
		case excluded(p):
		case slices.Contains(taken, p):
			res.AtStop = append(res.AtStop, p)
		case slices.Contains(conflicted, p) && slices.Contains(stopped, p):
			// A stop resolved this file: its conflicting region is the
			// stop's, the rest of the merge must still be there.
			if why := LostMergedLines(main, ref, newTip, p); why != "" {
				res.Why = tree + ", " + p + ": " + why
				return res
			}
			res.ConflictAllowed = append(res.ConflictAllowed, p)
		default:
			res.Why = tree + ", " + p + " differs from the old line merged onto the target"
			return res
		}
	}
	return res
}

// treeEntry is the mode and object id of a path in a tree; the zero value is
// a path the tree does not have.
type treeEntry struct{ mode, oid string }

// entryAt is treeish's entry for p, read NUL-delimited so a name git would
// quote is compared as itself.
func entryAt(main, treeish, p string) (treeEntry, error) {
	out, err := git.Run(main, "--literal-pathspecs", "ls-tree", "-z", "--full-tree", treeish, "--", p)
	if err != nil {
		return treeEntry{}, err
	}
	meta, path, ok := strings.Cut(strings.TrimSuffix(out, "\x00"), "\t")
	if !ok || path != p {
		return treeEntry{}, nil
	}
	f := strings.Fields(meta) // mode type oid
	if len(f) != 3 {
		return treeEntry{}, fmt.Errorf("%s: an entry git printed that is not mode, type and id", p)
	}
	return treeEntry{f[0], f[2]}, nil
}

// lastResolutions is each path's last record, in the order paths first
// appear: the last stop that resolved a path is the one its later commits sit
// on.
func lastResolutions(resolved []StopResolution) []StopResolution {
	var out []StopResolution
	for _, r := range resolved {
		if i := slices.IndexFunc(out, func(o StopResolution) bool { return o.Path == r.Path }); i >= 0 {
			out[i] = r
		} else {
			out = append(out, r)
		}
	}
	return out
}

// checkResolved is why newTip's path is not what a person resolved it to at
// a stop, carried over what the old line changed in it after that commit; ""
// when it is. A path whose expected content cannot be rebuilt is refused,
// never left to a looser check.
func checkResolved(main, oldTip, newTip string, r StopResolution) string {
	p := r.Path
	at, err1 := entryAt(main, r.Commit, p)
	old, err2 := entryAt(main, oldTip, p)
	now, err3 := entryAt(main, newTip, p)
	if err := errors.Join(err1, err2, err3); err != nil {
		return "what the stop resolved it to could not be compared: " + err.Error()
	}
	want := treeEntry{r.Mode, r.Blob}
	if r.Blob == "" {
		want = treeEntry{}
	}
	if old != at {
		// The old line changed p after the stop: its later changes go onto
		// what the person resolved.
		merged, err := carryLater(main, r, at, old)
		if err != nil {
			return "what the stop at " + git.ShortID(r.Commit, 7) + " resolved it to, with the old line's later changes, could not be rebuilt: " + err.Error()
		}
		want = merged
	}
	if now != want {
		return "differs from what the stop at " + git.ShortID(r.Commit, 7) + " resolved it to"
	}
	return ""
}

// carryLater is the entry of the path once the old line's later changes (from
// at, its entry in the stop's commit, to old, its entry at the old tip) are
// merged onto what the person resolved r to. It is an error when that does
// not merge cleanly or the path is added or deleted on either side.
func carryLater(main string, r StopResolution, at, old treeEntry) (treeEntry, error) {
	if r.Blob == "" || at.oid == "" || old.oid == "" || at.mode != old.mode || r.Mode != at.mode {
		return treeEntry{}, errors.New("the path is added, deleted or changed in mode by a later change")
	}
	dir, cleanup, err := tempDir("wtsync-carry-")
	if err != nil {
		return treeEntry{}, err
	}
	defer cleanup()
	var files [3]string
	for i, v := range []struct{ name, blob string }{{"ours", r.Blob}, {"base", at.oid}, {"theirs", old.oid}} {
		b, err := git.Exec(git.Opts{Dir: main}, "cat-file", "blob", v.blob)
		if err != nil {
			return treeEntry{}, err
		}
		files[i] = filepath.Join(dir, v.name)
		if err := os.WriteFile(files[i], b, 0o600); err != nil {
			return treeEntry{}, err
		}
	}
	if _, err := git.Run(main, "merge-file", files[0], files[1], files[2]); err != nil {
		return treeEntry{}, fmt.Errorf("the later changes do not merge onto it cleanly: %w", err)
	}
	id, err := git.Run(main, "hash-object", "--", files[0])
	if err != nil {
		return treeEntry{}, err
	}
	return treeEntry{r.Mode, id}, nil
}

// LostMergedLines is why newTip's p lost something of ref's p outside the
// conflicting regions ref carries markers for: a line of the merge that is
// not in the new file in order. "" when every such line is there.
func LostMergedLines(main, ref, newTip, p string) string {
	merged, err1 := git.Run(main, "show", ref+":"+p)
	now, err2 := git.Run(main, "show", newTip+":"+p)
	if err1 != nil || err2 != nil {
		return "the merged file or the new one could not be read, so it is not checked"
	}
	have := strings.Split(now, "\n")
	at, inConflict := 0, false
	for _, line := range strings.Split(merged, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			inConflict = true
			continue
		case strings.HasPrefix(line, ">>>>>>> "):
			inConflict = false
			continue
		case inConflict:
			continue
		}
		for at < len(have) && have[at] != line {
			at++
		}
		if at == len(have) {
			return fmt.Sprintf("the line %q of the merge is not in the resolution", line)
		}
		at++
	}
	return ""
}

// GlobMatching is the first pattern that matches path, "" for none.
func GlobMatching(patterns []string, path string) string {
	for _, g := range patterns {
		if MatchGlob(g, path) {
			return g
		}
	}
	return ""
}
