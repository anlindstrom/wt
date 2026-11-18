package syncrun

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// How a layer was found, for --json.
const (
	LayerFoundTip   = "tipOnLine" // a local branch whose tip is on the line
	LayerFoundNamed = "named"     // named with --layer
)

// StackLayer is one layer of a stack held on one line, as --json reports
// it: what the look sees, and, from a run, what came of it. The run's fields
// are null in the look.
type StackLayer struct {
	Branch      string  `json:"branch"`
	OldTip      string  `json:"oldTip"`
	Commits     int     `json:"commits"`
	Subject     string  `json:"subject"`
	FoundBy     string  `json:"foundBy"`
	Worktree    *string `json:"worktree"`
	State       string  `json:"state"`
	StateDetail *string `json:"stateDetail"`
	// The run's: the new tip and its count from the new base, how its
	// marker landed, whether its change is the same patch, and the move.
	NewTip       *string  `json:"newTip"`
	CommitsAfter *int     `json:"commitsAfter"`
	Mapped       *string  `json:"mapped"`
	Patch        *string  `json:"patch"`
	PatchWhy     *string  `json:"patchWhy"`
	Result       *string  `json:"result"`
	How          *string  `json:"how"`
	Held         *string  `json:"held"`
	MoveCommand  *string  `json:"moveCommand"`
	PushCommand  []string `json:"pushCommand"`
	NoPushReason *string  `json:"noPushReason"`
	// Note is what else happened to a moved layer: a checkout of it that
	// landed during its move.
	Note *string `json:"note"`
}

// StackSkipped is a branch on the line the run does not take as a layer.
type StackSkipped struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip"`
	Reason string `json:"reason"`
}

// Stack is a stack held on one line, as --json reports it.
type Stack struct {
	Branch     string         `json:"branch"`
	Base       string         `json:"base"`
	Head       string         `json:"head"`
	Autosquash bool           `json:"autosquash"`
	Layers     []StackLayer   `json:"layers"`
	Skipped    []StackSkipped `json:"skipped"`
	// Refusal is why the run moved no layer: a check of the new tips, or a
	// fold that changed the line's content. Null in the look, and when the
	// layers were moved or held one by one.
	Refusal *string `json:"refusal"`
	// Edit is what a run with --edit did to the commit it stopped at; null
	// in the look and for any other run.
	Edit *StackEdit `json:"edit"`
	// ContextGaps is, for a fold (--autosquash), every hunk of a fixup
	// that does not apply behind its target, with the later commit that
	// carries its context; empty for any other.
	ContextGaps []StackContextGap `json:"contextGaps"`
	// ContextUnchecked is, for a fold, what of its fixups could not be
	// analysed: a fixup over an earlier one that conflicts, or the whole
	// analysis failing. Empty when every fixup was analysed.
	ContextUnchecked []string `json:"contextUnchecked"`
	// Restored says an edit run's line was put back at the tip the run
	// found, its layers refused: the participant is restored, not rebased.
	Restored bool `json:"-"`
}

// StackContextGap is one hunk of a fixup a fold moves ahead of its
// context, as --json reports it.
type StackContextGap struct {
	Fixup         string `json:"fixup"`
	FixupSubject  string `json:"fixupSubject"`
	Target        string `json:"target"`
	TargetSubject string `json:"targetSubject"`
	Path          string `json:"path"`
	Lines         string `json:"lines"`
	Needs         string `json:"needs"`
	NeedsSubject  string `json:"needsSubject"`
	Message       string `json:"message"`
}

// StackEdit is the commit a --edit run stopped at, what it was amended to,
// the paths the amend changed, and the commits above it the amend emptied.
type StackEdit struct {
	Commit  string        `json:"commit"`
	Subject string        `json:"subject"`
	Amended string        `json:"amended"`
	Paths   []string      `json:"paths"`
	Emptied []StackCommit `json:"emptied"`
}

