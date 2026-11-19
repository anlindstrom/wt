package wtsync

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
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
	// itself. A branch at the very same tip as another is on that one, with
	// no commit of its own. A backup is never what a branch is on.
	On string
	// Base is where its own commits start: On's tip, or Fork. Commits is how
	// many there are from Base to Tip.
	Base    string
	Commits int
	// Backup says its name is a backup (wt refs sweep's patterns).
	Backup bool
	// Left is why a run never moves it: a backup, a merge commit, more than
	// one branch below it, or the branch it is on being left. "" for a
	// branch a run carries.
	Left string
}

// ReadSides reads the branches that hang off line, every one a local branch
// other than the line's own, its layers and its skipped branches, whose tip
// contains the line's first commit and is not on the line. They come
// parents first: each root in the order of where it forks (then by name),
// and after it what is stacked on it, by name. A branch matching backups
// (wt refs sweep's patterns, matched by backup(), as ReadLine matches them)
// is listed and left: a backup marks a point in time, and moving it would
// lose what it records.
func ReadSides(dir string, line Line, backups []string) ([]SideBranch, error) {
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
	refs, err := gitEnv(dir, nil, nil, "for-each-ref", "--contains", line.Commits[0], "--format=%(refname:lstrip=2) %(objectname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var sides []SideBranch
	for _, r := range strings.Split(refs, "\n") {
		name, tip, _ := strings.Cut(r, " ")
		if name == "" || taken[name] || pos[tip] > 0 {
			continue
		}
		fork, err := gitEnv(dir, nil, nil, "merge-base", tip, line.Head)
		if err != nil {
			return nil, err
		}
		if pos[fork] == 0 {
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
		// A twin: the first non-backup branch by name at the same tip.
		for i := range sides {
			if i < j && sides[i].Tip == s.Tip && !sides[i].Backup {
				s.On = sides[i].Branch
				break
			}
		}
		if s.On != "" {
			continue
		}
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
			s.Left = fmt.Sprintf("it has a merge commit (%s), which a replay would drop", merge)
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

func tipOf(sides []SideBranch, name string) string {
	for _, s := range sides {
		if s.Branch == name {
			return s.Tip
		}
	}
	return ""
}

// orderSides puts sides parents first: the roots by where they fork, then
// by name, each followed by what is stacked on it, by name.
func orderSides(sides []SideBranch) []SideBranch {
	children := map[string][]SideBranch{}
	var roots []SideBranch
	for _, s := range sides {
		if s.On == "" {
			roots = append(roots, s)
		} else {
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
