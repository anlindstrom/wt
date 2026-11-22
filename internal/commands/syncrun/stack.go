package syncrun

import (
	"cmp"
	"fmt"
	"io"
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
	// why is what holds its worktree, said short, for the look's table.
	why string
}

// How a side branch hangs off the line, for --json.
const (
	SideForked = wtsync.SideForked
	SideAbove  = wtsync.SideAbove
)

// StackSide is one branch that hangs off the line, as --json reports it:
// what the look sees, and, from a run, what came of it. The run's fields are
// null in the look.
type StackSide struct {
	Branch string `json:"branch"`
	// Kind is forked (from a commit of the line below its top) or above
	// (from the top itself).
	Kind   string `json:"kind"`
	OldTip string `json:"oldTip"`
	// Fork is the commit of the line it leaves the line at.
	Fork        string `json:"fork"`
	ForkSubject string `json:"forkSubject"`
	// On is the side branch it is stacked on; null when it sits on the line.
	On *string `json:"on"`
	// Base is where its own commits start, Commits how many there are.
	Base        string  `json:"base"`
	Commits     int     `json:"commits"`
	Backup      bool    `json:"backup"`
	Worktree    *string `json:"worktree"`
	State       string  `json:"state"`
	StateDetail *string `json:"stateDetail"`
	// Carry says the run carries it; Left says why not, null when it does.
	Carry bool    `json:"carry"`
	Left  *string `json:"left"`
	// The run's: where it was put, its new tip, the move, and for a held
	// one why and the command that moves it.
	NewBase     *string `json:"newBase"`
	NewTip      *string `json:"newTip"`
	Result      *string `json:"result"`
	How         *string `json:"how"`
	Held        *string `json:"held"`
	MoveCommand *string `json:"moveCommand"`
	// SameTip is the other side branches at its very tip, by name: siblings,
	// carried or held together.
	SameTip []string `json:"sameTip"`
	// frees is, when what holds its worktree is why it is left, what frees
	// it, and ownBy says that holder is why; the look's line says it once.
	frees string
	ownBy bool
	// byHand says no run gives it a command: a merge commit of its own.
	// with is the branch at its tip it moves with: held, it is held with
	// that one, and moved by hand after it.
	byHand bool
	with   string
}

// StackTop is the line's own branch, the top of the stack: the last row of
// the look's layer table. It is not one of the layers, which are what a run
// moves after its rebase; the rebase itself moves the top.
type StackTop struct {
	Branch   string  `json:"branch"`
	OldTip   string  `json:"oldTip"`
	Commits  int     `json:"commits"`
	Subject  string  `json:"subject"`
	Worktree *string `json:"worktree"`
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
	Top        *StackTop      `json:"top"`
	// SideBranches is the branches that hang off the line, parents first:
	// forked from a commit of it, or above its top. Empty with
	// --no-side-branches.
	SideBranches []StackSide `json:"sideBranches"`
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
	// Fold is what a fold's tree guard compared, for the run's own
	// `fold` field; it is not part of the look, so it is not serialized here.
	Fold *StackFold `json:"-"`
	// Push is what a run that pushed the line's moved layers did: one atomic
	// push per remote, each branch with its lease and the commit pushed. Null
	// in the look, and for a run that pushed nothing.
	Push *StackPush `json:"push"`
	// items is the moved layers that have somewhere safe to push, top the
	// line's own branch's push, and work the worktree the line is held on:
	// what PushStack takes.
	items []stackItem
	top   *wtsync.BranchPush
	work  string
	// declined is why the stack is not pushed as one, when it is not.
	declined string
}

// StackFold is, for a run that folds the line (--autosquash, --edit or
// wt sync steps --stack), what it folded and what its tree guard held it
// to; the run's participant carries it as `fold`.
type StackFold struct {
	// InPlace says the line is folded on its own base, not moved onto a
	// target: the run's `target` then says only where the declaration was
	// read from and what the line was assessed against.
	InPlace bool `json:"inPlace"`
	// Base is the line's own base: the commit it is folded on when InPlace.
	Base string `json:"base"`
	// Guard is the tree guard's comparison; null when none ran (the run has
	// not finished, or it was an edit).
	Guard *FoldGuard `json:"guard"`
}