// StackCommit is one commit of the line: its id and its subject.
type StackCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// StackOf is the look's report of a line: its layers and what holds each.
func StackOf(line wtsync.Line, autosquash bool, holders []wtsync.LayerHolder) *Stack {
	s := &Stack{Branch: line.Branch, Base: line.Base, Head: line.Head, Autosquash: autosquash,
		Layers: []StackLayer{}, Skipped: []StackSkipped{}, ContextGaps: []StackContextGap{}, ContextUnchecked: []string{}}
	for i, l := range line.Layers {
		found := LayerFoundTip
		if l.Named {
			found = LayerFoundNamed
		}
		sl := StackLayer{Branch: l.Branch, OldTip: l.Tip, Commits: l.Count, Subject: l.Subject, FoundBy: found,
			State: wtsync.HolderNone, PushCommand: nil}
		if i < len(holders) {
			h := holders[i]
			sl.State = h.State
			if h.Path != "" {
				sl.Worktree = jsonout.Strp(h.Path)
			}
			if h.Detail != "" {
				sl.StateDetail = jsonout.Strp(h.Detail)
			}
		}
		s.Layers = append(s.Layers, sl)
	}
	for _, k := range line.Skipped {
		s.Skipped = append(s.Skipped, StackSkipped{Branch: k.Branch, Tip: k.Tip, Reason: k.Why})
	}
	return s
}

// ContextGapsOf is wtsync.ContextGaps, replaced in a test.
var ContextGapsOf = wtsync.ContextGaps

// ReadHolders reads what holds each layer of line: its worktree, and in it
// tracked changes, a rebase, a session or a process.
func ReadHolders(ctx *kit.Context, line wtsync.Line, listed []agents.Agent) ([]wtsync.LayerHolder, error) {
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return nil, err
	}
	var scan *agents.ProcessScan
	var scanErr error
	for _, l := range line.Layers {
		if _, ok := worktrees.ByBranch(l.Branch); ok {
			scan, scanErr = agents.ScanHolders(listed)
			break
		}
	}
	holders := make([]wtsync.LayerHolder, len(line.Layers))
	for i, l := range line.Layers {
		holders[i] = wtsync.ReadHolder(l.Branch, worktrees, listed, scan, scanErr)
	}
	return holders, nil
}

// PrintLayers is the look's list of the layers, bottom first.
func PrintLayers(w io.Writer, s *Stack) {
	fmt.Fprintf(w, "  %s on the line of %s, bottom first:\n", kit.Count(len(s.Layers), "layer"), s.Branch)
	for _, l := range s.Layers {
		where := "no worktree"
		if l.Worktree != nil {
			where = l.State
			if l.StateDetail != nil {
				where += ": " + *l.StateDetail
			}
		}
		fmt.Fprintf(w, "    %-24s %s  %3d  %-50s  %s\n", l.Branch, git.ShortID(l.OldTip, 7), l.Commits, l.Subject, where)
	}
	for _, k := range s.Skipped {
		fmt.Fprintf(w, "    %-24s %s  not taken: %s\n", k.Branch, git.ShortID(k.Tip, 7), k.Reason)
	}
}

// StackFinish is what FinishStack needs: the run's record, the rebase's
// result, what it rebased onto, and how to move.
type StackFinish struct {
	Record *wtsync.StackRecord
	OldTip string // the line's tip before the rebase
	NewTip string // the line's tip after it, before any deferred step
	Onto   string
	Own    *wtsync.Own
	Listed []agents.Agent
	// ListErr is why the sessions could not be listed: every layer in a
	// worktree is then held, since who is in it is not known.
	ListErr error
	Force   bool
	Now     time.Time
	// Folds is the paths of the per-layer steps that folded their output
	// into the line: a fold in place may change them, and nothing else.
	Folds []string
	// Stopped is the paths the rebase stopped on, resolved by a strategy or
	// by a person.
	Stopped []string
	// AfterMove and Between are a test's hooks into the moves; nil in a
	// real run.
	AfterMove func(wtsync.LayerMove) error
	Between   func(stage string, m wtsync.LayerMove) error
}

