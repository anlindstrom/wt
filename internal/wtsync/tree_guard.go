package wtsync

import (
	"bytes"
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
	// Upstream is where --upstream said the replayed commits start, "" without
	// it.
	Upstream       string
	Folds, Stopped []string
	Resolved       []StopResolution
	// Fixes is what a person changed at check stops, merged into the line the
	// result is compared to.
	Fixes []CheckFix
}

// GuardResult is what TreeGuard found and what it let through to find it:
// Why is the difference it could not account for, "" when there is none;
// Excluded the declared step paths a changed file matched, and
// ConflictAllowed the conflicted files a stop resolved that differ from the
// merge, which it held only to keeping the merge's lines outside the conflict.
// AtStop is the paths it took as a person resolved them at a stop, where the
// merge would have given other content.
type GuardResult struct {
	Why string
	// Err is set, with Why, when the check could not be made: not a
	// difference found in the line.
	Err error
	// Replayed says the line has move! commits and moved, so it was held to the
	// line replayed without them.
	Replayed        bool
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
//
// A moving fold of a line with move! commits is held to the old line replayed
// onto the target without them (movedLineTree), with what a person changed at
// check stops merged in, and to nothing a stop resolved: such a run has no
// conflict to resolve.
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
	ref, conflicted := was, []string(nil)
	differs := " differs from the old line merged onto the target"
	if onto != base {
		replayed, moved, why, err := movedLineTree(main, base, onto, in.Upstream, oldTip)
		switch {
		case err != nil:
			res.Why, res.Err = tree+": the old line replayed onto the target could not be read: "+err.Error(), err
			return res
		case why != "":
			res.Why = tree + ": " + why
			return res
		case moved:
			ref, res.Replayed = replayed, true
			differs = " differs from the old line replayed onto the target without the move! commits"
			in.Resolved, stopped = nil, nil
		default:
			if ref, conflicted, err = MergedTree(main, base, onto, oldTip); err != nil {
				res.Why = tree + ": the old line merged onto the target could not be read: " + err.Error()
				return res
			}
		}
	}
	// A fold in place stays held to the old tree: a change made at a check
	// stop is accepted only where the line moves onto another commit.
	var fixes []CheckFix
	if onto != base {
		fixes = in.Fixes
	}
	var recorded []string
	for _, r := range lastResolutions(in.Resolved) {
		recorded = append(recorded, r.Path)
	}
	fixed, why := applyFixes(main, ref, fixes, recorded)
	if why != "" {
		res.Why = tree + ": " + why
		return res
	}
	before, err := ChangedPaths(main, ref, newTip)
	if err != nil {
		res.Why = tree + ": " + err.Error()
		return res
	}
	paths, err := ChangedPaths(main, fixed, newTip)
	if err != nil {
		res.Why = tree + ": " + err.Error()
		return res
	}
	for _, p := range before {
		if !slices.Contains(paths, p) && !excluded(p) {
			res.AtStop = append(res.AtStop, p)
		}
	}
	ref = fixed
	if onto == base {
		for _, p := range paths {
			if !excluded(p) {
				res.Why = tree + ", " + p + " among them"
				return res
			}
		}
		return res
	}
	// Every recorded path is held to its record, whether or not it differs
	// from the merge; a declared step path stays the step's.
	var taken []string
	for _, r := range lastResolutions(in.Resolved) {
		if GlobMatching(folds, r.Path) != "" {
			continue
		}
		why, err := checkResolved(main, oldTip, newTip, r, fixes)
		if err != nil {
			res.Err = fmt.Errorf("%s: %w", r.Path, err)
			why = "could not be checked: " + err.Error()
		}
		if why != "" {
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
			res.Why = tree + ", " + p + differs
			return res
		}
	}
	return res
}

// StampStops gives what a resume found at its stop its place among the run's
// stops: after every record st already holds of another stop. Both lists are
// from the one stop.
func StampStops(st State, resolved []StopResolution, fixed []CheckFix) {
	order := 1
	for _, r := range st.Resolutions {
		if !st.resolvedHere(r) {
			order = max(order, r.Order+1)
		}
	}
	for _, f := range st.Fixes {
		if !st.fixedHere(f) {
			order = max(order, f.Order+1)
		}
	}
	for i := range resolved {
		resolved[i].Order = order
	}
	for i := range fixed {
		fixed[i].Order = order
	}
}

