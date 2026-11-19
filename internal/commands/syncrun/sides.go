package syncrun

import (
	"fmt"
	"io"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// sideCount is what came of a run's side branches: how many moved, and how
// many were held that are not backups.
type sideCount struct{ moved, held int }

// carrySides carries the side branches of rec once its layers have moved,
// or, with leave, holds every one with the command that moves it; it prints
// a line for each and fills in s. forks is the fork plans brought in line
// with the rebase's drops. An error is a move that could not be recorded,
// or moves stopped part way: the record then says what moved.
func carrySides(main string, w io.Writer, s *Stack, rec *wtsync.StackRecord, in StackFinish, forks []wtsync.LayerPlan, leave string) (sideCount, error) {
	var n sideCount
	if len(rec.Sides) == 0 {
		return n, nil
	}
	opts := wtsync.CarryOptions{Head: in.NewTip, Onto: in.Onto, Forks: forks, Leave: leave}
	// A branch above the top goes onto the line's tip as it is now, after
	// any deferred step, which must still carry what the rebase made.
	top, err := git.Run(main, "rev-parse", "--verify", "refs/heads/"+rec.Branch)
	switch {
	case err != nil:
		opts.TopWhy = "the line's tip could not be read: " + err.Error()
	case !isAncestor(main, in.NewTip, top):
		opts.TopWhy = fmt.Sprintf("the line's tip %s no longer carries what the rebase made (%s)", git.ShortID(top, 7), git.ShortID(in.NewTip, 7))
	default:
		opts.Top = top
	}
	scan, scanErr := agents.ScanHolders(in.Listed)
	if in.ListErr != nil {
		scanErr = in.ListErr
	}
	opts.MoveOptions = wtsync.MoveOptions{Listed: in.Listed, Scan: scan, ScanErr: scanErr, Force: in.Force, Now: in.Now,
		AfterMove: in.AfterMove, Between: in.Between}
	carries, cerr := wtsync.CarrySides(main, rec, opts)
	byName := map[string]wtsync.SideCarry{}
	for _, c := range carries {
		byName[c.Side.Branch] = c
	}
	fmt.Fprintf(w, "  %s off the line:\n", countSides(len(rec.Sides)))
	for i := range s.SideBranches {
		x := &s.SideBranches[i]
		c, ok := byName[x.Branch]
		if !ok {
			why := "the moves stopped before it"
			x.Result, x.Held = jsonout.Strp(wtsync.MovePlanned), jsonout.Strp(why)
			fmt.Fprintf(w, "  · %s not moved: %s\n", x.Branch, why)
			continue
		}
		fillSide(x, c)
		m := c.Move
		switch m.Result {
		case wtsync.MoveMoved:
			n.moved++
			how := "branch moved"
			if m.How == wtsync.MoveByWorktree {
				how = "worktree reset"
			}
			fmt.Fprintf(w, "  ✓ %s  %s → %s  onto %s; %s\n", m.Branch, git.ShortID(m.Old, 7), git.ShortID(m.New, 7), sideOnto(main, c), how)
		case wtsync.MoveUnchanged:
			fmt.Fprintf(w, "  = %s  %s unchanged\n", m.Branch, git.ShortID(m.Old, 7))
		case wtsync.MoveHeld:
			switch {
			case c.Side.Backup:
				fmt.Fprintf(w, "  · %s  %s left where it is: %s\n", m.Branch, git.ShortID(m.Old, 7), m.Held)
			case m.Command != "":
				n.held++
				fmt.Fprintf(w, "  ⏸ %s held: %s; to move it later: %s\n", m.Branch, m.Held, m.Command)
			default:
				n.held++
				fmt.Fprintf(w, "  ⏸ %s held: %s\n", m.Branch, m.Held)
			}
		case wtsync.MoveRecovery:
			n.held++
			fmt.Fprintf(w, "  ✗ %s needs recovery: %s\n", m.Branch, m.Held)
		case wtsync.MovePlanned, wtsync.MoveMoving:
			fmt.Fprintf(w, "  · %s not moved: the moves stopped before it\n", m.Branch)
		}
		if m.Note != "" {
			fmt.Fprintf(w, "      note: %s\n", m.Note)
		}
	}
	return n, cerr
}

// sidesNotMoved marks every side branch of s as not moved, for why, and
// prints it once.
func sidesNotMoved(w io.Writer, s *Stack, why string) {
	if len(s.SideBranches) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s off the line not moved: %s\n", countSides(len(s.SideBranches)), why)
	for i := range s.SideBranches {
		x := &s.SideBranches[i]
		x.Carry, x.Left = false, jsonout.Strp(why)
		x.Result, x.Held = jsonout.Strp(wtsync.MoveHeld), jsonout.Strp(why)
	}
}

// fillSide sets the run's fields of x from what came of it.
func fillSide(x *StackSide, c wtsync.SideCarry) {
	m := c.Move
	x.Result = jsonout.Strp(m.Result)
	if c.NewBase != "" {
		x.NewBase = jsonout.Strp(c.NewBase)
	}
	if c.New != "" && m.Result != wtsync.MoveHeld {
		x.NewTip = jsonout.Strp(c.New)
	}
	if m.How != "" {
		x.How = jsonout.Strp(m.How)
	}
	if m.Path != "" {
		x.Worktree = jsonout.Strp(m.Path)
	}
	x.Carry = m.Result == wtsync.MoveMoved || m.Result == wtsync.MoveUnchanged
	x.Left = nil
	if m.Held != "" {
		x.Held = jsonout.Strp(m.Held)
		x.Left = jsonout.Strp(m.Held)
	}
	if m.Command != "" {
		x.MoveCommand = jsonout.Strp(m.Command)
	}
}

// sideOnto names where a moved side branch went: its new base and subject.
func sideOnto(main string, c wtsync.SideCarry) string {
	if out, err := git.Run(main, "log", "-1", "--format=%h %s", c.NewBase); err == nil {
		return out
	}
	return git.ShortID(c.NewBase, 7)
}

func countSides(n int) string {
	if n == 1 {
		return "1 side branch"
	}
	return fmt.Sprintf("%d side branches", n)
}

func isAncestor(main, a, b string) bool {
	_, err := git.Run(main, "merge-base", "--is-ancestor", a, b)
	return err == nil
}