// FinishStack checks every layer's new tip and moves the layers, once the
// rebase of the line has finished: in a run, and in the resume of one that
// was handed over. It prints a line per layer and returns the report and
// the failure the run ends with, empty when every layer moved. err is a
// move that could not be recorded, or moves stopped part way; the record on
// disk then says what moved.
func FinishStack(ctx *kit.Context, w io.Writer, in StackFinish) (s *Stack, failure string, err error) {
	rec, main := in.Record, ctx.Repo.MainRoot
	// What the line was rebased onto, as a layer it carries is said to be on.
	carrier := cmp.Or(rec.OntoRef, "trunk")
	holders := make([]wtsync.LayerHolder, len(rec.Line.Layers))
	for i, l := range rec.Line.Layers {
		holders[i] = wtsync.LayerHolder{Branch: l.Branch, State: wtsync.HolderNone}
	}
	s = StackOf(rec.Line, rec.Autosquash, holders)
	refuse := func(why string) (*Stack, string, error) {
		if rec.Edit != nil {
			// An edit run is never left rebased under layers that did not
			// follow: the line goes back where the run found it.
			why, s.Restored = putBackLine(main, rec, why)
		}
		fmt.Fprintf(w, "  ✗ no layer moved: %s\n", why)
		s.Refusal = jsonout.Strp(why)
		if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
			fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
		}
		return s, "no layer moved", nil
	}
	if rec.Edit != nil {
		// What the edit did, whatever comes of the layers; its content was
		// held to the old line with the amend merged in inside the rebase,
		// before the line's branch moved.
		e, eerr := stackEdit(main, rec, in.OldTip, in.NewTip)
		s.Edit = e
		printEdit(w, e)
		if eerr != nil {
			fmt.Fprintf(w, "      note: what the amend changed could not all be read: %v\n", eerr)
		}
	}
	if e := rec.Edit; e != nil {
		// Held inside the rebase already, before the line's branch moved;
		// held again here for a rebase a person finished by hand, which
		// never reached that check, before any layer moves.
		if e.Amended == "" {
			return refuse(fmt.Sprintf("the rebase went on from the edit stop by hand, so what %s was amended to is not known and the line cannot be checked", git.ShortID(e.Commit, 7)))
		}
		if why := EditContent(main, in.OldTip, in.Folds, nil)(in.NewTip, *e, in.Stopped); why != "" {
			return refuse(why)
		}
	}
	if (rec.Autosquash || rec.InPlace) && rec.Edit == nil {
		if why := foldChanged(main, rec.Line.Base, in.Onto, in.OldTip, in.NewTip, in.Folds, in.Stopped); why != "" {
			return refuse(fmt.Sprintf("the fold changed the line's content (%s); the line is folded, wt sync undo %s puts it back", why, rec.Work))
		}
	}
	if IsAlone(main, rec.Epoch) {
		// --alone: the line is rebased, and no layer is checked, moved or
		// held. Each was named when the run began.
		defer ClearAlone(main, rec.Epoch)
		if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
			fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
		}
		fmt.Fprintf(w, "  no layer moved: --alone leaves every layer on the old commits; wt sync undo %s puts the line back\n", rec.Work)
		return s, "", nil
	}
	plans := rec.Plans
	if rec.Edit != nil {
		// The commits the amend emptied were dropped: the plan follows.
		var rerr error
		if plans, rerr = wtsync.ReconcileEdit(main, in.Onto, plans, *rec.Edit); rerr != nil {
			return refuse(rerr.Error())
		}
	} else if rec.Upstream != "" {
		// Above an upstream git empties the picks the target already has:
		// the plan follows what the finished rebase dropped.
		var rerr error
		if plans, rerr = wtsync.ReconcileDropped(main, in.Onto, plans); rerr != nil {
			return refuse(rerr.Error())
		}
	}
	tips, cerr := wtsync.CheckLayers(main, in.NewTip, in.Onto, plans)
	if cerr != nil {
		return refuse(cerr.Error())
	}
	scan, scanErr := agents.ScanHolders(in.Listed)
	if in.ListErr != nil {
		scanErr = in.ListErr
	}
	merr := wtsync.MoveLayers(main, rec, tips, wtsync.MoveOptions{Listed: in.Listed, Scan: scan, ScanErr: scanErr,
		Force: in.Force, Now: in.Now, AfterMove: in.AfterMove, Between: in.Between})
	// A move that stopped, or needs recovery, still reports every layer:
	// what moved, what did not, and what to look at.
	held, moved := 0, 0
	oldBase, newBase := rec.Line.Base, in.Onto
	for i, t := range tips {
		m := rec.Moves[i]
		l := &s.Layers[i]
		l.NewTip, l.CommitsAfter, l.Mapped = jsonout.Strp(t.New), &t.Expect, jsonout.Strp(t.Mapped)
		l.Result, l.How = jsonout.Strp(m.Result), nil
		if m.How != "" {
			l.How = jsonout.Strp(m.How)
		}
		if m.Path != "" {
			l.Worktree = jsonout.Strp(m.Path)
		}
		same, perr := wtsync.SamePatch(main, oldBase, t.Old, newBase, t.New)
		patch, why := "same", ""
		switch {
		case perr != nil:
			patch, why = "unknown", perr.Error()
		case same:
		case rec.Edit != nil && t.Mapped == wtsync.MappedEmptied:
			patch, why = "changed", "the edit emptied every commit of it"
		case rec.Edit != nil && t.LeftOut > 0:
			patch, why = "changed", fmt.Sprintf("the edit emptied %s of it", kit.Count(t.LeftOut, "commit"))
		case rec.Edit != nil:
			patch, why = "changed", "the edit of "+git.ShortID(rec.Edit.Commit, 7)+" changed it, or a stop after it was resolved"
		case t.Mapped == wtsync.MappedEmptied:
			patch, why = "changed", carrier+" already carries all of its commits"
		case t.LeftOut > 0:
			patch, why = "changed", fmt.Sprintf("%s already carries %s of it", carrier, kit.Count(t.LeftOut, "commit"))
		case rec.Autosquash:
			patch, why = "changed", "a fixup was folded into it"
		default:
			patch, why = "changed", "the rebase changed it: a stop resolved, a version lifted, or "+carrier+"'s changes around it"
		}
		l.Patch = jsonout.Strp(patch)
		if why != "" {
			l.PatchWhy = jsonout.Strp(why)
		}
		mapped := map[string]string{wtsync.MappedTip: "", wtsync.MappedTipDropped: ", tip dropped: " + carrier + " carries it",
			wtsync.MappedEmptied: ", emptied: " + carrier + " carries every commit of it"}[t.Mapped]
		if rec.Edit != nil {
			mapped = map[string]string{wtsync.MappedTip: "", wtsync.MappedTipDropped: ", tip dropped: the edit emptied it",
				wtsync.MappedEmptied: ", emptied: the edit emptied every commit of it"}[t.Mapped]
		}
		switch m.Result {
		case wtsync.MoveMoved:
			how := "branch moved"
			if m.How == wtsync.MoveByWorktree {
				how = "worktree reset"
			}
			fmt.Fprintf(w, "  ✓ %s  %s → %s  %s%s; %s\n", m.Branch, git.ShortID(m.Old, 7), git.ShortID(m.New, 7), kit.Count(t.Expect, "commit"), mapped, how)
			moved++
			push, noPush := layerPush(in.Own, m.Branch, m.Path, main)
			l.PushCommand = push
			if noPush != "" {
				l.NoPushReason = jsonout.Strp(noPush)
				fmt.Fprintf(w, "      not pushed by this run, and no safe push: %s\n", noPush)
			} else {
				fmt.Fprintf(w, "      push: %s\n", strings.Join(push, " "))
			}
		case wtsync.MoveUnchanged:
			fmt.Fprintf(w, "  = %s  %s unchanged%s\n", m.Branch, git.ShortID(m.Old, 7), mapped)
		case wtsync.MoveHeld:
			held++
			fmt.Fprintf(w, "  ⏸ %s held: %s; to move it later: %s\n", m.Branch, m.Held, m.Command)
			l.Held, l.MoveCommand = jsonout.Strp(m.Held), jsonout.Strp(m.Command)
		case wtsync.MoveRecovery:
			fmt.Fprintf(w, "  ✗ %s needs recovery: %s\n", m.Branch, m.Held)
			l.Held = jsonout.Strp(m.Held)
		case wtsync.MovePlanned, wtsync.MoveMoving:
			fmt.Fprintf(w, "  · %s not moved: the moves stopped before it\n", m.Branch)
		}
		if m.Note != "" {
			fmt.Fprintf(w, "      note: %s\n", m.Note)
			l.Note = jsonout.Strp(m.Note)
		}
		oldBase, newBase = t.Old, t.New
	}
	if merr != nil {
		// The record stays: it says what moved, and undo reads it.
		return s, "", merr
	}
	if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
		fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
	}
	if moved > 0 {
		fmt.Fprintf(w, "  ↩ wt sync undo %s puts back the line and the %s it moved\n", rec.Work, kit.Count(moved, "layer"))
	}
	if held > 0 {
		return s, kit.Count(held, "layer") + " held", nil
	}
	return s, "", nil
}

