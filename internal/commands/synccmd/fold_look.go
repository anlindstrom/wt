package synccmd

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// FoldLook is what a fold does to the line, read from the line as it is: with
// no --onto the run folds in place, on the line's own base, and moves nothing
// onto trunk; with --onto it is made only for a line with a move! commit,
// replayed onto the target. Null in --json for any other look.
type FoldLook struct {
	// onto is the target of a fold that moves the line, nil in place.
	onto *OntoTarget
	// Base is the line's own base, which a fold in place stays on.
	Base string `json:"base"`
	// Refusal is why the run refuses the fold, the words it refuses with;
	// null when it does not. When set, Folds, Unchanged, Repointed and Held
	// are all empty.
	Refusal *string `json:"refusal"`
	// Folds is every fixup with the commit and layer it folds into, oldest
	// fixup first. A fixup above the top layer is on the line's own branch.
	Folds []FoldLookPair `json:"folds"`
	// Unchanged is the layers below every fold target: the fold leaves their
	// commits as they are.
	Unchanged []string `json:"unchanged"`
	// Repointed is the layers the fold gives new commits, bottom first.
	Repointed []string `json:"repointed"`
	// Held is the layers the run leaves on their old commits although the
	// fold changes them: a worktree holds them, or --alone.
	Held []FoldLookHeld `json:"held"`
}

// FoldLookHeld is a layer the run leaves where it is, and why.
type FoldLookHeld struct {
	Branch string `json:"branch"`
	Why    string `json:"why"`
}

// FoldLookPair is one fixup or move! commit of a FoldLook.
type FoldLookPair struct {
	// Kind is fold for a fixup!, squash! or amend! commit, move for a move!
	// commit, whose Move says what it moves.
	Kind          string        `json:"kind"`
	Fixup         string        `json:"fixup"`
	FixupSubject  string        `json:"fixupSubject"`
	FixupLayer    string        `json:"fixupLayer"`
	Target        string        `json:"target"`
	TargetSubject string        `json:"targetSubject"`
	TargetLayer   string        `json:"targetLayer"`
	Move          *FoldLookMove `json:"move"`
}

// The kinds of a FoldLookPair.
const (
	foldKindFold = "fold"
	foldKindMove = "move"
)

// FoldLookMove is what a move! commit moves: whole paths of Source, and line
// ranges of others, in Source's own line numbers.
type FoldLookMove struct {
	Source        string             `json:"source"`
	SourceSubject string             `json:"sourceSubject"`
	SourceLayer   string             `json:"sourceLayer"`
	Paths         []string           `json:"paths"`
	Lines         []wtsync.LineRange `json:"lines"`
}

// noticeFixupRefusal says on progress that a fixup's target refuses the fold
// of look's line, unless the run is known to stop before it checks them. It
// runs ahead of the look's slow phases and decides nothing the look prints: a
// line it cannot read is reported, and the targets are checked again, when
// the look reads the stack.
func noticeFixupRefusal(ctx *kit.Context, base, trunkSHA string, cfg *wtsync.Config, target *OntoTarget, look *StackLook, listed []agents.Agent, own wtsync.OwnRemote, progress lookProgress) {
	progress.phase("resolving fixup targets")
	line, path, err := readLookLine(ctx, base, trunkSHA, target, look)
	if err != nil || runStopsAhead(cfg, line, path, listed, own) {
		return
	}
	main := ctx.Repo.MainRoot
	onto, upstream := line.Base, ""
	if target != nil {
		if moves, err := wtsync.HasMoves(main, line.Commits); err != nil || !moves {
			return
		}
		onto, upstream = target.SHA, target.Upstream
	}
	if why, err := wtsync.FixupOfCarried(main, onto, upstream, line.Branch, line.Commits); err == nil && why != "" {
		progress.line("wt: unless something else stops it first, the run will refuse the fold: %s", kit.OneLine(why))
	}
}

// runStopsAhead is whether the run of the fold of line, checked out at path,
// is refused before it checks the fixups' targets, as far as that is known
// without the assessment: no declaration or one that refuses the fold, no
// layer, a handover, a rebase, tracked changes, a busy session, or its own
// remote.
func runStopsAhead(cfg *wtsync.Config, line wtsync.Line, path string, listed []agents.Agent, own wtsync.OwnRemote) bool {
	if cfg == nil || cfg.StackFoldRefusal() != "" || len(line.Layers) == 0 || own.State == wtsync.OwnBehind || own.FetchFailed != "" {
		return true
	}
	gitDir, err := wtsync.GitDir(path)
	if err != nil {
		return true
	}
	if paused, err := wtsync.HasPlan(gitDir); err != nil || paused {
		return true
	}
	s := wtsync.Recheck(path, gitDir, listed)
	return s.Err != nil || s.Rebasing || s.Dirty || len(s.Sessions.Busy()) > 0
}

