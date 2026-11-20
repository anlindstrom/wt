package wtsync

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/repo"
)

// The kinds of side branch, as SideBranch.Kind says it.
const (
	SideForked = "forked" // forks from a commit of the line below its top
	SideAbove  = "above"  // carries the line's top
)

// SideBranch is a local branch that hangs off a line without being on it:
// its tip is no commit of the line, and where it leaves the line (its
// merge-base with the line's top) is a commit of the line above its base.
// Forked from the top itself, it is above the line.
type SideBranch struct {
	Branch, Tip string
	Kind        string
	// Fork is the commit of the line it leaves the line at, and Count that
	// commit's position on the line, from 1.
	Fork, ForkSubject string
	Count             int
	// On is the side branch it is stacked on, "" when it sits on the line
	// itself. A backup is never what a branch is on, and neither is a branch
	// at its very tip: branches at one tip are siblings (SameTip), and what
	// is stacked above them is on the one that stands for them.
	On string
	// Base is where its own commits start: On's tip, or Fork. Commits is how
	// many there are from Base to Tip.
	Base    string
	Commits int
	// Backup says its name is a backup (wt refs sweep's patterns).
	Backup bool
	// Merges says it has a merge commit of its own, which a replay drops:
	// it is left, with no command, to be moved by hand.
	Merges bool
	// Left is why a run never moves it: a backup, a merge commit, more than
	// one branch below it, or the branch it is on being left. "" for a
	// branch a run carries.
	Left string
	// SameTip is the other branches at its very tip, by name: siblings,
	// carried or held together onto one new commit. With is the one of them
	// it moves with, which stands for them all; "" for that one, a backup,
	// and a branch alone at its tip.
	SameTip []string
	With    string
}

// SideGuard is what is never a side branch of a line: a branch Protected
// names (trunk, the long-lived branches beside it, the branch --onto names),
// and any branch that leaves the line at a commit one of History (trunk,
// its remote-tracking ref, the --onto target) already has. With --upstream
// below where trunk leaves, the line holds trunk's own commits, and a branch
// cut from them is trunk's history, not the line's.
type SideGuard struct {
	Protected func(name string) bool
	History   []string
}