// foldChanged is how a fold changed the line's content, outside the
// per-layer steps' paths (folds), which their output may change; "" for not
// at all, and never "" for a difference it cannot account for.
//
// A fold in place (onto is the line's own base) keeps the tree.
//
// A fold that moves the line onto another commit is held to what git merges
// the old line onto that commit with: every file that merges cleanly comes
// out as merged, so a hunk lost in a file the target also changed is caught.
// A file that does not merge cleanly and that a stop resolved, by a strategy
// or a person, is theirs in its conflicting region only; the rest of the
// merge must still be in it. The steps' own paths are the steps'. A moving
// fold is never accepted on the old tree alone.
func foldChanged(main, base, onto, oldTip, newTip string, folds, stopped []string) string {
	was, err1 := git.Run(main, "rev-parse", oldTip+"^{tree}")
	is, err2 := git.Run(main, "rev-parse", newTip+"^{tree}")
	if onto == base && err1 == nil && err2 == nil && was == is {
		return ""
	}
	tree := fmt.Sprintf("tree %s before, %s after", git.ShortID(was, 7), git.ShortID(is, 7))
	if err1 != nil || err2 != nil {
		return tree
	}
	if onto == base {
		paths, err := wtsync.ChangedPaths(main, was, newTip)
		if err != nil {
			return tree + ": " + err.Error()
		}
		for _, p := range paths {
			if !matchesAnyGlob(folds, p) {
				return tree + ", " + p + " among them"
			}
		}
		return ""
	}
	ref, conflicted, err := wtsync.MergedTree(main, base, onto, oldTip)
	if err != nil {
		return tree + ": the old line merged onto the target could not be read: " + err.Error()
	}
	paths, err := wtsync.ChangedPaths(main, ref, newTip)
	if err != nil {
		return tree + ": " + err.Error()
	}
	for _, p := range paths {
		switch {
		case matchesAnyGlob(folds, p):
		case slices.Contains(conflicted, p) && slices.Contains(stopped, p):
			// A stop resolved this file: its conflicting region is the
			// stop's, the rest of the merge must still be there.
			if why := lostMergedLines(main, ref, newTip, p); why != "" {
				return tree + ", " + p + ": " + why
			}
		default:
			return tree + ", " + p + " differs from the old line merged onto the target"
		}
	}
	return ""
}

