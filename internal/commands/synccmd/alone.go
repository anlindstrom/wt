package synccmd

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// editAlone is why --alone and --edit are not given together: an edit run
// stops for a person to amend and then moves the layers by what came of it,
// and with --alone there is no layer to move.
const editAlone = "--alone and --edit: --edit moves the layers by the amend, and --alone moves none; edit first without --alone"

// aloneUsage refuses the runs --alone has no meaning for, before anything is
// fetched or moved. It takes one worktree, which it rebases without the rest
// of its stack; with --stack it takes the line of that worktree and moves no
// layer. --onto already moves exactly the worktrees named, and wt sync steps
// and a resume or an undo have no stack to leave out.
func aloneUsage(opts RunOptions, works []string) error {
	switch {
	case !opts.Alone:
		return nil
	case len(works) != 1:
		return errors.New("--alone rebases one worktree without the rest of its stack: name the one")
	case opts.Onto != "":
		return errors.New("--alone and --onto: --onto already moves exactly the worktrees named and none of their stack; leave --alone out")
	case opts.Stack != nil && opts.Stack.Edit != "":
		return errors.New(editAlone)
	case opts.Steps != nil:
		return errors.New("--alone is for a rebase: wt sync steps runs the steps of the one branch named")
	}
	return nil
}

// strandedBy is the worktrees of b's stack that a run of b alone leaves
// where they are: every other member, and the branches b merges.
func strandedBy(parents map[string]string, ambiguous map[string][]string, b string) []string {
	var out []string
	for _, m := range wtsync.Members(parents, b) {
		if m != b {
			out = append(out, m)
		}
	}
	for _, m := range ambiguous[b] {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// leftAloneOf names each of branches as a worktree of the repository.
func leftAloneOf(ctx *kit.Context, worktrees []repo.Worktree, branches []string) []syncrun.LeftAlone {
	paths := map[string]string{}
	for _, wt := range worktrees {
		paths[wt.Branch] = wt.Path
	}
	out := []syncrun.LeftAlone{}
	for _, b := range branches {
		l := syncrun.LeftAlone{Branch: b, Path: jsonout.Strp(paths[b])}
		if path, ok := paths[b]; ok {
			l.Work = jsonout.Strp(kit.WorktreeName(ctx, b, path))
		}
		out = append(out, l)
	}
	return out
}

// printLeftAlone says, for each worktree a run left alone, that it was not
// touched and now sits on the old commits.
func printLeftAlone(w io.Writer, left []syncrun.LeftAlone) {
	for _, l := range left {
		fmt.Fprintf(w, "left alone: %s  not rebased and not touched; it sits on the old commits\n", aloneName(l))
	}
}

// printAloneLook is what wt sync <work> --alone adds to the look: who a run
// of it would leave.
func printAloneLook(w io.Writer, left []syncrun.LeftAlone) {
	if len(left) == 0 {
		fmt.Fprintln(w, "  --alone: no other worktree is stacked on it; nothing would be left alone")
		return
	}
	names := make([]string, len(left))
	for i, l := range left {
		names[i] = aloneName(l)
	}
	fmt.Fprintf(w, "  --alone  a run rebases this worktree only; left alone: %s (they stay on the old commits)\n", strings.Join(names, ", "))
}

// aloneName is the worktree a run left alone, or the branch of a layer in none.
func aloneName(l syncrun.LeftAlone) string {
	if l.Work != nil {
		return *l.Work
	}
	return l.Branch
}

// layersLeftAlone names the layer branches of a --stack line that a run given
// --alone moves none of, but for the line's own branch, and records them.
func (r *RunPlan) layersLeftAlone(b string, line wtsync.Line) error {
	worktrees, err := r.ctx.Repo.Worktrees()
	if err != nil {
		return err
	}
	var branches []string
	for _, l := range line.Layers {
		if l.Branch != b {
			branches = append(branches, l.Branch)
		}
	}
	left := leftAloneOf(r.ctx, worktrees, branches)
	printLeftAlone(r.w, left)
	r.opts.Journal.SetLeftAlone(left)
	return nil
}
