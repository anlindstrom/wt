package syncrun

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// EditContent is the content check of a --stack --edit run, which the
// rebase runs at the end of its list before the line's branch moves. The
// line is held to its old tip with the edit's change merged in, from the
// commit as it was to what it was amended to: every file that merges
// cleanly so must come out as merged; a file a stop resolved (earlier,
// before a handover, or stopped, in this rebase) keeps the rest of the
// merge outside its conflicting regions; and the per-layer steps' paths
// (folds) are theirs. A file the one merge cannot merge that no stop
// resolved is held to the old line replayed onto the amend commit by
// commit, as the rebase replays it, and refused where that replay does not
// merge it either. For the case the edit is for, a commit above that already
// carried the change, the reference is the old tree itself.
func EditContent(main, oldTip string, folds, earlier []string) func(head string, edit wtsync.EditStop, stopped []string) string {
	return func(head string, edit wtsync.EditStop, stopped []string) string {
		why := editChanged(main, edit, oldTip, head, folds, append(slices.Clone(earlier), stopped...))
		if why == "" {
			return ""
		}
		return fmt.Sprintf("the line's content is not the old line's with the edit of %s merged in (%s)", git.ShortID(edit.Commit, 7), why)
	}
}

func editChanged(main string, edit wtsync.EditStop, oldTip, head string, folds, stopped []string) string {
	ref, conflicted, err := wtsync.MergedTree(main, edit.Commit, edit.Amended, oldTip)
	if err != nil {
		return "the old line with the amend merged in could not be read: " + err.Error()
	}
	paths, err := wtsync.ChangedPaths(main, ref, head)
	if err != nil {
		return err.Error()
	}
	var replayed string
	var replayConflicted []string
	var replayErr error
	simulated := false
	for _, p := range paths {
		switch {
		case matchesAnyGlob(folds, p):
		case slices.Contains(conflicted, p) && slices.Contains(stopped, p):
			// A stop resolved this file: its conflicting region is the
			// stop's, the rest of the merge must still be there.
			if why := lostMergedLines(main, ref, head, p); why != "" {
				return p + ": " + why
			}
		case slices.Contains(conflicted, p):
			if !simulated {
				replayed, replayConflicted, replayErr = wtsync.ReplayOnto(main, edit.Commit, edit.Amended, oldTip)
				simulated = true
			}
			switch {
			case replayErr != nil:
				return p + ": the old line replayed onto the amend could not be simulated: " + replayErr.Error()
			case slices.Contains(replayConflicted, p):
				return p + ": the old line replayed onto the amend does not merge it, and no stop resolved it"
			default:
				same, err := sameEntry(main, replayed, head, p)
				if err != nil {
					return p + ": the replayed file could not be compared with the new one: " + err.Error()
				}
				if !same {
					return p + " is not what the old line replayed onto the amend gives"
				}
			}
		default:
			return p + " differs from the old line with the amend merged in"
		}
	}
	return ""
}

// sameEntry says whether tree-ish a and b hold the same entry at p: mode,
// type and object id, or no entry in either. An entry that cannot be read
// is an error, never a match.
func sameEntry(main, a, b, p string) (bool, error) {
	entry := func(treeish string) (string, error) {
		return git.Run(main, "--literal-pathspecs", "ls-tree", "--full-tree", treeish, "--", p)
	}
	x, err := entry(a)
	if err != nil {
		return false, err
	}
	y, err := entry(b)
	if err != nil {
		return false, err
	}
	return x == y, nil
}

// EditReason is a handover's reason at the stop --edit asked for.
func EditReason(h *wtsync.Handover) string {
	return fmt.Sprintf("stopped at %s %q, as --edit asked: amend it, then resume", git.ShortID(h.Commit, 7), h.Subject)
}

// EditAtStop is what --json reports of an edit at its stop, before anything
// is amended: the commit, nothing amended yet, no paths, nothing emptied.
func EditAtStop(e *wtsync.EditStop) *StackEdit {
	return &StackEdit{Commit: e.Commit, Subject: e.Subject, Paths: []string{}, Emptied: []StackCommit{}}
}

// stackEdit is what --json reports of an edit: the commit, what it was
// amended to, the paths the amend changed, and the commits above it it
// emptied, which the rebase dropped.
func stackEdit(main string, rec *wtsync.StackRecord, oldTip, newTip string) (*StackEdit, error) {
	e := rec.Edit
	out := &StackEdit{Commit: e.Commit, Subject: e.Subject, Amended: e.Amended, Paths: []string{}, Emptied: []StackCommit{}}
	if e.Amended == "" {
		return out, nil
	}
	paths, err := wtsync.ChangedPaths(main, e.Commit, e.Amended)
	if err != nil {
		return out, err
	}
	out.Paths = append(out.Paths, paths...)
	slices.Sort(out.Paths)
	emptied, err := wtsync.Emptied(main, e.Commit, oldTip, rec.Line.Base, newTip)
	if err != nil {
		return out, err
	}
	for _, c := range emptied {
		out.Emptied = append(out.Emptied, StackCommit{SHA: c.SHA, Subject: c.Subject})
	}
	return out, nil
}

// printEdit is the end of an edit run's report: the commit, the paths its
// amend changed, and what it emptied.
func printEdit(w io.Writer, e *StackEdit) {
	paths := "nothing"
	if len(e.Paths) > 0 {
		paths = strings.Join(e.Paths, ", ")
	}
	fmt.Fprintf(w, "  ✎ edited %s %q: the amend changed %s\n", git.ShortID(e.Commit, 7), e.Subject, paths)
	for _, c := range e.Emptied {
		fmt.Fprintf(w, "      emptied and dropped: %s %q\n", git.ShortID(c.SHA, 7), c.Subject)
	}
}