// ReadSides reads the branches that hang off line, every one a local branch
// other than the line's own, its layers and its skipped branches, whose tip
// contains the line's first commit and is not on the line. They come
// parents first: each root in the order of where it forks (then by name),
// and after it the branches at its tip and what is stacked on it, by name.
// Of branches at one tip, the one that stands for them (standsFor) comes
// first; the rest move with it. A branch matching backups
// (wt refs sweep's patterns, matched by backup(), as ReadLine matches them)
// is listed and left: a backup marks a point in time, and moving it would
// lose what it records.
func ReadSides(dir string, line Line, backups []string, guard SideGuard) ([]SideBranch, error) {
	if len(line.Commits) == 0 {
		return nil, nil
	}
	sweep := config.CompileRefPatterns(backups)
	pos := map[string]int{}
	for i, c := range line.Commits {
		pos[c] = i + 1
	}
	taken := map[string]bool{line.Branch: true}
	for _, l := range line.Layers {
		taken[l.Branch] = true
	}
	for _, k := range line.Skipped {
		taken[k.Branch] = true
	}
	refs, err := gitEnv(dir, nil, nil, "for-each-ref", "--contains", line.Commits[0], "--format=%(objectname) %(upstream) %(refname:lstrip=2)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var sides []SideBranch
	upstream := map[string]bool{}
	for _, r := range strings.Split(refs, "\n") {
		tip, rest, _ := strings.Cut(r, " ")
		up, name, _ := strings.Cut(rest, " ")
		upstream[name] = up != ""
		if name == "" || taken[name] || pos[tip] > 0 || (guard.Protected != nil && guard.Protected(name)) {
			continue
		}
		fork, err := gitEnv(dir, nil, nil, "merge-base", tip, line.Head)
		if err != nil {
			return nil, err
		}
		if pos[fork] == 0 {
			continue
		}
		if shared, err := inHistory(dir, fork, guard.History); err != nil {
			return nil, err
		} else if shared {
			continue
		}
		s := SideBranch{Branch: name, Tip: tip, Kind: SideForked, Fork: fork, Count: pos[fork], Backup: backup(name, sweep)}
		if fork == line.Head {
			s.Kind = SideAbove
		}
		if s.ForkSubject, err = gitEnv(dir, nil, nil, "log", "-1", "--format=%s", fork); err != nil {
			return nil, err
		}
		sides = append(sides, s)
	}
	slices.SortFunc(sides, func(a, b SideBranch) int { return strings.Compare(a.Branch, b.Branch) })
	// Which tip is below which, asked once per pair.
	below := map[[2]int]bool{}
	for i := range sides {
		for j := range sides {
			if i == j || sides[i].Tip == sides[j].Tip {
				continue
			}
			_, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", sides[i].Tip, sides[j].Tip)
			if err != nil {
				return nil, err
			}
			below[[2]int{i, j}] = code == 0
		}
	}
	for j := range sides {
		s := &sides[j]
		var under []int
		for i := range sides {
			if below[[2]int{i, j}] && !sides[i].Backup {
				under = append(under, i)
			}
		}
		// The nearest: every other branch below it is below this one too,
		// or at its tip.
		var nearest []string
		for _, i := range under {
			ok := true
			for _, k := range under {
				if k != i && sides[k].Tip != sides[i].Tip && !below[[2]int{k, i}] {
					ok = false
					break
				}
			}
			if ok && !slices.ContainsFunc(nearest, func(n string) bool { return tipOf(sides, n) == sides[i].Tip }) {
				nearest = append(nearest, sides[i].Branch)
			}
		}
		switch {
		case len(nearest) == 1:
			s.On = nearest[0]
		case len(under) > 0:
			var names []string
			for _, i := range under {
				names = append(names, sides[i].Branch)
			}
			s.Left = "it is stacked on more than one branch (" + strings.Join(names, ", ") + "), so where it goes cannot be told"
		}
	}
	if err := groupSameTips(dir, sides, upstream); err != nil {
		return nil, err
	}
	for i := range sides {
		s := &sides[i]
		s.Base = s.Fork
		if s.On != "" {
			s.Base = tipOf(sides, s.On)
		}
		n, err := gitEnv(dir, nil, nil, "rev-list", "--count", s.Base+".."+s.Tip)
		if err != nil {
			return nil, err
		}
		s.Commits, _ = strconv.Atoi(n)
		merge, err := gitEnv(dir, nil, nil, "log", "--merges", "-1", "--format=%h %s", s.Base+".."+s.Tip)
		if err != nil {
			return nil, err
		}
		switch {
		case s.Backup:
			s.Left = "a backup name: a backup marks a point in time, and wt never moves it"
		case merge != "":
			s.Merges = true
			s.Left = fmt.Sprintf("it has a merge commit (%s), which a replay would drop: move it by hand", merge)
		}
	}
	sides = orderSides(sides)
	// Parents first: a branch on one that is left is left too.
	left := map[string]string{}
	for i := range sides {
		s := &sides[i]
		if why, ok := left[s.On]; ok && s.Left == "" {
			s.Left = "it is stacked on " + s.On + ", which is left (" + why + ")"
		}
		if s.Left != "" {
			left[s.Branch] = s.Left
		}
	}
	return sides, nil
}

// inHistory is whether commit is one of history's, or below one of them;
// a ref that does not resolve has none.
func inHistory(dir, commit string, history []string) (bool, error) {
	for _, h := range history {
		if h == "" {
			continue
		}
		if _, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", h+"^{commit}"); err != nil {
			continue
		}
		_, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", commit, h)
		if err != nil {
			return false, err
		}
		if code == 0 {
			return true, nil
		}
	}
	return false, nil
}