// foldLookAt reads the stack of look and the fold a run of it makes; a nil
// fold for a run onto target with no move! commit on the line.
func foldLookAt(ctx *kit.Context, base, trunkSHA string, cfg *wtsync.Config, target *OntoTarget, look *StackLook, listed []agents.Agent, progress lookProgress) (*syncrun.Stack, *FoldLook, error) {
	s, err := stackLookAt(ctx, base, trunkSHA, target, look, listed, progress)
	if err != nil {
		return nil, nil, err
	}
	fl, err := foldLookFor(ctx.Repo.MainRoot, cfg, s, target, look.Alone)
	if err != nil {
		return nil, nil, err
	}
	return s, fl, nil
}

// foldLookFor is foldLookOf where a look has a fold to show: in place, or onto
// target for a line with a move! commit.
func foldLookFor(main string, cfg *wtsync.Config, s *syncrun.Stack, target *OntoTarget, alone bool) (*FoldLook, error) {
	if target != nil {
		commits, err := lineCommits(main, s)
		if err != nil {
			return nil, err
		}
		if moves, err := wtsync.HasMoves(main, commits); err != nil || !moves {
			return nil, err
		}
	}
	return foldLookOf(main, cfg, s, target, alone)
}

// lineCommits is the commits of s's line, oldest first.
func lineCommits(main string, s *syncrun.Stack) ([]string, error) {
	out, err := git.Run(main, "rev-list", "--reverse", s.Base+".."+s.Head)
	return strings.Fields(out), err
}

// aloneHeld is why --alone leaves a layer where it is.
const aloneHeld = "--alone leaves every layer on its old commits"

// foldLookOf reads the fold the run of s would make from the commits of its
// line, as the run decides it: the refusals it makes before it writes
// anything, the layers a worktree holds, and --alone.
func foldLookOf(main string, cfg *wtsync.Config, s *syncrun.Stack, target *OntoTarget, alone bool) (*FoldLook, error) {
	commits, err := lineCommits(main, s)
	if err != nil {
		return nil, err
	}
	fl := &FoldLook{Base: s.Base, Folds: []FoldLookPair{}, Unchanged: []string{}, Repointed: []string{}, Held: []FoldLookHeld{}}
	onto, upstream := s.Base, ""
	if target != nil {
		fl.onto, onto, upstream = target, target.SHA, target.Upstream
	}
	why := cfg.StackFoldRefusal()
	if why == "" {
		if why, err = wtsync.FixupOfCarried(main, onto, upstream, s.Branch, commits); err != nil {
			return nil, err
		}
	}
	var moves []wtsync.PartMove
	if why == "" {
		if moves, why, err = wtsync.PlanMovesOnto(main, cfg, commits, onto, upstream); err != nil {
			return nil, err
		}
	}
	if why != "" {
		fl.Refusal = &why
		return fl, nil
	}
	pairs, err := wtsync.FoldPairs(main, commits)
	if err != nil {
		return nil, err
	}
	layerAt := func(i int) string {
		for _, l := range s.Layers {
			if i < l.Commits {
				return l.Branch
			}
		}
		return s.Branch
	}
	lowest := len(commits)
	for _, p := range pairs {
		fl.Folds = append(fl.Folds, FoldLookPair{Kind: foldKindFold, Fixup: p.Fixup, FixupSubject: p.FixupSubject, FixupLayer: layerAt(p.FixupAt),
			Target: p.Target, TargetSubject: p.TargetSubject, TargetLayer: layerAt(p.TargetAt)})
		lowest = min(lowest, p.TargetAt)
	}
	at := map[string]int{}
	for i, c := range commits {
		at[c] = i
	}
	for _, m := range moves {
		fl.Folds = append(fl.Folds, FoldLookPair{Kind: foldKindMove, Fixup: m.Commit, FixupSubject: m.Subject, FixupLayer: layerAt(at[m.Commit]),
			Target: m.Target, TargetSubject: m.TargetSubject, TargetLayer: layerAt(at[m.Target]),
			Move: &FoldLookMove{Source: m.Source, SourceSubject: m.SourceSubject, SourceLayer: layerAt(at[m.Source]),
				Paths: append([]string{}, m.Paths...), Lines: append([]wtsync.LineRange{}, m.Lines...)}})
		lowest = min(lowest, at[m.Target])
	}
	slices.SortStableFunc(fl.Folds, func(a, b FoldLookPair) int { return at[a.Fixup] - at[b.Fixup] })
	for _, l := range s.Layers {
		changes := onto != s.Base || len(fl.Folds) > 0 && l.Commits > lowest
		switch {
		case alone:
			fl.Held = append(fl.Held, FoldLookHeld{Branch: l.Branch, Why: aloneHeld})
		case !changes:
			fl.Unchanged = append(fl.Unchanged, l.Branch)
		case (wtsync.LayerHolder{State: l.State}).Holds():
			why := l.State
			if l.StateDetail != nil {
				why += ": " + *l.StateDetail
			}
			fl.Held = append(fl.Held, FoldLookHeld{Branch: l.Branch, Why: why})
		default:
			fl.Repointed = append(fl.Repointed, l.Branch)
		}
	}
	return fl, nil
}

