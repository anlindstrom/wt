package wtsync

import (
	"errors"
	"fmt"
	"slices"

	"github.com/anlindstrom/wt/internal/git"
)

// conflictedAt is the paths the pick the sequencer stopped at in p
// conflicted on, rebuilt with plumbing: the pick's own commit, a commit of
// the old line above the edit, merged onto HEAD, which at a conflict is
// what the pick is applied on and must be on top of where the handover left
// the rebase. Anything it cannot establish is an error, never a guess.
func (d *driver) conflictedAt(p Progress) ([]string, error) {
	if p.Commit == "" {
		return nil, errors.New("the sequencer names no commit it stopped at")
	}
	pick, err := commitOf(d.req.Path, p.Commit)
	if err != nil {
		return nil, fmt.Errorf("the commit it stopped at, %s, is not one here", p.Commit)
	}
	if _, code, err := gitEnvAllow(d.req.Path, nil, nil, 1, "merge-base", "--is-ancestor", pick, d.old); err != nil || code != 0 {
		return nil, fmt.Errorf("%s is not a commit of the line", git.ShortID(pick, 7))
	}
	head, err := d.git("rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	// HEAD is on top of where the handover left the rebase, or, past an
	// amend there, of the amend the sequencer's note or the handover names.
	var anchors []string
	for _, c := range []string{d.req.HandedHead, d.req.Edit.Amended} {
		if c != "" {
			anchors = append(anchors, c)
		}
	}
	if len(anchors) == 0 {
		return nil, errors.New("the handover recorded no HEAD to hold this stop to")
	}
	onTop := false
	for _, c := range anchors {
		if _, code, err := gitEnvAllow(d.req.Path, nil, nil, 1, "merge-base", "--is-ancestor", c, head); err == nil && code == 0 {
			onTop = true
			break
		}
	}
	if !onTop {
		return nil, fmt.Errorf("HEAD %s is on top of neither where the handover left the rebase nor the amend", git.ShortID(head, 7))
	}
	_, clean, conflicts, _, err := mergeTree(d.mainRoot, pick+"^", head, pick)
	if err != nil {
		return nil, err
	}
	if clean {
		return nil, fmt.Errorf("%s merges onto HEAD without a conflict, so this is not a stop at it", git.ShortID(pick, 7))
	}
	var paths []string
	for _, c := range conflicts {
		if !slices.Contains(paths, c.Path) {
			paths = append(paths, c.Path)
		}
	}
	return paths, nil
}