// groupSameTips makes siblings of the branches at one tip: each names the
// others in SameTip, every one but the one that stands for them moves With
// it, and a branch stacked on any of them is on that one.
func groupSameTips(dir string, sides []SideBranch, upstream map[string]bool) error {
	at := map[string][]int{}
	for i := range sides {
		at[sides[i].Tip] = append(at[sides[i].Tip], i)
	}
	stands := map[string]string{}
	for tip, group := range at {
		if len(group) < 2 {
			continue
		}
		var names []string
		for _, i := range group {
			if !sides[i].Backup {
				names = append(names, sides[i].Branch)
			}
		}
		rep, err := standsFor(dir, names, upstream)
		if err != nil {
			return err
		}
		stands[tip] = rep
		for _, i := range group {
			for _, k := range group {
				if k != i {
					sides[i].SameTip = append(sides[i].SameTip, sides[k].Branch)
				}
			}
			if !sides[i].Backup && sides[i].Branch != rep {
				sides[i].With = rep
			}
		}
	}
	for i := range sides {
		if rep := stands[tipOf(sides, sides[i].On)]; sides[i].On != "" && rep != "" {
			sides[i].On = rep
		}
	}
	return nil
}

// standsFor is which of names, branches at one tip and sorted, stands for
// them: one with an upstream, then the oldest by its reflog, then the
// first by name. Never a backup; "" when every one is.
func standsFor(dir string, names []string, upstream map[string]bool) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	born := map[string]int64{}
	for _, n := range names {
		// The reflog, newest first: its last entry is when the branch was
		// made, read from the entry's own date (%gd, refs/heads/x@{<unix>}),
		// not its commit's. A branch with none sorts after one with a date.
		out, _, err := gitEnvAllow(dir, nil, nil, 128, "reflog", "show", "--date=unix", "--format=%gd", "refs/heads/"+n, "--")
		if err != nil {
			return "", err
		}
		f := strings.Fields(out)
		if len(f) > 0 {
			last := f[len(f)-1]
			if i := strings.LastIndex(last, "@{"); i >= 0 {
				born[n], _ = strconv.ParseInt(strings.TrimSuffix(last[i+2:], "}"), 10, 64)
			}
		}
	}
	best := slices.Clone(names)
	slices.SortStableFunc(best, func(a, b string) int {
		if upstream[a] != upstream[b] {
			if upstream[a] {
				return -1
			}
			return 1
		}
		ba, bb := born[a], born[b]
		switch {
		case ba == bb:
			return 0
		case ba == 0:
			return 1
		case bb == 0:
			return -1
		}
		return cmp.Compare(ba, bb)
	})
	return best[0], nil
}

func tipOf(sides []SideBranch, name string) string {
	for _, s := range sides {
		if s.Branch == name {
			return s.Tip
		}
	}
	return ""
}

