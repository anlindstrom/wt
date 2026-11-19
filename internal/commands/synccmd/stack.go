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
// layers named, or every local branch whose tip is on it but the backups.
func readLine(ctx *kit.Context, branch, trunkSHA string, layers []string) (wtsync.Line, error) {
	return wtsync.ReadLine(ctx.Repo.MainRoot, branch, trunkSHA, layers, backupPatterns(ctx))
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

// stackLookAt is the look at the stack held on the line of the worktree
// look names, read from base (trunk, or where a rebase onto target starts):
// its layers, bottom first, and what holds each. A target on the line is
// refused.
func stackLookAt(ctx *kit.Context, base, trunkSHA string, target *OntoTarget, look *StackLook, listed []agents.Agent) (*syncrun.Stack, error) {
	wt, err := kit.Locate(ctx, look.Work)
	if err != nil {
		return nil, err
	}
	line, err := readLine(ctx, wt.Branch, base, look.Layers)
	if err != nil {
		return nil, err
	}
	if target != nil {
		if why := target.onLine(ctx, line, trunkSHA, kit.WorktreeName(ctx, wt.Branch, wt.Path)); why != "" {
			return nil, errors.New(why)
		}
	}
	holders, err := syncrun.ReadHolders(ctx, line, listed)
	if err != nil {
		return nil, err
	}
	s := syncrun.StackOf(line, look.Autosquash, holders, wt.Path)
	if !look.NoSideBranches {
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