// printFoldLook is the look's lines for a fold in place: what folds into
// what, and which layers change.
func printFoldLook(w io.Writer, fl *FoldLook) {
	if fl.Refusal != nil {
		fmt.Fprintf(w, "  the run refuses the fold, and nothing moves:\n")
		printLines(w, 2, *fl.Refusal)
		return
	}
	if len(fl.Folds) == 0 {
		fmt.Fprintf(w, "  no fixup!, squash! or amend! commit on the line: the fold changes nothing\n")
		return
	}
	if t := fl.onto; t != nil {
		fmt.Fprintf(w, "  %s as the line is replayed onto %s:\n", foldVerb(fl.Folds), t.Short())
	} else {
		fmt.Fprintf(w, "  %s in place on the line's own base %s, nothing is rebased onto trunk:\n",
			foldVerb(fl.Folds), git.ShortID(fl.Base, 7))
	}
	moves := 0
	for _, p := range fl.Folds {
		fmt.Fprintf(w, "    %s %s  → %s %s  (layer %s)\n", git.ShortID(p.Fixup, 7), kit.OneLine(p.FixupSubject),
			git.ShortID(p.Target, 7), kit.OneLine(p.TargetSubject), p.TargetLayer)
		if m := p.Move; m != nil {
			moves++
			fmt.Fprintf(w, "      moves %s of %s %s  (layer %s)\n", wtsync.MoveWhat(m.Paths, m.Lines), git.ShortID(m.Source, 7), kit.OneLine(m.SourceSubject), m.SourceLayer)
		}
	}
	if moves > 0 {
		fmt.Fprintf(w, "  a move! commit needs the run's checks at its target and source: --each-commit=targets\n")
	}
	if len(fl.Held) > 0 && fl.Held[0].Why == aloneHeld {
		fmt.Fprintf(w, "  --alone: no layer moves; each stays on its old commits\n")
		return
	}
	fmt.Fprintf(w, "  layers unchanged: %s; re-pointed: %s\n", listOrNone(fl.Unchanged), listOrNone(fl.Repointed))
	if len(fl.Held) > 0 {
		held := make([]string, len(fl.Held))
		for i, h := range fl.Held {
			held[i] = h.Branch + " (" + h.Why + ")"
		}
		fmt.Fprintf(w, "  held where they are: %s\n", strings.Join(held, ", "))
	}
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// foldVerb is "1 fixup folds", "2 fixups fold", or with move! commits
// among them "1 fixup and 1 move fold".
func foldVerb(folds []FoldLookPair) string {
	moves := 0
	for _, p := range folds {
		if p.Move != nil {
			moves++
		}
	}
	fixups := len(folds) - moves
	switch {
	case moves == 0 && fixups == 1:
		return "1 fixup folds"
	case moves == 0:
		return kit.Count(fixups, "fixup") + " fold"
	case fixups == 0 && moves == 1:
		return "1 move folds"
	case fixups == 0:
		return kit.Count(moves, "move") + " fold"
	}
	return kit.Count(fixups, "fixup") + " and " + kit.Count(moves, "move") + " fold"
}

// printFoldDetail is wt sync <work> --stack --autosquash for a look with a
// fold: the worktree and what the run does with it. A run folds the line on
// its own base, or onto --onto's target, so nothing is said of a rebase onto
// trunk.
func printFoldDetail(w io.Writer, work string, a wtsync.Assessment, fl *FoldLook) {
	where := "in place"
	if fl.onto != nil {
		where = "onto " + fl.onto.Ref
	}
	fmt.Fprintf(w, "\n%s  folds %s\n", work, where)
	printFacts(w, a)
	fmt.Fprintf(w, "  run     %s\n", foldVerdict(work, a, fl))
	if a.PlanFile != "" {
		fmt.Fprintf(w, "  plan    %s\n", a.PlanFile)
	}
	printErrorAndHandover(w, a)
	fmt.Fprintln(w)
	printFoldLook(w, fl)
}

// foldVerdict is runVerdict for a fold in place, with the verdict the run
// gives a stack.
func foldVerdict(work string, a wtsync.Assessment, fl *FoldLook) string {
	v, why := wtsync.Preflight(a)
	v, why = stackVerdict(a, v, why, fl.onto == nil)
	switch {
	case v == wtsync.SkipRun:
		return "skipped: " + why
	case v != wtsync.Proceed:
		return "refused: " + why
	case fl.Refusal != nil:
		return "refused: the fold: " + kit.OneLine(*fl.Refusal)
	}
	flags, does := "--stack --autosquash", "folds the line in place"
	if t := fl.onto; t != nil {
		flags += " --onto " + t.Ref
		if t.UpstreamRef != "" {
			flags += " --upstream " + t.UpstreamRef
		}
		does = "folds the line and moves it onto " + t.Ref
	}
	if slices.ContainsFunc(fl.Folds, func(p FoldLookPair) bool { return p.Move != nil }) {
		flags += " --each-commit=targets"
	}
	verdict := fmt.Sprintf("wt sync rebase %s %s %s", work, flags, does)
	if len(fl.Folds) == 0 {
		verdict = fmt.Sprintf("wt sync rebase %s --stack --autosquash has no fixup to fold", work)
	}
	if len(a.Sessions) > 0 {
		verdict += "; asks first: " + kit.WhoLabel(a.Sessions) + " is in it"
	}
	return verdict
}
