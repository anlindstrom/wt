package synccmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/retire"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// readLine reads the stack held on the line of branch against trunk: the
// layers named, or every local branch whose tip is on it but the backups and
// the strays (skipStrays).
func readLine(ctx *kit.Context, branch, trunkSHA string, layers []string) (wtsync.Line, error) {
	line, err := wtsync.ReadLine(ctx.Repo.MainRoot, branch, trunkSHA, layers, backupPatterns(ctx))
	if err != nil || len(layers) > 0 {
		return line, err
	}
	return skipStrays(ctx, line)
}

// skipStrays lists under Skipped the found branches that are strays and
// leaves them out of the layers: a branch with no commits of its own that has
// uncommitted tracked changes in a worktree other than the line's own. A
// branch has no commits of its own when its tip is the line's tip or the tip
// of a branch that stays a layer. A branch with uncommitted changes (as
// wtsync.ReadHolder reads them) is not one that stays, so two dirty branches
// at one commit no other layer has are not judged against each other, and
// nothing depends on the order of the names. A line that would be left with
// no layer keeps all of them: it keeps at least the layers it has without
// this rule. --layer never reaches here: it takes the branch it names.
func skipStrays(ctx *kit.Context, line wtsync.Line) (wtsync.Line, error) {
	if len(line.Layers) < 2 {
		return line, nil
	}
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return line, err
	}
	top, _ := worktrees.ByBranch(line.Branch)
	dirty := map[string]string{}
	stays := map[string]bool{line.Head: true}
	for _, l := range line.Layers {
		h := wtsync.ReadHolder(l.Branch, worktrees, nil, nil, nil)
		if h.State == wtsync.HolderDirty && h.Path != top.Path {
			dirty[l.Branch] = h.Path
			continue
		}
		stays[l.Tip] = true
	}
	var kept []wtsync.LineLayer
	var strays []wtsync.SkippedBranch
	for _, l := range line.Layers {
		if path, ok := dirty[l.Branch]; ok && stays[l.Tip] {
			why := "no commits of its own, with uncommitted changes in " + path + "; name it with --layer to move it"
			strays = append(strays, wtsync.SkippedBranch{Branch: l.Branch, Tip: l.Tip, Why: why})
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) == 0 || len(strays) == 0 {
		return line, nil
	}
	line.Layers = kept
	line.Skipped = append(line.Skipped, strays...)
	slices.SortFunc(line.Skipped, func(a, b wtsync.SkippedBranch) int { return strings.Compare(a.Branch, b.Branch) })
	return line, nil
}

// readSides reads the branches that hang off line, a backup known by the
// same patterns readLine knows one by.
func readSides(ctx *kit.Context, line wtsync.Line, trunkSHA string, target *OntoTarget) ([]wtsync.SideBranch, error) {
	trunk := ctx.Config.MainBranch
	originHead, _, _ := ctx.Repo.OriginHead()
	guard := wtsync.SideGuard{History: []string{trunkSHA, "refs/heads/" + trunk, "refs/remotes/origin/" + trunk}}
	onto := ""
	if target != nil {
		guard.History = append(guard.History, target.SHA)
		onto = strings.TrimPrefix(target.Full, "refs/heads/")
	}
	guard.Protected = func(name string) bool {
		return (onto != "" && name == onto) || retire.ProtectedBranch(name, trunk, originHead)
	}
	return wtsync.ReadSides(ctx.Repo.MainRoot, line, backupPatterns(ctx), guard)
}

// aloneSides is why no side branch moves in a run with --alone, "" without.
func aloneSides(alone bool) string {
	if alone {
		return "--alone leaves every side branch on the old commits"
	}
	return ""
}

// backupPatterns is the names a --stack run takes for backups: wt refs
// sweep's and the ones it keeps.
func backupPatterns(ctx *kit.Context) []string { return ctx.UserConfig().BackupPatterns() }

// readLookLine reads the line of the worktree look names from base (trunk,
// or where a rebase onto target starts), and the path of that worktree. A
// target on the line is refused.
func readLookLine(ctx *kit.Context, base, trunkSHA string, target *OntoTarget, look *StackLook) (wtsync.Line, string, error) {
	wt, err := kit.Locate(ctx, look.Work)
	if err != nil {
		return wtsync.Line{}, "", err
	}
	line, err := readLine(ctx, wt.Branch, base, look.Layers)
	if err != nil {
		return wtsync.Line{}, "", err
	}
	if target != nil {
		if why := target.onLine(ctx, line, trunkSHA, kit.WorktreeName(ctx, wt.Branch, wt.Path)); why != "" {
			return wtsync.Line{}, "", errors.New(why)
		}
	}
	return line, wt.Path, nil
}

// stackLookAt is the look at the stack held on the line of the worktree
// look names, read as readLookLine reads it: its layers, bottom first, and
// what holds each.
func stackLookAt(ctx *kit.Context, base, trunkSHA string, target *OntoTarget, look *StackLook, listed []agents.Agent, progress lookProgress) (*syncrun.Stack, error) {
	line, path, err := readLookLine(ctx, base, trunkSHA, target, look)
	if err != nil {
		return nil, err
	}
	holders, err := syncrun.ReadHolders(ctx, line, listed)
	if err != nil {
		return nil, err
	}
	s := syncrun.StackOf(line, look.Autosquash, holders, path)
	if !look.NoSideBranches {
		progress.phase("reading side branches")
		sides, err := readSides(ctx, line, trunkSHA, target)
		if err != nil {
			return nil, err
		}
		sideHolders, err := syncrun.ReadSideHolders(ctx, sides, listed)
		if err != nil {
			return nil, err
		}
		syncrun.SetSides(s, sides, sideHolders, aloneSides(look.Alone))
	}
	if look.Autosquash {
		progress.phase("analysing fixup context")
		syncrun.ContextGaps(ctx.Repo.MainRoot, line, s)
	}
	return s, nil
}

// editOnLine resolves rev, what --edit names, to the one commit of line it
// must be: a commit, and one of the line's own, not of its base.
func editOnLine(main, rev string, line wtsync.Line, work string) (*wtsync.EditStop, error) {
	sha, err := git.Run(main, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil || sha == "" {
		return nil, fmt.Errorf("--edit %s is not a commit here, or names more than one; nothing is rebased", rev)
	}
	if !slices.Contains(line.Commits, sha) {
		return nil, fmt.Errorf("--edit %s (%s) is not on the line of %s, from its base %s to its tip: name one of the line's own commits; nothing is rebased",
			rev, git.ShortID(sha, 7), work, git.ShortID(line.Base, 7))
	}
	subject, err := git.Run(main, "log", "-1", "--format=%s", sha)
	if err != nil {
		return nil, err
	}
	return &wtsync.EditStop{Commit: sha, Subject: subject}, nil
}
