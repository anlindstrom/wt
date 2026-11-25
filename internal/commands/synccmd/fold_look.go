package synccmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// FoldLook is what a fold with no --onto does to the line, read from the
// line as it is: the run folds in place, on the line's own base, and moves
// nothing onto trunk. Null in --json for any other look.
type FoldLook struct {
	// Base is the line's own base the fold stays on.
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

// FoldLookMove is what a move! commit moves: whole paths of Source.
type FoldLookMove struct {
	Source        string   `json:"source"`
	SourceSubject string   `json:"sourceSubject"`
	SourceLayer   string   `json:"sourceLayer"`
	Paths         []string `json:"paths"`
}

// foldsInPlace is whether a look with these options and target describes a
// fold in place, as a run folds unless --onto names where the line goes.
func foldsInPlace(look *StackLook, target *OntoTarget) bool {
	return look != nil && look.Autosquash && target == nil
}

// foldLookAt reads the stack of look and the fold a run of it makes.
func foldLookAt(ctx *kit.Context, base, trunkSHA string, cfg *wtsync.Config, look *StackLook, listed []agents.Agent) (*syncrun.Stack, *FoldLook, error) {
	s, err := stackLookAt(ctx, base, trunkSHA, nil, look, listed)
	if err != nil {
		return nil, nil, err
	}
	fl, err := foldLookOf(ctx.Repo.MainRoot, cfg, s, look.Alone)
	if err != nil {
		return nil, nil, err
	}
	return s, fl, nil
}

// aloneHeld is why --alone leaves a layer where it is.
const aloneHeld = "--alone leaves every layer on its old commits"

// foldLookOf reads the fold the run of s would make from the commits of its
// line, as the run decides it: the refusals it makes before it writes
// anything, the layers a worktree holds, and --alone.
func foldLookOf(main string, cfg *wtsync.Config, s *syncrun.Stack, alone bool) (*FoldLook, error) {
	out, err := git.Run(main, "rev-list", "--reverse", s.Base+".."+s.Head)
	if err != nil {
		return nil, err
	}
	var commits []string
	if out != "" {
		commits = strings.Split(out, "\n")
	}
	fl := &FoldLook{Base: s.Base, Folds: []FoldLookPair{}, Unchanged: []string{}, Repointed: []string{}, Held: []FoldLookHeld{}}
	why := cfg.StackFoldRefusal()
	if why == "" {
		if why, err = wtsync.FixupOfCarried(main, s.Base, "", s.Branch, commits); err != nil {
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
	for _, l := range s.Layers {
		changes := len(pairs) > 0 && l.Commits > lowest
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
	fmt.Fprintf(w, "  %s in place on the line's own base %s, nothing is rebased onto trunk:\n",
		foldVerb(len(fl.Folds)), git.ShortID(fl.Base, 7))
	for _, p := range fl.Folds {
		fmt.Fprintf(w, "    %s %s  → %s %s  (layer %s)\n", git.ShortID(p.Fixup, 7), kit.OneLine(p.FixupSubject),
			git.ShortID(p.Target, 7), kit.OneLine(p.TargetSubject), p.TargetLayer)
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

// foldVerb is "1 fixup folds" or "2 fixups fold".
func foldVerb(n int) string {
	if n == 1 {
		return "1 fixup folds"
	}
	return kit.Count(n, "fixup") + " fold"
}

// printFoldDetail is wt sync <work> --stack --autosquash with no --onto: the
// worktree and what the run does with it. A run folds the line on its own
// base, so nothing is said of a rebase onto trunk.
func printFoldDetail(w io.Writer, work string, a wtsync.Assessment, fl *FoldLook) {
	fmt.Fprintf(w, "\n%s  folds in place\n", work)
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
	v, why = stackVerdict(a, v, why, true)
	switch {
	case v == wtsync.SkipRun:
		return "skipped: " + why
	case v != wtsync.Proceed:
		return "refused: " + why
	case fl.Refusal != nil:
		return "refused: the fold: " + kit.OneLine(*fl.Refusal)
	}
	verdict := fmt.Sprintf("wt sync rebase %s --stack --autosquash folds the line in place", work)
	if len(fl.Folds) == 0 {
		verdict = fmt.Sprintf("wt sync rebase %s --stack --autosquash has no fixup to fold", work)
	}
	if len(a.Sessions) > 0 {
		verdict += "; asks first: " + kit.WhoLabel(a.Sessions) + " is in it"
	}
	return verdict
}
