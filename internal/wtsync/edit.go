package wtsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// EditStop is the commit a --stack --edit run stops at for a person to
// amend, and what they made of it.
type EditStop struct {
	// Commit is the commit as the line had it, full id.
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	// Amended is HEAD when the run went on from the edit stop: the amended
	// commit, or the last of the commits that replace it. Empty until then.
	Amended string `json:"amended,omitempty"`
}

// passed says the run has gone on from the edit stop.
func (e *EditStop) passed() bool { return e != nil && e.Amended != "" }

// EditKeptPrefix is where a --stack --edit run whose content was refused
// keeps the line as it was rebased, amend and resolutions included, before
// it puts the rebase back: refs/wt-sync-kept/<branch>/<epoch>.
const EditKeptPrefix = "refs/wt-sync-kept/"

// EditKeptRef names that ref for one branch in one run.
func EditKeptRef(branch string, epoch int64) string {
	return fmt.Sprintf("%s%s/%d", EditKeptPrefix, branch, epoch)
}

// editMarkerPath is where the sequence editor notes the commit of the edit,
// in the sequencer's own directory, so that git's abort or finish removes it
// with the rest and StripMarks keeps that one edit in the list.
func editMarkerPath(wtPath string) (string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wt-sync-edit"), nil
}

func readEditMarker(wtPath string) (string, error) {
	path, err := editMarkerPath(wtPath)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// isEdit says whether the stop p is the run's edit stop, not yet passed:
// git stopped after applying the commit the edit names.
func (d *driver) isEdit(p Progress) bool {
	e := d.req.Edit
	if e == nil || e.passed() || p.Command != "edit" || !p.Applied || p.Commit == "" {
		return false
	}
	full, err := commitOf(d.req.Path, p.Commit)
	return err == nil && full == e.Commit
}

// liftMarked says whether a lifting rule marked sha, so its pick has the
// lift's break before it and the lift runs at its edit stop.
func (d *driver) liftMarked(sha string) bool {
	for _, m := range d.liftMarks {
		if m == sha {
			return true
		}
	}
	return false
}

// editStop hands the edit stop over to a person: HEAD is the commit, nothing
// is unmerged. What a lift staged there is amended in first, as git's own
// continue would.
func (d *driver) editStop(p Progress) (Result, error) {
	if staged, err := d.git("diff", "--cached", "--name-only"); err != nil {
		return d.fail(err)
	} else if staged != "" {
		if _, err := d.git("commit", "--quiet", "--amend", "--no-edit", "--no-verify", "--no-gpg-sign"); err != nil {
			return d.fail(fmt.Errorf("amending the lift into %q: %w", p.Subject, err))
		}
	}
	h := &Handover{Index: p.Index, Total: p.Total, Commit: d.req.Edit.Commit, Subject: d.req.Edit.Subject,
		Staged: map[string]string{}, Edit: true}
	d.res.LayerState = d.layerState()
	if err := StripMarks(d.req.Path); err != nil {
		return d.fail(err)
	}
	d.res.Left = h
	return d.res, nil
}

// todoLeft says whether the sequencer's list still has a command to run.
func (d *driver) todoLeft() (bool, error) {
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
			return true, nil
		}
	}
	return false, nil
}

// atFinal is the content check of a --stack --edit run at the break that
// ends its list, before git finishes and moves the line's branch. A line
// whose content is refused is kept under EditKeptRef and the rebase put
// back: nothing moved, nothing is lost.
func (d *driver) atFinal() (refused bool, res Result, err error) {
	if d.req.Edit == nil || d.req.Content == nil {
		return false, d.res, nil
	}
	left, err := d.todoLeft()
	if err != nil || left {
		return false, d.res, err
	}
	head, err := d.git("rev-parse", "HEAD")
	if err != nil {
		return false, d.res, err
	}
	why := "the run never went on from the edit stop, so the line has nothing to be held to"
	if d.req.Edit.passed() {
		why = d.req.Content(head, *d.req.Edit, append(StopPaths(d.res.Stops), d.res.ByHand...))
	}
	if why == "" {
		return false, d.res, nil
	}
	res, err = d.refuseEdit(head, why)
	return true, res, err
}

