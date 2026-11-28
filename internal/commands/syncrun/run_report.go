package syncrun

import (
	"slices"

	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// What wt sync --json prints of a run, as --json names it.
const (
	// DetailSummary holds only the rows that need attention.
	DetailSummary = "summary"
	// DetailFull holds every row: --full.
	DetailFull = "full"
)

// patchChanged is a layer whose patch is not what it was, as --json says it.
const patchChanged = "changed"

// StackReport is what a run's result says of a stack beyond what the look
// does; nil in the look, whose object then has none of these fields.
type StackReport struct {
	Counts StackCounts `json:"counts"`
	// PushLaterCommand is the push of what the run left unpushed and kept a
	// record for, as an argv; null when it left no such record.
	PushLaterCommand []string `json:"pushLaterCommand"`
}

// StackCounts is every row of a participant's stack and layer steps, whether
// or not the result holds it.
type StackCounts struct {
	Layers           int            `json:"layers"`
	LayerResults     map[string]int `json:"layerResults"`
	PatchChanged     int            `json:"patchChanged"`
	SideBranches     int            `json:"sideBranches"`
	SideResults      map[string]int `json:"sideResults"`
	Pushed           int            `json:"pushed"`
	LayerStepResults map[string]int `json:"layerStepResults"`
}

// reported is p as the result holds it: its stack with its counts and, in a
// summary, only the rows that need attention. p itself is left as it is.
func reported(p *SyncParticipant, summary bool) *SyncParticipant {
	out := *p
	if p.Stack != nil {
		s := *p.Stack
		s.StackReport = &StackReport{Counts: countRows(&s, p.LayerSteps), PushLaterCommand: s.pushLater}
		if summary {
			// With no record saved, a layer's own command is all that says
			// how to push it. A record taken or replaced since is another
			// matter: the command would push what the branch holds now.
			s.Layers = only(s.Layers, func(l StackLayer) bool {
				return layerNeedsAttention(l) || s.unsaved && l.PushCommand != nil
			})
			s.SideBranches = only(s.SideBranches, sideNeedsAttention)
			if s.Push != nil {
				push := *s.Push
				push.Branches = only(push.Branches, func(b StackPushed) bool { return b.Outcome != PushedOK })
				s.Push = &push
			}
		}
		out.Stack = &s
		if note := s.recoveryNote(); note != "" {
			if out.Recovery != nil {
				note = *out.Recovery + "; " + note
			}
			out.Recovery = jsonout.Strp(note)
		}
	}
	if summary {
		out.LayerSteps = only(out.LayerSteps, stepNeedsAttention)
		out.Timings.Slowest = nil
	}
	return &out
}

// only is the rows that keep, never nil.
func only[T any](rows []T, keep func(T) bool) []T {
	return slices.DeleteFunc(append([]T{}, rows...), func(r T) bool { return !keep(r) })
}

func countRows(s *Stack, steps []LayerStepJSON) StackCounts {
	c := StackCounts{Layers: len(s.Layers), SideBranches: len(s.SideBranches),
		LayerResults: map[string]int{}, SideResults: map[string]int{}, LayerStepResults: map[string]int{}}
	for _, l := range s.Layers {
		if l.Result != nil {
			c.LayerResults[*l.Result]++
		}
		if str(l.Patch) == patchChanged {
			c.PatchChanged++
		}
	}
	for _, x := range s.SideBranches {
		if x.Result != nil {
			c.SideResults[*x.Result]++
		}
	}
	if s.Push != nil {
		for _, b := range s.Push.Branches {
			if b.Outcome == PushedOK {
				c.Pushed++
			}
		}
	}
	for _, x := range steps {
		c.LayerStepResults[x.Result]++
	}
	return c
}

// unsettled is a move that did not simply happen or leave the branch be.
func unsettled(result *string) bool {
	switch str(result) {
	case wtsync.MoveHeld, wtsync.MoveRecovery, wtsync.MoveMoving, wtsync.MovePlanned:
		return true
	}
	return false
}

// A layer with no result is one the run has not come to: what holds it is
// what it will meet.
func layerNeedsAttention(l StackLayer) bool {
	if l.Result == nil {
		return l.State != wtsync.HolderNone && l.State != wtsync.HolderClean
	}
	return unsettled(l.Result) || l.NoPushReason != nil || l.Note != nil || l.MoveCommand != nil
}

// A backup stays where it is by design.
func sideNeedsAttention(x StackSide) bool {
	return !x.Backup && (unsettled(x.Result) || x.Left != nil || x.MoveCommand != nil)
}

func stepNeedsAttention(x LayerStepJSON) bool {
	switch x.Result {
	case wtsync.LayerOwed, wtsync.LayerFailed:
		return true
	}
	return false
}
