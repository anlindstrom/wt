package wtsync

import (
	"fmt"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// PutBackLine puts the line of an edit run back at the tip the run found,
// once its rebase finished and its layers were refused. What the line came
// to is kept first, under a ref created for it (KeepEditSnapshot). The line
// is put back only when its worktree has no tracked change, staged or not:
// a reset would lose a staged version the pin does not hold, so with one
// the line stays rebased and undo is named. restored says the line is back
// at the old tip; why is the refusal with what was done.
func PutBackLine(main string, rec *StackRecord, why string) (string, bool) {
	old, path, branch := rec.Line.Head, rec.Path, rec.Branch
	undo := fmt.Sprintf("wt sync undo %s puts it back", rec.Work)
	if ref, err := git.Run(path, "symbolic-ref", "--quiet", "HEAD"); err != nil || ref != "refs/heads/"+branch {
		return fmt.Sprintf("%s; the line is not put back: its worktree is not on %s; %s", why, branch, undo), false
	}
	head, err := git.Run(path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Sprintf("%s; the line is not put back (%v); %s", why, err, undo), false
	}
	if head == old {
		return why, true
	}
	kept, err := KeepEditSnapshot(main, branch, rec.Epoch, head)
	if err != nil {
		return fmt.Sprintf("%s; the line is not put back, since what it came to could not be kept (%v); %s", why, err, undo), false
	}
	keptLine := fmt.Sprintf("what it came to, %s kept under %s, holds the amend and every resolution", git.ShortID(head, 7), kept)
	// A status that cannot be read is not a clean one.
	status, err := git.Run(path, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	if err != nil || strings.TrimSpace(status) != "" {
		what := "it has tracked changes, which a reset would not keep"
		if err != nil {
			what = "its status could not be read (" + err.Error() + ")"
		}
		return fmt.Sprintf("%s; the line is not put back: %s; %s; %s", why, what, keptLine, undo), false
	}
	if _, err := git.Run(path, "reset", "--quiet", "--keep", old); err != nil {
		return fmt.Sprintf("%s; the line is not put back (%v); %s; %s", why, err, keptLine, undo), false
	}
	if now, err := git.Run(main, "rev-parse", "refs/heads/"+branch); err != nil || now != old {
		return fmt.Sprintf("%s; after the reset %s is at %s, not %s; %s", why, branch, git.ShortID(now, 7), git.ShortID(old, 7), keptLine), false
	}
	why = strings.TrimSuffix(why, "; no layer moved, the line itself is rebased")
	return fmt.Sprintf("%s; the line is put back, %s at %s as before; %s", why, branch, git.ShortID(old, 7), keptLine), true
}