// SettledAt is st's records of the run's stops, with what a resume found at
// the stop st hands over in place of every record st holds of that stop. A
// resume cut after it kept them, before the rebase went on, leaves such
// records, and the stop is the same one: what is found there now, a fix
// amended since included, is what counts, once. A resume that finds nothing
// there now, the rebase gone on by hand, keeps what was kept. A file a drop
// took is kept unless what is found now names that file: a retry finds only
// the conflicts again, never the files the drop took that merged cleanly.
func (st State) SettledAt(resolved []StopResolution, fixed []CheckFix) ([]StopResolution, []CheckFix) {
	rs, fs := slices.Clone(st.Resolutions), slices.Clone(st.Fixes)
	if len(resolved)+len(fixed) == 0 {
		return rs, fs
	}
	rs = slices.DeleteFunc(rs, func(r StopResolution) bool {
		if !st.resolvedHere(r) {
			return false
		}
		// A file a drop took stays taken unless what is found now names it.
		return !r.Drop || slices.ContainsFunc(resolved, func(n StopResolution) bool { return n.Path == r.Path })
	})
	fs = slices.DeleteFunc(fs, st.fixedHere)
	return append(rs, resolved...), append(fs, fixed...)
}

// resolvedHere says whether r was recorded at the conflict stop st hands
// over: a pick stops on a conflict once in a rebase.
func (st State) resolvedHere(r StopResolution) bool {
	return st.Check == nil && st.StoppedCommit != "" && r.Commit == st.StoppedCommit
}

// fixedHere says whether f was recorded at the check stop st hands over: a
// later stop is at a HEAD the rebase made after it.
func (st State) fixedHere(f CheckFix) bool {
	return st.Check != nil && f.Stop == st.Head
}

// FixAtCheckStop is what a person changed at the check stop st handed over,
// as resume finds it: HEAD was the commit st.Head, and is now an amend of it
// or a commit added above it. Nothing is recorded unless the rebase is still
// at the stop wt left (AtHandoverStop), the stop is a failed check and not an
// edit, and HEAD's tree is not the tree that failed; otherwise the fold is
// checked as before.
func FixAtCheckStop(wtPath string, st State) []CheckFix {
	if st.Check == nil || st.AtEdit || st.Edit != nil || st.ByHand || !AtHandoverStop(wtPath, st) {
		return nil
	}
	before, err1 := git.Run(wtPath, "rev-parse", "--verify", st.Head+"^{tree}")
	after, err2 := git.Run(wtPath, "rev-parse", "--verify", "HEAD^{tree}")
	if err1 != nil || err2 != nil || before == after {
		return nil
	}
	fix := CheckFix{Stop: st.Head, Before: before, After: after}
	fix.Head, _ = git.Run(wtPath, "rev-parse", "--verify", "HEAD")
	if parent, err := git.Run(wtPath, "rev-parse", "--verify", "HEAD^"); err == nil && parent == st.Head {
		fix.Added, _ = git.Run(wtPath, "rev-parse", "--verify", "HEAD")
	}
	return []CheckFix{fix}
}

// applyFixes is ref with what a person changed at each check stop merged in,
// each change from the tree of the commit stopped at to the tree it was
// resumed with. A path a conflict stop resolved may not merge: its expected
// content is rebuilt from the resolution and the fixes (checkResolved), and
// the tree returned is not used for it. The reason a fix cannot be merged,
// "" when all were.
func applyFixes(main, ref string, fixes []CheckFix, resolved []string) (string, string) {
	for _, f := range fixes {
		merged, clean, conflicts, _, err := mergeTree(main, f.Before, ref, f.After)
		if err != nil {
			return "", "what was changed at the check stop at " + git.ShortID(f.Stop, 7) + " could not be merged into the old line: " + err.Error()
		}
		if !clean {
			for _, c := range conflicts {
				if !slices.Contains(resolved, c.Path) {
					return "", "what was changed at the check stop at " + git.ShortID(f.Stop, 7) + " does not merge cleanly into the old line"
				}
			}
		}
		ref = merged
	}
	return ref, ""
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
// never left to a looser check; err is a rebuild that failed to run.
func checkResolved(main, oldTip, newTip string, r StopResolution, fixes []CheckFix) (string, error) {
	p := r.Path
	at, err1 := entryAt(main, r.Commit, p)
	old, err2 := entryAt(main, oldTip, p)
	now, err3 := entryAt(main, newTip, p)
	if err := errors.Join(err1, err2, err3); err != nil {
		return "", fmt.Errorf("what the stop resolved it to could not be compared: %w", err)
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
			return refusedMerge("what the stop at "+git.ShortID(r.Commit, 7)+" resolved it to, with the old line's later changes, could not be rebuilt", err)
		}
		want = merged
	}
	for _, f := range fixes {
		if f.Order <= r.Order {
			// Made before this resolution, which is the newer word.
			continue
		}
		before, err1 := entryAt(main, f.Before, p)
		after, err2 := entryAt(main, f.After, p)
		if err := errors.Join(err1, err2); err != nil {
			return "", fmt.Errorf("what was changed at the check stop at %s could not be read: %w", git.ShortID(f.Stop, 7), err)
		}
		switch {
		case before == after || want == after:
			continue
		case want == before:
			// The resolution is exactly what the fix started from: the fix
			// is applied whole, a deletion or a mode change included.
			want = after
			continue
		}
		merged, err := mergeEntries(main, want, before, after)
		if err != nil {
			return refusedMerge("what was changed at the check stop at "+git.ShortID(f.Stop, 7)+" could not be merged onto what the stop resolved it to", err)
		}
		want = merged
	}
	if now != want {
		return "differs from what the stop at " + git.ShortID(r.Commit, 7) + " resolved it to", nil
	}
	return "", nil
}