// orderSides puts sides parents first: the roots by where they fork, then
// by name, each followed by the branches that move with it and then what is
// stacked on it, by name.
func orderSides(sides []SideBranch) []SideBranch {
	children, with := map[string][]SideBranch{}, map[string][]SideBranch{}
	var roots []SideBranch
	for _, s := range sides {
		switch {
		case s.With != "":
			with[s.With] = append(with[s.With], s)
		case s.On == "":
			roots = append(roots, s)
		default:
			children[s.On] = append(children[s.On], s)
		}
	}
	slices.SortStableFunc(roots, func(a, b SideBranch) int {
		if a.Count != b.Count {
			return a.Count - b.Count
		}
		return strings.Compare(a.Branch, b.Branch)
	})
	out := make([]SideBranch, 0, len(sides))
	var walk func(SideBranch)
	walk = func(s SideBranch) {
		out = append(out, s)
		out = append(out, with[s.Branch]...)
		for _, c := range children[s.Branch] {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// ForkPoints is the commits of the line the sides' roots fork from below
// the line's top, once each, bottom first: what a rebase of the line marks
// (StackRequest.Forks).
func ForkPoints(sides []SideBranch) []string {
	var out []string
	count := map[string]int{}
	for _, s := range sides {
		if s.On == "" && s.Kind == SideForked && !s.Backup && count[s.Fork] == 0 {
			out = append(out, s.Fork)
			count[s.Fork] = s.Count
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return count[a] - count[b] })
	return out
}

// ForkMapPrefix is where a rebase of a stack held on one line has git
// write where each fork commit of its side branches lands:
// refs/wt-stack-fork/<epoch>/<n>, n from 1 at the bottom. Not under
// StackMapPrefix: a layer's marker is read by its number.
const ForkMapPrefix = "refs/wt-stack-fork/"

// ForkMapRef names the marker of fork n (from 1) in the run of epoch.
func ForkMapRef(epoch int64, n int) string {
	return ForkMapPrefix + strconv.FormatInt(epoch, 10) + "/" + strconv.Itoa(n)
}

// isForkMark is whether the fields of a list command are a fork's marker,
// which nothing but the run's read of its forks looks at: it is no layer's
// end, and the stops of the layers and the checks pass over it.
func isForkMark(f []string) bool {
	return len(f) >= 2 && f[0] == "update-ref" && strings.HasPrefix(f[1], ForkMapPrefix)
}

// planForks reads, from the list a rebase of the line started with, where
// each fork commit's marker lands, as planLayers reads a layer's: after the
// last commit at or below the fork that the list keeps, counted and with
// the subject that commit will carry.
func planForks(dir string, epoch int64, line Line, forks []string, todo string) ([]LayerPlan, error) {
	if len(forks) == 0 {
		return nil, nil
	}
	pos := map[string]int{}
	for i, c := range line.Commits {
		pos[c] = i + 1
	}
	at := line
	at.Layers = nil
	for _, f := range forks {
		at.Layers = append(at.Layers, LineLayer{Branch: "fork " + git.ShortID(f, 7), Tip: f, Count: pos[f]})
	}
	return planMarks(dir, at, todo, func(n int) string { return ForkMapRef(epoch, n) })
}

// CheckMarker checks one marker of a finished rebase of the line against
// its plan, as CheckLayers checks every layer's: why is "" and tip the
// marker's commit when it holds, and otherwise says what is wrong.
func CheckMarker(dir, head, onto string, p LayerPlan) (tip, why string, err error) {
	tip, ok, err := refTip(dir, p.Ref)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "the rebase did not write its marker " + p.Ref, nil
	}
	if _, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", tip, head); err != nil {
		return "", "", err
	} else if code == 1 {
		return "", fmt.Sprintf("its marker %s is not on the rebased line", git.ShortID(tip, 7)), nil
	}
	if _, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", onto, tip); err != nil {
		return "", "", err
	} else if code == 1 {
		return "", fmt.Sprintf("its new tip %s does not descend from %s, where the line was put", git.ShortID(tip, 7), onto), nil
	}
	out, err := gitEnv(dir, nil, nil, "rev-list", "--count", onto+".."+tip)
	if err != nil {
		return "", "", err
	}
	if n, _ := strconv.Atoi(out); n != p.Expect {
		return "", fmt.Sprintf("its new tip %s has %d commits from the new base, and the plan expects %d", git.ShortID(tip, 7), n, p.Expect), nil
	}
	if p.After != "" {
		subject, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", tip)
		if err != nil {
			return "", "", err
		}
		if subject != p.Subject {
			return "", fmt.Sprintf("its new tip %s is %q, and the plan expects %q", git.ShortID(tip, 7), subject, p.Subject), nil
		}
	}
	return tip, "", nil
}

// ReplaySide replays the commits base..tip onto onto in the object store,
// oldest first, as a rebase would: each commit's change merged onto the new
// parent, with its author, date and message and a new committer. A commit
// whose change the new parent already has is dropped, and a commit that was
// empty to begin with is kept. Nothing else is touched: no ref, no index,
// no worktree. A conflict returns no tip and says which commit and paths.
func ReplaySide(dir, base, tip, onto string) (newTip, conflict string, err error) {
	out, err := gitEnv(dir, nil, nil, "rev-list", "--reverse", "--topo-order", base+".."+tip)
	if err != nil {
		return "", "", err
	}
	parent := onto
	for _, c := range strings.Fields(out) {
		was, err := gitEnv(dir, nil, nil, "rev-parse", c+"^", c+"^{tree}", c+"^^{tree}")
		if err != nil {
			return "", "", err
		}
		f := strings.Fields(was)
		if len(f) != 3 {
			return "", "", fmt.Errorf("%s: its parent could not be read", git.ShortID(c, 7))
		}
		tree, clean, conflicts, _, err := mergeTree(dir, f[0], parent, c)
		if err != nil {
			return "", "", err
		}
		if !clean {
			var paths []string
			for _, x := range conflicts {
				paths = append(paths, x.Path)
			}
			subject, _ := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", c)
			return "", fmt.Sprintf("%s %q conflicts in %s", git.ShortID(c, 7), subject, strings.Join(paths, ", ")), nil
		}
		parentTree, err := gitEnv(dir, nil, nil, "rev-parse", parent+"^{tree}")
		if err != nil {
			return "", "", err
		}
		if tree == parentTree && f[1] != f[2] {
			continue // its change is on the new parent already
		}
		if parent, err = replayCommit(dir, c, tree, parent); err != nil {
			return "", "", err
		}
	}
	return parent, "", nil
}