// refuseEdit keeps head under EditKeptRef, aborts the rebase, which puts the
// line's branch back where it never moved from, and removes the handover
// and the stack's record: nothing of the run is left but its safety refs.
func (d *driver) refuseEdit(head, why string) (Result, error) {
	ref := EditKeptRef(d.req.Branch, d.req.Epoch)
	if _, err := gitEnv(d.mainRoot, nil, nil, "update-ref", "-m", "wt sync: the refused edit", ref, head); err != nil {
		return d.fail(fmt.Errorf("%s; and what it came to could not be kept under %s (%w), so the rebase is left as it is", why, ref, err))
	}
	kept := fmt.Sprintf("the line as rebased, %s kept under %s, holds the amend and every resolution", git.ShortID(head, 7), ref)
	if _, err := d.git("rebase", "--abort"); err != nil {
		return d.res, fmt.Errorf("%s; the rebase could not be put back (%w); %s", why, err, kept)
	}
	if busy, _ := RebaseInProgress(d.req.Path); busy {
		return d.res, fmt.Errorf("%s; a rebase is still in progress after the abort; %s", why, kept)
	}
	if now, err := d.git("rev-parse", "HEAD"); err != nil || now != d.old {
		return d.res, fmt.Errorf("%s; after the abort HEAD is %s, not %s; %s", why, git.ShortID(now, 7), git.ShortID(d.old, 7), kept)
	}
	d.res.Restored = true
	var notes []string
	if gitDir, err := GitDir(d.req.Path); err == nil {
		if err := RemovePlan(gitDir); err != nil {
			notes = append(notes, "its handover could not be removed: "+err.Error())
		}
	}
	if err := ForgetStack(d.mainRoot, d.req.Epoch); err != nil {
		notes = append(notes, "the stack's record could not be removed: "+err.Error())
	}
	msg := fmt.Sprintf("%s; no layer moved and the rebase is put back, %s at %s as before; %s", why, d.req.Branch, git.ShortID(d.old, 7), kept)
	if len(notes) > 0 {
		msg += "; " + strings.Join(notes, "; ")
	}
	return d.res, fmt.Errorf("%s", msg)
}

// editChecks keeps, of the commits to check, the edit's commit and those
// above it: nothing below the edit is replayed.
func (d *driver) editChecks(commits []string) ([]string, error) {
	e := d.req.Edit
	if e == nil || len(commits) == 0 {
		return commits, nil
	}
	out, err := d.git("rev-list", e.Commit+"^.."+d.req.Branch)
	if err != nil {
		return nil, err
	}
	from := map[string]bool{}
	for _, c := range strings.Fields(out) {
		from[c] = true
	}
	var kept []string
	for _, c := range commits {
		if from[c] {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// editMarks is what an edit run puts in the list: the edit of its commit,
// until the run has gone on from it, and the break that ends the list.
func (d *driver) editMarks() (edit string, final bool) {
	e := d.req.Edit
	if e == nil {
		return "", false
	}
	if !e.passed() {
		edit = e.Commit
	}
	return edit, true
}

// amendedPath is where a resume notes, before it goes on from the edit stop,
// what HEAD was there, so that a resume interrupted past it still knows
// what the commit was amended to. Kept in the sequencer's own directory.
func amendedPath(wtPath string) (string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wt-sync-amended"), nil
}

func noteAmended(wtPath, head string) error {
	path, err := amendedPath(wtPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(head+"\n"), 0o644)
}

func readAmended(wtPath string) (string, error) {
	path, err := amendedPath(wtPath)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// refreshAmend takes HEAD as what the edited commit was amended to when a
// resume finds the rebase at that commit's own check stop: the check that
// failed there is fixed by amending the commit again, and the line is then
// held to that amend, not to the one the run first went on from.
func (d *driver) refreshAmend() error {
	e := d.req.Edit
	if !e.passed() {
		return nil
	}
	sha, _, err := d.checkAt()
	if err != nil || sha != e.Commit {
		return err
	}
	head, err := d.git("rev-parse", "HEAD")
	if err != nil || head == e.Amended {
		return err
	}
	if err := noteAmended(d.req.Path, head); err != nil {
		return err
	}
	again := *e
	again.Amended = head
	d.req.Edit, d.res.Edit = &again, &again
	return nil
}