// FoldGuard is what the guard compared and how it came out.
type FoldGuard struct {
	// OldTip and NewTip are the line's tips before and after the rebase.
	OldTip string `json:"oldTip"`
	NewTip string `json:"newTip"`
	// Onto is the commit a moving fold's old line was merged onto to compare;
	// null for a fold in place, which is compared with the old tip itself.
	Onto *string `json:"onto"`
	// ExcludedGlobs is the declared per-layer step paths a changed file
	// matched, which the guard left out of the comparison.
	ExcludedGlobs []string `json:"excludedGlobs"`
	// ConflictAllowed is, for a moving fold, the files that conflicted when
	// merged and were resolved at a stop: only the lines of the merge outside
	// the conflicting regions must still be in them, in order.
	ConflictAllowed []string `json:"conflictAllowed"`
	// Outcome is passed or failed; Reason is why it failed, null when it
	// passed.
	Outcome string  `json:"outcome"`
	Reason  *string `json:"reason"`
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

// StackOf is the look's report of a line: its layers and what holds each,
// and its top, the line's own branch, checked out at path.
func StackOf(line wtsync.Line, autosquash bool, holders []wtsync.LayerHolder, path string) *Stack {
	s := &Stack{Branch: line.Branch, Base: line.Base, Head: line.Head, Autosquash: autosquash,
		Layers: []StackLayer{}, Skipped: []StackSkipped{}, SideBranches: []StackSide{}, ContextGaps: []StackContextGap{}, ContextUnchecked: []string{},
		Top: &StackTop{Branch: line.Branch, OldTip: line.Head, Commits: len(line.Commits), Subject: line.Subject}}
	if path != "" {
		s.Top.Worktree = jsonout.Strp(path)
	}
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
			sl.why = wtsync.HolderWhy(h)
		}
		s.Layers = append(s.Layers, sl)
	}
	for _, k := range line.Skipped {
		s.Skipped = append(s.Skipped, StackSkipped{Branch: k.Branch, Tip: k.Tip, Reason: k.Why})
	}
	return s
}

// SetSides lists sides on s as the look sees them, with what holds each
// (holders, in the same order) and whether the run would carry it: not a
// backup, a branch with a merge commit or on more than one branch, one a
// worktree holds, one at the same tip as a branch that is held, one on a
// branch that is not carried, and none with leave (why the run moves no
// side branch, such as --alone).
func SetSides(s *Stack, sides []wtsync.SideBranch, holders []wtsync.LayerHolder, leave string) {
	own := make([]string, len(sides))
	for i, x := range sides {
		side := StackSide{Branch: x.Branch, Kind: x.Kind, OldTip: x.Tip, Fork: x.Fork, ForkSubject: x.ForkSubject,
			Base: x.Base, Commits: x.Commits, Backup: x.Backup, State: wtsync.HolderNone, SameTip: append([]string{}, x.SameTip...),
			byHand: x.Merges, with: x.With}
		if x.On != "" {
			side.On = jsonout.Strp(x.On)
		}
		if i < len(holders) {
			h := holders[i]
			side.State = h.State
			if h.Path != "" {
				side.Worktree = jsonout.Strp(h.Path)
			}
			if h.Detail != "" {
				side.StateDetail = jsonout.Strp(h.Detail)
			}
		}
		switch {
		case x.Left != "":
			own[i] = x.Left
		case leave != "":
			own[i] = leave
		case i < len(holders) && holders[i].Holds():
			own[i] = wtsync.HolderWhy(holders[i])
			side.frees, side.ownBy = wtsync.HolderFrees(holders[i]), true
		}
		s.SideBranches = append(s.SideBranches, side)
	}
	// Branches at one tip move together: one held holds the rest.
	held := map[string]string{}
	for i, x := range sides {
		if own[i] != "" && !x.Backup {
			held[x.Branch] = own[i]
		}
	}
	carried := map[string]bool{}
	for i, x := range sides {
		side := &s.SideBranches[i]
		why := own[i]
		if why == "" && !x.Backup {
			for _, t := range x.SameTip {
				if h, ok := held[t]; ok {
					why = "it is the same commit as " + t + ", which is held (" + h + "): they move together"
					break
				}
			}
		}
		if why == "" && x.On != "" && !carried[x.On] {
			why = "it is stacked on " + x.On + ", which is not carried"
		}
		if why != "" {
			side.Left = jsonout.Strp(why)
		} else {
			side.Carry, carried[x.Branch] = true, true
		}
	}
}