// lostMergedLines is why newTip's p lost something of ref's p outside the
// conflicting regions ref carries markers for: a line of the merge that is
// not in the new file in order. "" when every such line is there.
func lostMergedLines(main, ref, newTip, p string) string {
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

func matchesAnyGlob(patterns []string, path string) bool {
	for _, g := range patterns {
		if wtsync.MatchGlob(g, path) {
			return true
		}
	}
	return false
}

// layerPush is the push of a moved layer: to the branch's own remote, with a
// lease on the commit wt last fetched there, or why there is no safe one.
func layerPush(own *wtsync.Own, branch, path, mainRoot string) ([]string, string) {
	if own == nil {
		return nil, "its own remote was not read"
	}
	to := own.Push(branch)
	if to.Remote == "" {
		return nil, to.Why
	}
	st := own.State(branch)
	switch st.State {
	case wtsync.OwnNone, wtsync.OwnGone:
		return nil, "there is no remote branch of its own yet"
	case wtsync.OwnBehind, wtsync.OwnDiverged:
		return nil, to.Ref() + " has commits this branch never had"
	case wtsync.OwnInSync, wtsync.OwnAhead, wtsync.OwnRebased:
	default:
		return nil, "it cannot be compared with " + to.Ref() + ": " + st.Why
	}
	dir := path
	if dir == "" {
		dir = mainRoot
	}
	return []string{"git", "-C", dir, "push", "--force-with-lease=refs/heads/" + to.Branch + ":" + st.Commit,
		to.Remote, "refs/heads/" + branch + ":refs/heads/" + to.Branch}, ""
}

// StackAfterRebase records the plan a rebase of the line read from its list,
// once the rebase has returned however it ended.
func StackAfterRebase(ctx *kit.Context, rec *wtsync.StackRecord, res wtsync.Result) error {
	if rec == nil {
		return nil
	}
	rec.Plans, rec.Replay = res.Layers, res.Replay
	return wtsync.WriteStackRecord(ctx.Repo.MainRoot, rec)
}