// replayCommit commits tree on parent with c's message, author and author
// date; the committer is whoever runs it, as in a rebase.
func replayCommit(dir, c, tree, parent string) (string, error) {
	raw, err := gitEnv(dir, nil, nil, "cat-file", "commit", c)
	if err != nil {
		return "", err
	}
	_, message, _ := strings.Cut(raw, "\n\n")
	who, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%an%x00%ae%x00%ad", "--date=raw", c)
	if err != nil {
		return "", err
	}
	f := strings.Split(who, "\x00")
	if len(f) != 3 {
		return "", fmt.Errorf("the author of %s could not be read", git.ShortID(c, 7))
	}
	env := []string{"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2]}
	return gitEnv(dir, env, strings.NewReader(message+"\n"), "commit-tree", "--no-gpg-sign", tree, "-p", parent, "-F", "-")
}

// TwinByHand is how a branch at the very tip of rep, held with it, is
// moved once rep has: by a person, since no commit it could be checked to
// land on existed when the run held them.
func TwinByHand(rep string) string {
	return "move " + rep + " first (its own command), then move this branch by hand onto " + rep +
		"'s new tip, once you have checked that it carries all of this branch's commits"
}

// twinCommand moves s, held after rep, the branch it moves with, has moved,
// onto the commit rep went to, as one shell line whose guards refuse,
// changing nothing, unless both tips are what the run left: s still at its
// old tip (no commit or merge of its own to replay), and rep at its new
// tip, so the pair never splits. Its old tip is the upstream, so nothing of
// it is replayed; git rebase moves a branch with nothing to replay, where
// wt sync rebase skips it. Every ref but the branch rebase checks out is
// refs/heads/, quoted, or a commit, so no tag of the same name is taken;
// the branch argument rebase resolves under refs/heads/ first.
// --no-autostash, as the run's own rebase, keeps the refusal over tracked
// changes when rebase.autoStash is set. "" while rep has not moved: no
// commit exists to check it against, and it is moved by hand (TwinByHand).
func twinCommand(s SideBranch, rep SideCarry, path string, inWorktree bool) string {
	if rep.Move.Result != MoveMoved && rep.Move.Result != MoveUnchanged {
		return ""
	}
	g := "git"
	if inWorktree {
		g = "git -C " + shellQuote(path)
	}
	tip := func(branch string) string {
		return fmt.Sprintf(`"$(%s rev-parse --verify --quiet %s)"`, g, shellQuote("refs/heads/"+branch))
	}
	return fmt.Sprintf("test %s = %s && test %s = %s && %s rebase --no-update-refs --no-autostash --onto %s %s %s",
		tip(s.Branch), s.Tip, tip(s.With), rep.New, g, rep.New, s.Tip, shellQuote(s.Branch))
}

// twinsHeld is why the branches that move with s, at its very tip, hold it
// too: the first of them its worktree holds, said as the same commit. ""
// when none is held, and for a branch alone at its tip.
func twinsHeld(s SideBranch, sides []SideBranch, worktrees repo.Worktrees, opts MoveOptions) string {
	for _, t := range sides {
		if t.With != s.Branch {
			continue
		}
		if why := twinHeld(t.Branch, worktrees, opts); why != "" {
			return "it is the same commit as " + t.Branch + ", which is held (" + why + "): they move together"
		}
	}
	return ""
}

// twinHeld is what holds branch where it is checked out, as a move reads
// it (moveLayer): a rebase or bisect elsewhere, or what holds its worktree
// and --force does not move. "" when nothing does, or it is in none.
func twinHeld(branch string, worktrees repo.Worktrees, opts MoveOptions) string {
	use, ok := worktrees.Users()[branch]
	if !ok {
		return ""
	}
	if heldBy(branch, worktrees, use) != "" {
		return "a " + use.By + " in " + use.Path + " holds it"
	}
	gitDir, err := GitDir(use.Path)
	if err != nil {
		return err.Error()
	}
	// A live lock makes its move hold it (moveLayer): read it here, so the
	// branch it moves with is held before either moves.
	if l, ok, err := ReadLock(gitDir); err != nil {
		return "its lock could not be read: " + err.Error()
	} else if ok && LockLive(l, opts.Now) {
		return "locked: " + lockHeld(l, opts.Now).Error()
	}
	h := holderAt(LayerHolder{Branch: branch, Path: use.Path}, gitDir, opts.Listed, opts.Scan, opts.ScanErr)
	switch {
	case !h.Holds() || (opts.Force && h.Forcible()):
		return ""
	case h.Forcible():
		return HolderWhy(h) + " (--force moves it)"
	}
	return HolderWhy(h)
}

// SideCarry is what a run came to for one side branch: where it goes, its
// replayed tip, and its move, which Result says (a backup is always held,
// with no command).
type SideCarry struct {
	Side    SideBranch
	NewBase string
	New     string
	Move    LayerMove
}

// CarryOptions is what CarrySides needs besides the record.
type CarryOptions struct {
	MoveOptions
	// Head and Onto are the rebased line's tip as the rebase left it and
	// where the line was put: each fork's marker is checked against them as
	// a layer's is. Forks is the fork plans, brought in line with what the
	// rebase dropped as the layers' are.
	Head, Onto string
	Forks      []LayerPlan
	// Top is where a branch above the line's top goes: the line's tip now.
	// Empty with TopWhy, why it is not known.
	Top, TopWhy string
	// Leave is why no side branch moves at all (--alone); each is held with
	// the command that moves it. Empty carries them.
	Leave string
}

// CarrySides carries each of rec.Sides, parents first, once the layers have
// moved: a branch forked mid-line onto where the rebase put its fork
// commit, read from the run's own marker and checked; one above the top
// onto the line's tip; one stacked on another side branch onto that
// branch's new tip. Its own commits are replayed in the object store
// (ReplaySide) and the branch moves as a layer does (moveLayer): guarded by
// its old tip, in its worktree only while that is clean and idle, pinned
// under the run's epoch so wt sync undo puts it back, and recorded in
// rec.Moves before it moves and once it has. A backup never moves. Any
// branch that does not move is held, with the command that moves it later
// where its new base is known. An error is the record that could not be
// written, or the moves stopped by a test's hook; rec says what moved.
func CarrySides(mainRoot string, rec *StackRecord, opts CarryOptions) ([]SideCarry, error) {
	if len(rec.Sides) == 0 {
		return nil, nil
	}
	forkNew, forkWhy := map[string]string{}, map[string]string{}
	for _, p := range opts.Forks {
		tip, why, err := CheckMarker(mainRoot, opts.Head, opts.Onto, p)
		if err != nil {
			return nil, err
		}
		if why != "" {
			forkWhy[p.Old] = "where its fork " + git.ShortID(p.Old, 7) + " went cannot be told: " + why
			continue
		}
		forkNew[p.Old] = tip
	}
	worktrees, err := (&repo.Repo{MainRoot: mainRoot}).Worktrees()
	if err != nil {
		return nil, err
	}
	users := worktrees.Users()
	save := func() error { return WriteStackRecord(mainRoot, rec) }
	out := make([]SideCarry, 0, len(rec.Sides))
	byName := map[string]SideCarry{}
	for _, s := range rec.Sides {
		c := SideCarry{Side: s}
		m := LayerMove{Branch: s.Branch, Old: s.Tip, Result: MovePlanned}
		use, inWorktree := users[s.Branch]
		inMain := inWorktree && slices.ContainsFunc(worktrees, func(w repo.Worktree) bool { return w.IsMain && samePath(w.Path, use.Path) })
		command := func(onto string) string {
			if onto == "" || s.Merges {
				return ""
			}
			if s.With != "" {
				return twinCommand(s, byName[s.With], use.Path, inWorktree)
			}
			if onto == s.On {
				onto = shellQuote(onto)
			}
			switch {
			case inMain:
				// wt sync rebase takes no main checkout: git there, with no
				// other branch dragged along.
				return fmt.Sprintf("git -C %s rebase --no-update-refs --onto %s %s %s", shellQuote(use.Path), onto, s.Base, shellQuote(s.Branch))
			case inWorktree:
				return fmt.Sprintf("wt sync rebase %s --onto %s --upstream %s", shellQuote(s.Branch), onto, s.Base)
			}
			return fmt.Sprintf("git rebase --no-update-refs --onto %s %s %s", onto, s.Base, shellQuote(s.Branch))
		}
		why, onto := "", ""
		switch {
		case s.With != "":
			// It moves with the branch that stands for its tip, onto the
			// very commit that one went to, or is held with it; its command,
			// whatever holds it, goes onto that branch.
			rep := byName[s.With]
			c.NewBase, onto = rep.NewBase, s.With
			switch rep.Move.Result {
			case MoveMoved, MoveUnchanged:
			default:
				// No commit it could be checked to land on exists yet: it
				// gets no command, and a person moves it.
				byHand := "it is the same commit as " + s.With + ", which is held too: " + TwinByHand(s.With)
				if own := twinHeld(s.Branch, worktrees, opts.MoveOptions); own != "" {
					byHand = own + "; " + byHand
				}
				why = byHand
			}
		case s.On != "":
			parent := byName[s.On]
			switch parent.Move.Result {
			case MoveMoved, MoveUnchanged:
				c.NewBase = parent.New
			default:
				why, onto = "it is stacked on "+s.On+", which did not move: move "+s.On+" first", s.On
			}
		case s.Kind == SideAbove:
			c.NewBase, why = opts.Top, opts.TopWhy
		default:
			c.NewBase, why = forkNew[s.Fork], forkWhy[s.Fork]
			if c.NewBase == "" && why == "" {
				why = "the rebase planned no marker for its fork " + git.ShortID(s.Fork, 7)
			}
		}
		if onto == "" {
			onto = c.NewBase
		}
		hold := func(why, cmd string) { m.Result, m.Held, m.Command = MoveHeld, why, cmd }
		twins := ""
		if why == "" && s.Left == "" && opts.Leave == "" && !s.Backup {
			twins = twinsHeld(s, rec.Sides, worktrees, opts.MoveOptions)
		}
		switch {
		case s.Backup:
			hold(s.Left, "")
		case why != "":
			hold(why, command(onto))
		case s.Left != "":
			hold(s.Left, command(onto))
		case opts.Leave != "":
			hold(opts.Leave, command(onto))
		case s.With != "":
			c.New = byName[s.With].New
		case twins != "":
			hold(twins, command(onto))
		case c.NewBase == s.Base:
			c.New, m.New, m.Result = s.Tip, s.Tip, MoveUnchanged
		case s.Commits == 0 && s.On != "":
			c.New = c.NewBase
		default:
			tip, conflict, err := ReplaySide(mainRoot, s.Base, s.Tip, c.NewBase)
			if err != nil {
				return out, err
			}
			if conflict != "" {
				hold("its commits do not replay cleanly where it goes: "+conflict, command(onto))
			}
			c.New = tip
		}
		if m.Result == MovePlanned && c.New == s.Tip {
			m.New, m.Result = c.New, MoveUnchanged
		}
		rec.Moves = append(rec.Moves, m)
		k := len(rec.Moves) - 1
		if m.Result == MovePlanned {
			rec.Moves[k].New = c.New
			if err := save(); err != nil {
				return out, err
			}
			merr := moveLayer(mainRoot, rec.Epoch, &rec.Moves[k], opts.MoveOptions, save)
			var stop stopped
			if errors.As(merr, &stop) {
				c.Move = rec.Moves[k]
				return append(out, c), stop.error
			}
			if rec.Moves[k].Result == MoveHeld {
				rec.Moves[k].Command = command(onto)
			}
			if err := save(); err != nil {
				return out, err
			}
			if merr != nil {
				c.Move = rec.Moves[k]
				return append(out, c), merr
			}
			if opts.AfterMove != nil {
				if err := opts.AfterMove(rec.Moves[k]); err != nil {
					c.Move = rec.Moves[k]
					return append(out, c), err
				}
			}
		} else if err := save(); err != nil {
			return out, err
		}
		c.Move = rec.Moves[k]
		out = append(out, c)
		byName[s.Branch] = c
	}
	return out, nil
}