// ReadSideHolders reads what holds each of sides, as ReadHolders does for
// the layers.
func ReadSideHolders(ctx *kit.Context, sides []wtsync.SideBranch, listed []agents.Agent) ([]wtsync.LayerHolder, error) {
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return nil, err
	}
	var scan *agents.ProcessScan
	var scanErr error
	for _, x := range sides {
		if _, ok := worktrees.ByBranch(x.Branch); ok {
			scan, scanErr = agents.ScanHolders(listed)
			break
		}
	}
	holders := make([]wtsync.LayerHolder, len(sides))
	for i, x := range sides {
		holders[i] = wtsync.ReadHolder(x.Branch, worktrees, listed, scan, scanErr)
	}
	return holders, nil
}

// ContextGaps is, for a fold of line, the hunks of its fixups that lack
// their context behind their targets, set on s as --json reports them, with
// what could not be analysed, and returned for the rebase. An analysis that
// fails is noted on s and the look or run goes on without: the gaps are a
// warning. PrintLayers says both.
func ContextGaps(main string, line wtsync.Line, s *Stack) []wtsync.ContextGap {
	gaps, unchecked, err := ContextGapsOf(main, line.Commits)
	if err != nil {
		s.ContextUnchecked = append(s.ContextUnchecked, "which fixups lack their context could not be read: "+err.Error())
		return nil
	}
	s.ContextGaps = GapsJSON(gaps)
	s.ContextUnchecked = append(s.ContextUnchecked, unchecked...)
	return gaps
}

// ContextGapsOf is wtsync.ContextGaps, replaced in a test.
var ContextGapsOf = wtsync.ContextGaps

// GapsJSON is gaps as --json reports them.
func GapsJSON(gaps []wtsync.ContextGap) []StackContextGap {
	out := []StackContextGap{}
	for _, g := range gaps {
		out = append(out, StackContextGap{Fixup: g.Fixup, FixupSubject: g.FixupSubject, Target: g.Target,
			TargetSubject: g.TargetSubject, Path: g.Path, Lines: g.Lines, Needs: g.Needs, NeedsSubject: g.NeedsSubject, Message: g.Line()})
	}
	return out
}

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
			where = cmp.Or(l.why, layerState(l))
		}
		fmt.Fprintf(w, "    %-24s %s  %3d  %-50s  %s\n", l.Branch, git.ShortID(l.OldTip, 7), l.Commits, l.Subject, where)
	}
	if t := s.Top; t != nil {
		fmt.Fprintf(w, "    %-24s %s  %3d  %-50s  %s\n", t.Branch, git.ShortID(t.OldTip, 7), t.Commits, t.Subject, "the top: the rebase moves it")
	}
	for _, k := range s.Skipped {
		fmt.Fprintf(w, "    %-24s %s  not taken: %s\n", k.Branch, git.ShortID(k.Tip, 7), k.Reason)
	}
	if len(s.SideBranches) > 0 {
		fmt.Fprintf(w, "  %s off the line:\n", countSides(len(s.SideBranches)))
	}
	for _, x := range s.SideBranches {
		fmt.Fprintf(w, "    %-24s %s  %s\n", x.Branch, git.ShortID(x.OldTip, 7), sideWhere(x))
	}
	for _, g := range s.ContextGaps {
		fmt.Fprintf(w, "  ⚠ %s %q: %s\n", git.ShortID(g.Fixup, 7), g.FixupSubject, g.Message)
	}
	if len(s.ContextGaps) > 0 {
		fmt.Fprintf(w, "    the fold stops there or loses the hunk: move each hunk into a fixup of the commit named, or resolve the stop with it kept\n")
	}
	for _, n := range s.ContextUnchecked {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
}