// mergeRefusal is mergeEntries finding that a file does not merge: a
// conflict, or a side that adds or deletes it, changes its mode or has it
// binary. Any other error of mergeEntries is a failure to try.
type mergeRefusal string

func (r mergeRefusal) Error() string { return string(r) }

// refusedMerge is checkResolved's answer to err from a merge: why with it for
// a file that does not merge, err itself for anything else.
func refusedMerge(why string, err error) (string, error) {
	var refusal mergeRefusal
	if errors.As(err, &refusal) {
		return why + ": " + err.Error(), nil
	}
	return "", fmt.Errorf("%s: %w", why, err)
}

// carryLater is the entry of the path once the old line's later changes (from
// at, its entry in the stop's commit, to old, its entry at the old tip) are
// merged onto what the person resolved r to. It is an error when that does
// not merge cleanly or the path is added or deleted on either side.
func carryLater(main string, r StopResolution, at, old treeEntry) (treeEntry, error) {
	if r.Blob == "" || at.oid == "" || old.oid == "" || at.mode != old.mode || r.Mode != at.mode {
		return treeEntry{}, mergeRefusal("the path is added, deleted or changed in mode by a later change")
	}
	return mergeEntries(main, treeEntry{r.Mode, r.Blob}, at, old)
}

// mergeEntries is the entry of a file once the change from base to theirs is
// merged onto ours; a mergeRefusal when it does not merge cleanly or the file
// is added, deleted, binary or changed in mode on any side.
func mergeEntries(main string, ours, base, theirs treeEntry) (treeEntry, error) {
	if ours.oid == "" || base.oid == "" || theirs.oid == "" || ours.mode != base.mode || base.mode != theirs.mode {
		return treeEntry{}, mergeRefusal("the path is added, deleted or changed in mode")
	}
	dir, cleanup, err := tempDir("wtsync-carry-")
	if err != nil {
		return treeEntry{}, err
	}
	defer cleanup()
	var files [3]string
	for i, v := range []struct{ name, blob string }{{"ours", ours.oid}, {"base", base.oid}, {"theirs", theirs.oid}} {
		b, err := git.Exec(git.Opts{Dir: main}, "cat-file", "blob", v.blob)
		if err != nil {
			return treeEntry{}, err
		}
		// What git takes for binary, which merge-file does not merge.
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			return treeEntry{}, mergeRefusal("the file is binary")
		}
		files[i] = filepath.Join(dir, v.name)
		if err := os.WriteFile(files[i], b, 0o600); err != nil {
			return treeEntry{}, err
		}
	}
	if _, err := git.Run(main, "merge-file", files[0], files[1], files[2]); err != nil {
		// merge-file exits with the number of conflicts, up to 127.
		var failed *git.Error
		if errors.As(err, &failed) && failed.Code > 0 && failed.Code < 128 {
			return treeEntry{}, mergeRefusal("the later changes do not merge onto it cleanly: " + err.Error())
		}
		return treeEntry{}, err
	}
	// Written, since a later fix may merge onto it and read it back.
	id, err := git.Run(main, "hash-object", "-w", "--", files[0])
	if err != nil {
		return treeEntry{}, err
	}
	return treeEntry{ours.mode, id}, nil
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
