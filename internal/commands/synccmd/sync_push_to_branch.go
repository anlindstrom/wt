package synccmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// SyncPushToBranch is wt sync push-to --branch: wt sync push-to for any local
// branch, a worktree's or not. branch is a branch name or a pattern with * or
// ? (matched as --layer matches: * crosses a slash); a pattern records one
// push target per branch it matches, and dest then holds one * that stands
// for what the pattern's * matched. Every match is checked before any is
// recorded, so a refusal leaves everything as it was. dryRun says what would
// be recorded and records nothing.
func SyncPushToBranch(ctx *kit.Context, branch, dest string, unset, dryRun bool, w io.Writer) error {
	branches, err := pushToBranches(ctx, branch)
	if err != nil {
		return err
	}
	type mapping struct {
		branch string
		to     repo.PushTo
	}
	var maps []mapping
	if dest != "" && !unset {
		for _, b := range branches {
			d, err := expandDest(branch, dest, b, len(branches))
			if err != nil {
				return err
			}
			to, err := resolvePushTo(ctx, d)
			if err != nil {
				return err
			}
			maps = append(maps, mapping{b, to})
		}
	}
	switch {
	case unset:
		for _, b := range branches {
			if dryRun {
				fmt.Fprintf(w, "would forget where %s pushes\n", b)
				continue
			}
			if err := ctx.Repo.UnsetPushTo(b); err != nil {
				return err
			}
			fmt.Fprintf(w, "forgot where %s pushes\n", b)
		}
		return nil
	case dest != "":
		for _, m := range maps {
			if dryRun {
				fmt.Fprintf(w, "would record: %s pushes to %s\n", m.branch, m.to)
				continue
			}
			if err := ctx.Repo.SetPushTo(m.branch, m.to); err != nil {
				return err
			}
		}
	}
	if dryRun {
		return nil
	}
	own := wtsync.ReadOwnOrUnknown(ctx.Repo.MainRoot, ctx.Config.MainBranch)
	for _, b := range branches {
		to := own.Push(b)
		if to.Remote == "" {
			fmt.Fprintf(w, "%s has nowhere to push: %s\n", b, syncrun.NotPushed(to))
			continue
		}
		fmt.Fprintf(w, "%s pushes to %s (%s)\n", b, to.Ref(), to.Rule)
	}
	return nil
}

// pushToBranches is the local branches branch names: the one, or every one a
// pattern matches. Nothing matching is refused.
func pushToBranches(ctx *kit.Context, branch string) ([]string, error) {
	if !wtsync.IsLayerPattern(branch) {
		if !ctx.Repo.BranchExists(branch) {
			return nil, fmt.Errorf("there is no local branch %s", branch)
		}
		return []string{branch}, nil
	}
	all, err := ctx.Repo.Branches()
	if err != nil {
		return nil, err
	}
	pat := config.CompileRefPatterns([]string{branch})
	var out []string
	for _, b := range all {
		if _, ok := config.MatchRefPattern(pat, b.Name); ok {
			out = append(out, b.Name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s matches no local branch; nothing is recorded", branch)
	}
	return out, nil
}

// expandDest is dest for one matched branch: its * is replaced by what the
// pattern's single * matched. A destination with no * names one branch, so it
// is for a pattern that matched one.
func expandDest(pattern, dest, branch string, matches int) (string, error) {
	stars := strings.Count(dest, "*")
	if !wtsync.IsLayerPattern(pattern) {
		if stars > 0 {
			return "", fmt.Errorf("%s has a *, and %s is a branch name, not a pattern", dest, pattern)
		}
		return dest, nil
	}
	switch {
	case stars == 0 && matches == 1:
		return dest, nil
	case stars == 0:
		return "", fmt.Errorf("%s matches %d branches and %s is one remote branch: write a * in it for what the pattern's * matched", pattern, matches, dest)
	case stars > 1 || strings.Count(pattern, "*") != 1 || strings.Contains(pattern, "?"):
		return "", fmt.Errorf("a destination with a * needs a pattern with one * and no ?: %s -> %s", pattern, dest)
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	return strings.Replace(dest, "*", strings.TrimSuffix(strings.TrimPrefix(branch, prefix), suffix), 1), nil
}