// sideWhere is the look's line for a side branch: where it hangs off the
// line, its own commits, its worktree, and whether the run carries it.
func sideWhere(x StackSide) string {
	where := "forks at " + git.ShortID(x.Fork, 7) + " " + x.ForkSubject
	switch {
	case x.On != nil:
		where = "on " + *x.On
	case x.Kind == SideAbove:
		where = "above the top"
	}
	where += ", " + kit.Count(x.Commits, "commit")
	if len(x.SameTip) > 0 {
		where += ", same commit as " + strings.Join(x.SameTip, ", ")
	}
	// What holds its worktree, said once: in the reason when it is why the
	// branch is held.
	if x.Worktree != nil && !x.ownBy {
		where += ", " + x.State
		if x.StateDetail != nil {
			where += ": " + *x.StateDetail
		}
	}
	switch {
	case x.Backup:
		return where + "; a backup: stays where it is"
	case x.Left != nil && x.with != "" && x.frees != "":
		return where + "; held: " + *x.Left + " — " + x.frees + "; otherwise a run holds it with " + x.with + ": " + wtsync.TwinByHand(x.with)
	case x.Left != nil && x.with != "":
		return where + "; held: " + *x.Left + "; a run holds it with " + x.with + ": " + wtsync.TwinByHand(x.with)
	case x.Left != nil && x.frees != "":
		return where + "; held: " + *x.Left + " — " + x.frees + "; otherwise a run holds it and prints the command to move it later"
	case x.Left != nil && x.byHand:
		return where + "; held: " + *x.Left
	case x.Left != nil:
		return where + "; held: " + *x.Left + "; a run holds it and prints the command to move it later"
	}
	return where + "; carried"
}

// layerState is a layer's state as --json has it, with its detail.
func layerState(l StackLayer) string {
	if l.StateDetail != nil {
		return l.State + ": " + *l.StateDetail
	}
	return l.State
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
	// Dropped is the commits the finished rebase dropped as empty, as
	// ReportReplay names them, and Confirmed says wt drove that rebase from
	// start to end: only then are the drops it is certain of taken out of
	// the plan.
	Dropped   []wtsync.Dropped
	Confirmed bool
	// Gaps is, for a fold, the fixup hunks named before the rebase as
	// lacking their context: the report keeps them.
	Gaps []wtsync.ContextGap
	// Unchecked is what of the fold's fixups could not be analysed: the
	// report keeps it with the gaps.
	Unchecked []string
	// AfterMove and Between are a test's hooks into the moves; nil in a
	// real run.
	AfterMove func(wtsync.LayerMove) error
	Between   func(stage string, m wtsync.LayerMove) error
}

// StackFinishCaller is what a run and a resume each decide for themselves
// when they finish a stack: the tip the line was rebased from, who owns the
// branch, the paths the rebase stopped on, whether wt drove the replay (the
// tree guard's strictness depends on it), and the fold's report inputs. No
// field has a default.
type StackFinishCaller struct {
	OldTip    string
	Own       *wtsync.Own
	Stopped   []string
	Confirmed bool
	Gaps      []wtsync.ContextGap
	Unchecked []string
	// AfterMove and Between are a test's hooks; nil in a real run and a resume.
	AfterMove func(wtsync.LayerMove) error
	Between   func(stage string, m wtsync.LayerMove) error
}

// StackFinishFor builds the StackFinish a run and a resume both write: what
// the rebase and the sessions listed after it say, and c what only the caller
// knows.
func StackFinishFor(rec *wtsync.StackRecord, res wtsync.Result, cfg *wtsync.Config, now time.Time, listed []agents.Agent, listErr error, c StackFinishCaller) StackFinish {
	return StackFinish{Record: rec, OldTip: c.OldTip, NewTip: res.NewTip,
		Onto: rec.Onto, Own: c.Own, Listed: listed, ListErr: listErr, Force: rec.Force, Now: now, AfterMove: c.AfterMove, Between: c.Between,
		Folds: FoldPaths(cfg), Stopped: c.Stopped, Dropped: res.Emptied, Confirmed: c.Confirmed, Gaps: c.Gaps, Unchecked: c.Unchecked}
}

