package wtsync

import (
	"fmt"
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
// (Stopped).
type GuardInput struct {
	Base, Onto     string
	OldTip, NewTip string
	Folds, Stopped []string
}

// GuardResult is what TreeGuard found and what it let through to find it:
// Why is the difference it could not account for, "" when there is none;
// Excluded the declared step paths a changed file matched, and
// ConflictAllowed the conflicted files a stop resolved that differ from the
// merge, which it held only to keeping the merge's lines outside the conflict.
type GuardResult struct {
	Why             string
	Excluded        []string
	ConflictAllowed []string
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
// merge must still be in it. The steps' own paths are the steps'. A moving
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
	for _, p := range paths {
		switch {
		case excluded(p):
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