// FinishStack checks every layer's new tip and moves the layers, once the
// rebase of the line has finished: in a run, and in the resume of one that
// was handed over. It prints a line per layer and returns the report and
// the failure the run ends with, empty when every layer moved. err is a
// move that could not be recorded, or moves stopped part way; the record on
// disk then says what moved.
func FinishStack(ctx *kit.Context, w io.Writer, in StackFinish) (s *Stack, failure string, err error) {
	rec, main := in.Record, ctx.Repo.MainRoot
	holders := make([]wtsync.LayerHolder, len(rec.Line.Layers))
	for i, l := range rec.Line.Layers {
		holders[i] = wtsync.LayerHolder{Branch: l.Branch, State: wtsync.HolderNone}
	}
	s = StackOf(rec.Line, rec.Autosquash, holders, rec.Path)
	SetSides(s, rec.Sides, nil, "")
	s.ContextGaps = GapsJSON(in.Gaps)
	s.ContextUnchecked = append(s.ContextUnchecked, in.Unchecked...)
	s.work = rec.Work
	top := pushOf(rec, rec.Line.Branch)
	s.top = &top
	if len(rec.Pushes) == 0 {
		s.declined = "the run's record has no leases (an older wt wrote it), and a lease read now would be on whatever was fetched since"
	}
	f := &stackFinisher{w: w, main: main, rec: rec, in: in, s: s}
	if why := f.guardEdit(); why != "" {
		return f.refuse(why)
	}
	s.Fold = FoldOf(rec)
	if why := f.guardFold(); why != "" {
		return f.refuse(why)
	}
	forks := f.followDrops(rec.Forks)
	if IsAlone(main, rec.Epoch) {
		return f.alone(forks)
	}
	plans, why := f.followPlans()
	if why != "" {
		return f.refuse(why)
	}
	tips, why := f.verifyTips(plans)
	if why != "" {
		return f.refuse(why)
	}
	merr := f.moveLayers(tips)
	held, moved := f.reportLayers(tips)
	return f.moveSides(forks, merr, held, moved)
}

// stackFinisher is what FinishStack's phases share: the run's record and
// what the rebase came to, the report being built, and where it prints.
type stackFinisher struct {
	w    io.Writer
	main string
	rec  *wtsync.StackRecord
	in   StackFinish
	s    *Stack
}

// refuse ends the finish with no layer moved. An edit run is never left
// rebased under layers that did not follow: the line goes back where the run
// found it.
func (f *stackFinisher) refuse(why string) (*Stack, string, error) {
	w, s, rec, main := f.w, f.s, f.rec, f.main
	if rec.Edit != nil {
		why, s.Restored = wtsync.PutBackLine(main, rec, why)
	}
	fmt.Fprintf(w, "  ✗ no layer moved: %s\n", why)
	s.Refusal = jsonout.Strp(why)
	sidesNotMoved(w, s, "no layer moved, and no side branch")
	if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
		fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
	}
	return s, "no layer moved", nil
}

// guardEdit reports what an edit run's amend did and holds the line to it;
// the reason it cannot be accepted, "" for accepted or not an edit run.
func (f *stackFinisher) guardEdit() string {
	w, s, rec, main, in := f.w, f.s, f.rec, f.main, f.in
	if rec.Edit == nil {
		return ""
	}
	// What the edit did, whatever comes of the layers; its content was
	// held to the old line with the amend merged in inside the rebase,
	// before the line's branch moved.
	e, eerr := stackEdit(main, rec, in.OldTip, in.NewTip)
	s.Edit = e
	printEdit(w, e)
	if eerr != nil {
		fmt.Fprintf(w, "      note: what the amend changed could not all be read: %v\n", eerr)
	}
	// Held inside the rebase already, before the line's branch moved;
	// held again here for a rebase a person finished by hand, which
	// never reached that check, before any layer moves.
	stop := rec.Edit
	if stop.Amended == "" {
		return fmt.Sprintf("the rebase went on from the edit stop by hand, so what %s was amended to is not known and the line cannot be checked", git.ShortID(stop.Commit, 7))
	}
	return EditContent(main, in.OldTip, in.Folds, nil)(in.NewTip, *stop, in.Stopped)
}

// guardFold holds a fold's result to the line's content, the tree guard, and
// records it as fold.guard; the reason it failed, "" for passed or no fold.
func (f *stackFinisher) guardFold() string {
	w, s, rec, main, in := f.w, f.s, f.rec, f.main, f.in
	if (!rec.Autosquash && !rec.InPlace) || rec.Edit != nil {
		return ""
	}
	res := foldCheck(main, rec.Line.Base, in.Onto, in.OldTip, in.NewTip, in.Folds, in.Stopped)
	g := &FoldGuard{OldTip: in.OldTip, NewTip: in.NewTip, ExcludedGlobs: nonNil(res.Excluded),
		ConflictAllowed: nonNil(res.ConflictAllowed), Outcome: "passed"}
	if !s.Fold.InPlace {
		g.Onto = jsonout.Strp(in.Onto)
	}
	s.Fold.Guard = g
	if res.Why != "" {
		g.Outcome, g.Reason = "failed", jsonout.Strp(res.Why)
		return fmt.Sprintf("the fold changed the line's content (%s); the line is folded, wt sync undo %s puts it back", res.Why, rec.Work)
	}
	fmt.Fprintf(w, "  ✓ %s\n", guardPassed(g))
	return ""
}

// followDrops is a set of plans as they are once the rebase dropped what
// it dropped: an edit's emptied commits, or the drops wt is certain of. Plans
// that cannot be followed come back nil.
func (f *stackFinisher) followDrops(plans []wtsync.LayerPlan) []wtsync.LayerPlan {
	rec, main, in := f.rec, f.main, f.in
	// The fork plans follow what the rebase dropped, as the layers' do below,
	// for the commands --alone prints as much as for the carry.
	if rec.Edit != nil {
		if got, rerr := wtsync.ReconcileEdit(main, in.Onto, plans, *rec.Edit); rerr == nil {
			return got
		}
		return nil
	}
	if got, rerr := wtsync.ApplyDrops(main, in.Onto, plans, in.Dropped, in.Confirmed); rerr == nil {
		return got
	}
	return nil
}

// alone ends the finish of an --alone run: the line is rebased, and no layer
// is checked, moved or held. Each was named when the run began.
func (f *stackFinisher) alone(forks []wtsync.LayerPlan) (*Stack, string, error) {
	w, s, rec, main := f.w, f.s, f.rec, f.main
	defer ClearAlone(main, rec.Epoch)
	leave := "--alone leaves every side branch on the old commits"
	if _, serr := carrySides(main, w, s, rec, f.in, forks, leave); serr != nil {
		fmt.Fprintf(w, "  note: the side branches could not be read against the rebase: %v\n", serr)
	}
	if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
		fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
	}
	fmt.Fprintf(w, "  no layer moved: --alone leaves every layer on the old commits; wt sync undo %s puts the line back\n", rec.Work)
	return s, "", nil
}

// followPlans is the layers' plans as they are once the rebase dropped what
// it dropped; the reason they cannot be followed otherwise.
func (f *stackFinisher) followPlans() ([]wtsync.LayerPlan, string) {
	rec, main, in := f.rec, f.main, f.in
	if rec.Edit != nil {
		// The commits the amend emptied were dropped: the plan follows.
		plans, rerr := wtsync.ReconcileEdit(main, in.Onto, rec.Plans, *rec.Edit)
		if rerr != nil {
			return nil, rerr.Error()
		}
		return plans, ""
	}
	// git empties the picks the target already has (above an upstream)
	// and the ones a conflict rule resolved to trunk's side: the plan
	// follows the drops wt is certain of.
	plans, rerr := wtsync.ApplyDrops(main, in.Onto, rec.Plans, in.Dropped, in.Confirmed)
	if rerr != nil {
		return nil, rerr.Error()
	}
	return plans, ""
}

// verifyTips checks every layer's new tip against its plan; the reason one
// does not hold, "".
func (f *stackFinisher) verifyTips(plans []wtsync.LayerPlan) ([]wtsync.LayerTip, string) {
	rec, main, in := f.rec, f.main, f.in
	tips, cerr := wtsync.CheckLayers(main, in.NewTip, in.Onto, plans)
	if cerr == nil {
		return tips, ""
	}
	why := cerr.Error()
	if !in.Confirmed && rec.Edit == nil && len(in.Dropped) > 0 {
		why += "; the replay was advanced by hand past the run's stop, or the remaining replay was edited by hand, so no dropped commit is taken out of the plan"
	}
	if unclear := wtsync.UnclearDrops(plans, in.Dropped); len(unclear) > 0 && rec.Edit == nil {
		var names []string
		for _, d := range unclear {
			names = append(names, git.ShortID(d.Commit, 7)+" "+d.Subject)
		}
		why += "; the rebase dropped " + strings.Join(names, ", ") + ", which wt cannot tell from another commit with the same author, date and subject, so no layer's count was lowered for it"
	}
	return nil, why
}

// moveLayers moves the layers to their new tips, once the sessions in their
// worktrees are known; the error is a move that stopped or could not be
// recorded.
func (f *stackFinisher) moveLayers(tips []wtsync.LayerTip) error {
	main, rec, in := f.main, f.rec, f.in
	scan, scanErr := agents.ScanHolders(in.Listed)
	if in.ListErr != nil {
		scanErr = in.ListErr
	}
	return wtsync.MoveLayers(main, rec, tips, wtsync.MoveOptions{Listed: in.Listed, Scan: scan, ScanErr: scanErr,
		Force: in.Force, Now: in.Now, AfterMove: in.AfterMove, Between: in.Between})
}

// reportLayers says what came of each layer and records it in the report.
// A move that stopped, or needs recovery, still reports every layer: what
// moved, what did not, and what to look at.
func (f *stackFinisher) reportLayers(tips []wtsync.LayerTip) (held, moved int) {
	w, s, rec, main, in := f.w, f.s, f.rec, f.main, f.in
	// What the line was rebased onto, as a layer it carries is said to be on.
	carrier := cmp.Or(rec.OntoRef, "trunk")
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
		case foldedOnly(t, rec):
			patch, why = "changed", "every commit of it was folded into an earlier commit"
		case len(t.Emptied) > 0 && rec.Upstream == "" && t.Mapped == wtsync.MappedEmptied:
			patch, why = "changed", "every commit of it was dropped as empty"
		case len(t.Emptied) > 0 && rec.Upstream == "" && t.LeftOut == len(t.Emptied):
			patch, why = "changed", kit.Count(len(t.Emptied), "commit")+" of it dropped as empty"
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
		if len(t.Emptied) > 0 && rec.Edit == nil && rec.Upstream == "" {
			mapped = emptiedClause(main, t, tips, i)
		}
		if t.Mapped == wtsync.MappedTipDropped && t.TipFolded && rec.Edit == nil && t.LeftOut == 0 && len(t.Emptied) == 0 {
			mapped = ", tip folded: the fixup went into an earlier commit"
			if out, err := git.Run(main, "log", "-1", "--format=%h %s", t.New); err == nil {
				mapped += "; ends at " + strings.TrimSpace(out)
			}
		}
		if foldedOnly(t, rec) {
			below := "the new base"
			if i > 0 {
				below = tips[i-1].Branch
			}
			mapped = ", folded: every commit of it went into an earlier commit; ends where " + below + " ends"
		}
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
			bp := pushOf(rec, m.Branch)
			if bp.Pushes() {
				s.items = append(s.items, stackItem{BranchPush: bp, New: m.New})
			}
			push, noPush := layerPush(in.Own, m.Branch, m.Path, main)
			if noPush == "" && !bp.Pushes() && bp.Why != "" {
				// What the lease record read before the run is the push's
				// say: a destination that was not safe to push then is not
				// one now, whatever the state looks like after the move.
				push, noPush = nil, bp.Why
			}
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
	return held, moved
}

// moveSides carries the side branches, forgets the record and says how to
// put it all back; the failure the run ends with is what stayed held.
func (f *stackFinisher) moveSides(forks []wtsync.LayerPlan, merr error, held, moved int) (*Stack, string, error) {
	w, s, rec, main := f.w, f.s, f.rec, f.main
	if merr != nil {
		// The record stays: it says what moved, and undo reads it.
		sidesNotMoved(w, s, "the layer moves stopped before it")
		return s, "", merr
	}
	sides, serr := carrySides(main, w, s, rec, f.in, forks, "")
	if serr != nil {
		// The record stays: it says what moved, and undo reads it.
		return s, "", serr
	}
	if ferr := wtsync.ForgetStack(main, rec.Epoch); ferr != nil {
		fmt.Fprintf(w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", ferr, rec.Work)
	}
	switch {
	case sides.moved > 0:
		fmt.Fprintf(w, "  ↩ wt sync undo %s puts back the line, the %s and the %s it moved\n", rec.Work,
			kit.Count(moved, "layer"), countSides(sides.moved))
	case moved > 0:
		fmt.Fprintf(w, "  ↩ wt sync undo %s puts back the line and the %s it moved\n", rec.Work, kit.Count(moved, "layer"))
	}
	var failures []string
	if held > 0 {
		failures = append(failures, kit.Count(held, "layer")+" held")
	}
	if sides.held > 0 {
		failures = append(failures, countSides(sides.held)+" held")
	}
	return s, strings.Join(failures, ", "), nil
}

// foldChanged is why the tree guard refuses a fold, "" for none; see
// wtsync.TreeGuard.
func foldChanged(main, base, onto, oldTip, newTip string, folds, stopped []string) string {
	return foldCheck(main, base, onto, oldTip, newTip, folds, stopped).Why
}

// foldResult is what the tree guard found; see wtsync.GuardResult.
type foldResult = wtsync.GuardResult

// foldCheck holds newTip's tree to what the fold may change; see
// wtsync.TreeGuard.
func foldCheck(main, base, onto, oldTip, newTip string, folds, stopped []string) foldResult {
	return wtsync.TreeGuard(main, wtsync.GuardInput{Base: base, Onto: onto, OldTip: oldTip, NewTip: newTip, Folds: folds, Stopped: stopped})
}

// guardPassed is what a passed tree guard compared, as one line: the old
// tip, or for a fold that moved the line the old line merged onto its
// target; the step paths it left out, apart from the conflicted files it
// held only to keeping the merge's lines outside the conflict.
func guardPassed(g *FoldGuard) string {
	line := "tip tree equal to " + git.ShortID(g.OldTip, 7)
	if g.Onto != nil {
		line = "tip tree equal to the old line merged onto " + git.ShortID(*g.Onto, 7)
	}
	if len(g.ExcludedGlobs) > 0 {
		line += " outside " + strings.Join(g.ExcludedGlobs, ", ")
	}
	if len(g.ConflictAllowed) > 0 {
		line += "; " + strings.Join(g.ConflictAllowed, ", ") + " resolved at a stop, with every line of the merge outside the conflict kept"
	}
	return line
}

func matchesAnyGlob(patterns []string, path string) bool {
	return wtsync.GlobMatching(patterns, path) != ""
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
	rec.Plans, rec.Replay, rec.Forks = res.Layers, res.Replay, res.Forks
	return wtsync.WriteStackRecord(ctx.Repo.MainRoot, rec)
}

// emptiedClause says, on a layer's line, which of its commits the rebase
// dropped as empty and where the layer now ends: at its last commit left, or,
// when none is left, where the layer below (tips[i-1]) ends.
func emptiedClause(main string, t wtsync.LayerTip, tips []wtsync.LayerTip, i int) string {
	var dropped []string
	for _, sha := range t.Emptied {
		if out, err := git.Run(main, "log", "-1", "--format=%h %s", sha); err == nil {
			dropped = append(dropped, strings.TrimSpace(out))
		} else {
			dropped = append(dropped, git.ShortID(sha, 7))
		}
	}
	clause := ", dropped as empty: " + strings.Join(dropped, ", ")
	switch t.Mapped {
	case wtsync.MappedEmptied:
		below := "the new base"
		if i > 0 {
			below = tips[i-1].Branch
		}
		return clause + "; ends where " + below + " ends"
	case wtsync.MappedTipDropped:
		if out, err := git.Run(main, "log", "-1", "--format=%h %s", t.New); err == nil {
			return clause + "; ends at " + strings.TrimSpace(out)
		}
	}
	return clause
}

// foldedOnly is whether a layer's every commit was folded by --autosquash
// into an earlier commit, so that it ends where the layer below ends: no
// commit of it was left out or dropped.
func foldedOnly(t wtsync.LayerTip, rec *wtsync.StackRecord) bool {
	return rec.Edit == nil && rec.Autosquash && t.Mapped == wtsync.MappedEmptied && t.LeftOut == 0 && len(t.Emptied) == 0 && len(t.Picks) == 0
}

// FoldOf is the fold a stack record is of, before its guard has run; nil
// for a record of a run that does not fold the line.
func FoldOf(rec *wtsync.StackRecord) *StackFold {
	if !rec.Autosquash && !rec.InPlace && rec.Edit == nil {
		return nil
	}
	return &StackFold{InPlace: rec.Onto == rec.Line.Base, Base: rec.Line.Base}
}

// SetFold gives the participant the fold the finished stack reports; a
// stack that did not fold leaves what the run recorded as it was.
func SetFold(p *SyncParticipant, s *Stack) {
	if s != nil && s.Fold != nil {
		p.Fold = s.Fold
	}
}

// nonNil is l, or an empty list for none, so --json says [] and not null.
func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
