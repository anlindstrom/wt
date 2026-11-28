package wtsync

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/anlindstrom/wt/internal/git"
)

// ContextGap is one hunk of a fixup!, squash! or amend! commit that does not
// apply where autosquash puts it, right behind its target: the hunk's
// context only a later commit of the line adds. A fixup written at the top
// of a line and folded into a commit low on it carries such hunks.
type ContextGap struct {
	Fixup         string `json:"fixup"`
	FixupSubject  string `json:"fixupSubject"`
	Target        string `json:"target"`
	TargetSubject string `json:"targetSubject"`
	// Path and Lines are the hunk: its file, and the lines it changes in
	// the fixup, "13" or "13-15".
	Path  string `json:"path"`
	Lines string `json:"lines"`
	// Needs is the first commit after the target, in line order, whose tree
	// has the hunk's context.
	Needs        string `json:"needs"`
	NeedsSubject string `json:"needsSubject"`
	// Doubt is why Needs is no place to retarget the hunk to, "" when the
	// order the fold takes confirms it. Kept with a stop; not in --json.
	Doubt string `json:"doubt,omitempty"`
	// Applies is set when the fold in place applies the fixup, so only
	// another run may stop at the gap. Not in --json.
	Applies bool `json:"applies,omitempty"`
}

// Line is what a person is told about the gap.
func (g ContextGap) Line() string {
	return fmt.Sprintf("fixup moved ahead of its context: %s:%s needs %s %q, which comes later on the line",
		g.Path, g.Lines, git.ShortID(g.Needs, 7), oneLinePlan(g.NeedsSubject))
}

// Retarget is the way out of a stop at the gap: not a resolution. It names
// no commit to retarget to when Needs is in doubt.
func (g ContextGap) Retarget(work string) string {
	if g.Doubt != "" {
		return fmt.Sprintf("not a resolution: wt sync undo %s puts everything back; no retarget is offered: %s", work, g.Doubt)
	}
	return fmt.Sprintf("not a resolution: wt sync undo %s, then move that hunk of %s into a fixup of %s (git commit --fixup=%s) and run again",
		work, git.ShortID(g.Fixup, 7), git.ShortID(g.Needs, 7), git.ShortID(g.Needs, 7))
}

// GapsAt is the gaps of the fold commit c, matched by id or by a prefix of
// one as the sequencer may abbreviate it.
func GapsAt(gaps []ContextGap, c string) []ContextGap {
	var out []ContextGap
	for _, g := range gaps {
		if c != "" && (g.Fixup == c || strings.HasPrefix(g.Fixup, c)) {
			out = append(out, g)
		}
	}
	return out
}

// FixupState is what the analysis found of one fixup.
type FixupState string

const (
	// FixupApplies means the fold of the line on its own base reaches the fixup,
	// which merges clean there, behind its target.
	FixupApplies FixupState = "applies"
	// FixupAheadOfContext means the fold does not apply the fixup, and a hunk
	// of it needs a later commit of the line (a ContextGap).
	FixupAheadOfContext FixupState = "ahead-of-context"
	// FixupConflicts means that fold first conflicts at the fixup, and no later
	// commit explains why. What .wt-sync.yaml resolves is not consulted.
	FixupConflicts FixupState = "conflicts"
	// FixupNotAnalysed means wt cannot say, for the reason in Why. Gaps may
	// still name it.
	FixupNotAnalysed FixupState = "not-analysed"
)

// FixupOutcome is what the analysis found of one fixup!, squash! or amend!
// commit of the line.
type FixupOutcome struct {
	Fixup, Subject string
	// Target is the commit it folds into, "" when its subject names no one
	// commit.
	Target string
	State  FixupState
	// Why is set for FixupNotAnalysed.
	Why string
}

// FixupAnalysis is what AnalyseFixups found of a line.
type FixupAnalysis struct {
	// Gaps is every hunk of a fixup that lacks its context at its target.
	Gaps []ContextGap
	// Unchecked is, one line per fixup, what of the gaps could not be told.
	Unchecked []string
	// Fixups is every fixup of the line, in line order.
	Fixups []FixupOutcome
}

// AnalyseFixups says what each fixup of the line comes to in the fold, names
// every hunk that lacks its context at its target, and notes what of that it
// could not tell. commits is the line, oldest first, and cfg the declaration
// a run of it reads, nil when none: its version lifts and layer steps are
// asked about.
//
// The gaps are read from the line's own trees (lineGaps), so that a run
// stopping at one finds it whatever order it takes, and, for a fixup only the
// fold's order stops, from the tree it stops on (stopGaps). The outcomes are
// read
// from the line replayed on its own base as the fold orders it (replayFold),
// before any move onto another base. A fixup that replay applies, behind a
// target git keeps, applies, and has no note; its gaps stand, for a run onto
// another base. Nothing but objects in the store is written: no ref, no index
// and no worktree. An error means nothing was analysed.
func AnalyseFixups(dir string, commits []string, cfg *Config) (FixupAnalysis, error) {
	a, _, err := analyseFixups(dir, commits, cfg, false)
	return a, err
}

// analyseFixups is AnalyseFixups with the replay it made, which with toTip
// goes on to the line's last commit instead of ending at the last fold target.
func analyseFixups(dir string, commits []string, cfg *Config, toTip bool) (FixupAnalysis, foldReplay, error) {
	var none FixupAnalysis
	var run foldReplay
	if len(commits) == 0 {
		return none, run, nil
	}
	out, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H%x00%s"}, commits...)...)
	if err != nil {
		return none, run, err
	}
	var all []lineCommit
	for _, l := range strings.Split(out, "\n") {
		if sha, subject, ok := strings.Cut(l, "\x00"); ok {
			all = append(all, lineCommit{sha, subject})
		}
	}
	index := map[string]int{}
	for i, e := range all {
		index[e.sha] = i
	}
	targetOf := map[int]int{} // the commit each fixup folds into
	for i, e := range all {
		if j, ok := foldTarget(dir, all[:i], e.subject, index); ok {
			targetOf[i] = j
		}
	}
	blobs := blobCache{dir: dir, got: map[string]*string{}}
	lines, err := lineGaps(dir, blobs, all, targetOf)
	if err != nil {
		return none, run, err
	}
	run, err = replayFold(dir, all, targetOf, cfg, toTip)
	if err != nil {
		return none, run, err
	}
	paths := pathCache{dir: dir, got: map[string][]string{}}
	var a FixupAnalysis
	for i, e := range all {
		j, ok := targetOf[i]
		if !ok {
			if isFold(e.subject) {
				a.Fixups = append(a.Fixups, FixupOutcome{Fixup: e.sha, Subject: e.subject, State: FixupNotAnalysed,
					Why: "its subject names no one commit earlier on the line"})
			}
			continue
		}
		line := lines[i]
		outcome := FixupOutcome{Fixup: e.sha, Subject: e.subject, Target: all[j].sha, State: FixupNotAnalysed}
		_, applied := run.after[e.sha]
		stopsHere, unreached := run.stop == i, run.unreached
		if applied {
			step, err := layerStepOver(cfg, paths, run.upTo(i))
			if err != nil {
				return none, run, err
			}
			if step != "" {
				applied, unreached = false, fmt.Sprintf("the layer step %q may change what it, or a commit before it, is merged onto", step)
			}
		}
		if applied {
			if run.emptied[all[j].sha] {
				outcome.Why = fmt.Sprintf("git drops its target %s as empty, so it folds into the commit below", git.ShortID(all[j].sha, 7))
			} else {
				outcome.State = FixupApplies
			}
			for _, g := range line.gaps {
				g.Applies = outcome.State == FixupApplies
				g.Doubt = "the fold in place applies this fixup, which says nothing of where another run needs it"
				a.Gaps = append(a.Gaps, g)
			}
			a.Fixups = append(a.Fixups, outcome)
			continue
		}
		verdict := "; wt cannot tell whether this fixup applies: " + unreached
		if stopsHere {
			verdict = "; the fold in place conflicts at this fixup (a run with --onto may differ, and .wt-sync.yaml may resolve the file)"
		}
		who := fmt.Sprintf("%s %q", git.ShortID(e.sha, 7), oneLinePlan(e.subject))
		// Where the fold stops, its own tree may lack what the line's trees
		// have, or stop in a file the note of the line says nothing of.
		var whole laterFile
		if stopsHere {
			more, hunks, w, err := stopGaps(dir, blobs, all, j, i, run.tree, run.conflicted)
			if err != nil {
				return none, run, err
			}
			whole = w
			for n, g := range more {
				if !slices.ContainsFunc(line.gaps, func(have ContextGap) bool { return have.Path == g.Path && have.Lines == g.Lines }) {
					line.gaps, line.hunks = append(line.gaps, g), append(line.hunks, hunks[n])
				}
			}
		}
		switch {
		case len(line.gaps) > 0 || line.note != "":
			cur := line.onto
			if stopsHere {
				cur = run.tree
			}
			view := foldView{dir: dir, all: all, targetOf: targetOf, blobs: blobs, paths: paths,
				j: j, i: i, cur: cur, stopsHere: stopsHere, conflicted: run.conflicted}
			outcome.State = FixupAheadOfContext
			var said []string
			if line.note != "" {
				outcome.State = FixupNotAnalysed
				said = append(said, line.note)
			}
			for n, g := range line.gaps {
				h := line.hunks[n]
				m, found, err := view.placed(g.Path, h.before, isFold(all[h.first].subject))
				if err != nil {
					return none, run, err
				}
				switch {
				case !found:
					g.Doubt = "wt cannot tell which commit first has the context in the fold"
					outcome.State = FixupNotAnalysed
				case all[m].sha != g.Needs:
					g.Doubt = fmt.Sprintf("the fold first has the context at %s %q", git.ShortID(all[m].sha, 7), oneLinePlan(all[m].subject))
				case isFold(g.NeedsSubject):
					g.Doubt = "the commit it needs is a fixup, which a retarget cannot name"
				}
				if g.Doubt != "" {
					said = append(said, fmt.Sprintf("%s:%s: %s", g.Path, g.Lines, g.Doubt))
				}
				a.Gaps = append(a.Gaps, g)
			}
			if len(said) > 0 {
				note := strings.Join(said, "; ") + verdict
				a.Unchecked = append(a.Unchecked, who+": "+note)
				if outcome.State == FixupNotAnalysed {
					outcome.Why = note
				}
			}
		case whole.path != "":
			has := "has " + whole.path + " as the fixup changes it"
			if whole.absent {
				has = "is without " + whole.path + ", as the fixup finds it"
			}
			outcome.Why = fmt.Sprintf("%s %q %s%s", git.ShortID(all[whole.k].sha, 7), oneLinePlan(all[whole.k].subject), has, verdict)
		case stopsHere:
			outcome.State = FixupConflicts
		default:
			outcome.Why = unreached
		}
		a.Fixups = append(a.Fixups, outcome)
	}
	return a, run, nil
}

// lineFixup is what the line's own trees say of one fixup.
type lineFixup struct {
	// onto is the tree it was merged onto.
	onto string
	// gaps is its hunks that lack their context there, and hunks, for each,
	// what lineGaps read.
	gaps  []ContextGap
	hunks []gapHunk
	// note is what of it could not be told, in place of its gaps.
	note string
}

// gapHunk is the lines a gap's hunk needs, and the first commit of the line
// after the target whose tree has them.
type gapHunk struct {
	before []string
	first  int
}

// lineGaps is, per fixup, what lacks its context behind its target. Each
// fixup is merged, in the object store, onto its target's tree with the
// fixups of the same target before it already folded, from its own parent; a
// file that does not merge clean is looked at hunk by hunk, and a hunk whose
// lines before the change are not in the target's file but are in a later
// commit's names the first such commit. A conflict no later commit explains
// is not a gap.
func lineGaps(dir string, blobs blobCache, all []lineCommit, targetOf map[int]int) (map[int]lineFixup, error) {
	folded := map[int]string{} // a target's tree with its fixups so far
	// conflicted is, per target, the first fixup of it that conflicted
	// there: what a fixup after it lacks may be that one's conflicting part,
	// which the analysis cannot fold in.
	conflicted := map[int]int{}
	isFoldAt := map[int]bool{}
	// cleanFold is the fixups that merge clean onto their target.
	cleanFold := map[int]bool{}
	for i, e := range all {
		isFoldAt[i] = isFold(e.subject)
	}
	found := map[int]lineFixup{}
	var err error
	for i, e := range all {
		j, ok := targetOf[i]
		if !ok {
			continue
		}
		cur, ok := folded[j]
		if !ok {
			if cur, err = gitEnv(dir, nil, nil, "rev-parse", all[j].sha+"^{tree}"); err != nil {
				return nil, err
			}
		}
		parent, err := gitEnv(dir, nil, nil, "rev-parse", e.sha+"^^{tree}")
		if err != nil {
			return nil, err
		}
		tree, clean, conflicts, _, err := mergeTree(dir, parent, cur, e.sha)
		if err != nil {
			return nil, err
		}
		if clean {
			folded[j] = tree
			cleanFold[i] = true
			found[i] = lineFixup{onto: cur}
			continue
		}
		// The fixups after this one are checked against its clean part:
		// the merge with each conflicted file as the target had it.
		earlier, tainted := conflicted[j]
		if !tainted {
			conflicted[j] = i
		}
		if kept, err := cleanPart(dir, tree, cur, conflicts); err == nil {
			folded[j] = kept
		} else {
			folded[j] = cur
		}
		// Everything that lacks its context behind the target and is found
		// later is at risk: the files, the hunks and the commits that add
		// it. why is set when the analysis cannot place a hunk.
		why := ""
		atRiskFiles, atRiskLater := map[string]bool{}, map[int]bool{}
		atRiskHunks := 0
		var mine []ContextGap
		var read []gapHunk
		// replayed is the trees the commits after the target come to when
		// the fixups before this one are folded: built once, on first use.
		var replayed map[int]string
		replayDone := false
		for _, c := range conflicts {
			hunks, err := fixupHunks(dir, e.sha, c.Path)
			if err != nil {
				return nil, err
			}
			for _, h := range hunks {
				if len(h.before) == 0 {
					continue
				}
				if has, err := blobs.has(cur, c.Path, h.before); err != nil {
					return nil, err
				} else if has {
					continue
				}
				// foldedHas is whether this hunk's context is in commit t's
				// tree with its fixups folded in so far: what that commit
				// comes to in the rebase, not the tree of the fixup that
				// holds the context in the line. A tree that cannot be read
				// has nothing.
				foldedHas := func(t int) bool {
					tree, ok := folded[t]
					if !ok {
						tree = all[t].sha + "^{tree}"
					}
					has, err := blobs.has(tree, c.Path, h.before)
					return err == nil && has
				}
				for k := j + 1; k < i; k++ {
					later := all[k]
					has, err := blobs.has(later.sha+"^{tree}", c.Path, h.before)
					if err != nil {
						return nil, err
					}
					if !has {
						continue
					}
					atRiskFiles[c.Path] = true
					atRiskHunks++
					needs := k
					switch {
					case tainted && k >= earlier:
						// At or after an earlier fixup of the same target
						// that conflicted: the context may be its own,
						// which comes before, not later.
						if why == "" {
							why = fmt.Sprintf("the earlier fixup %s conflicts there too", git.ShortID(all[earlier].sha, 7))
						}
					case isFoldAt[k]:
						// A fixup of a later commit folds into that commit,
						// so what it adds is that commit's. Any other
						// fixup's share of it cannot be told.
						t, ok := targetOf[k]
						laterFold := ok && t > j && cleanFold[k]
						if laterFold && foldedHas(t) {
							needs = t
							break
						}
						if laterFold {
							// The context may be what that fold and the
							// commits up to a later one come to together.
							if !replayDone {
								replayDone = true
								if replayed, err = replayAfter(dir, all, isFoldAt, targetOf, cleanFold, cur, j, i); err != nil {
									return nil, err
								}
							}
							if m, found, err := firstReplayedWith(blobs, replayed, j, i, c.Path, h.before); err != nil {
								return nil, err
							} else if found {
								needs = m
								break
							}
						}
						if why == "" {
							why = fmt.Sprintf("the context is in the fixup %s, whose own fold wt cannot predict", git.ShortID(later.sha, 7))
						}
					}
					atRiskLater[needs] = true
					mine = append(mine, ContextGap{Fixup: e.sha, FixupSubject: e.subject, Target: all[j].sha, TargetSubject: all[j].subject,
						Path: c.Path, Lines: h.lines(), Needs: all[needs].sha, NeedsSubject: all[needs].subject})
					read = append(read, gapHunk{before: h.before, first: k})
					break
				}
			}
		}
		if why != "" {
			found[i] = lineFixup{onto: cur, note: fmt.Sprintf("not analysed: %s (%s) lack context behind %s that %s %s; %s",
				plural(len(atRiskFiles), "file"), plural(atRiskHunks, "hunk"),
				git.ShortID(all[j].sha, 7), plural(len(atRiskLater), "later commit"), addVerb(len(atRiskLater)), why)}
			continue
		}
		found[i] = lineFixup{onto: cur, gaps: mine, hunks: read}
	}
	return found, nil
}

// foldReplay is the line replayed on its own base as the fold orders it.
type foldReplay struct {
	// replayedLine is what was replayed; nothing when the line was not.
	replayedLine
	// stop is the commit the replay conflicts at, -1 when there is none, and
	// conflicted the files it conflicts in.
	stop       int
	conflicted []string
	// unreached is why a fixup the replay did not apply may not be reached.
	unreached string
	// first is the commit the replay starts at, and order the foldOrder from
	// there.
	first int
	order []int
	// all is the line that was read.
	all []lineCommit
}

// upTo is the commits of the fold of all up to fixup i: those below the
// replay, which are no fold's, then the replay's own as far as i.
func (r foldReplay) upTo(i int) []string {
	var commits []string
	for m := range r.first {
		if !isFold(r.all[m].subject) {
			commits = append(commits, r.all[m].sha)
		}
	}
	for _, c := range r.order {
		commits = append(commits, r.all[c].sha)
		if c == i {
			break
		}
	}
	return commits
}

// replayFold replays all for AnalyseFixups, from the first commit a fixup
// folds into to the last. A line the run refuses is not replayed, nor one
// whose fold takes an order wt does not follow, and the replay ends where
// the run may lift a version. With toTip it goes on to the last commit.
func replayFold(dir string, all []lineCommit, targetOf map[int]int, cfg *Config, toTip bool) (foldReplay, error) {
	r := foldReplay{stop: -1, all: all}
	for i, c := range all {
		if _, ok := targetOf[i]; !ok && isFold(c.subject) {
			r.unreached = fmt.Sprintf("the run refuses the line: %s %q names no one commit earlier on it", git.ShortID(c.sha, 7), oneLinePlan(c.subject))
			return r, nil
		}
	}
	if len(targetOf) == 0 {
		return r, nil
	}
	if r.unreached = unfollowed(all, targetOf); r.unreached != "" {
		return r, nil
	}
	targets := slices.Collect(maps.Values(targetOf))
	first, last := slices.Min(targets), slices.Max(targets)
	if toTip {
		last = len(all) - 1
	}
	start, err := gitEnv(dir, nil, nil, "rev-parse", all[first].sha+"^^{tree}")
	if err != nil {
		return r, err
	}
	order := foldOrder(all, targetOf, first, last)
	r.first, r.order = first, order
	if r.replayedLine, r.stop, r.conflicted, err = replayCommits(dir, all, order, start); err != nil {
		return r, err
	}
	if r.stop >= 0 {
		r.unreached = fmt.Sprintf("the fold in place conflicts at %s %q first", git.ShortID(all[r.stop].sha, 7), oneLinePlan(all[r.stop].subject))
	}
	at, err := mayLiftAt(dir, cfg, all, targetOf, order, start, r.after)
	if err != nil || at < 0 {
		return r, err
	}
	for _, c := range order[at:] {
		delete(r.after, all[c].sha)
	}
	c := all[order[at]]
	r.stop, r.conflicted = -1, nil
	r.unreached = fmt.Sprintf("the run may lift a version at %s %q, which changes what later fixups are merged onto", git.ShortID(c.sha, 7), oneLinePlan(c.subject))
	return r, nil
}

// unfollowed is why the order the fold takes all in is not one wt replays,
// "" when it is: a move! commit, or a fold into a fold, which autosquash
// places after the later folds of that one's own target.
func unfollowed(all []lineCommit, targetOf map[int]int) string {
	for i, c := range all {
		if isMove(c.subject) {
			return "the line has a move! commit, whose part wt does not fold in here"
		}
		if j, ok := targetOf[i]; ok && isFold(all[j].subject) {
			return fmt.Sprintf("the line folds %s into the fixup %s, an order wt does not replay", git.ShortID(c.sha, 7), git.ShortID(all[j].sha, 7))
		}
	}
	return ""
}

// foldOrder is the commits from first to last that are no fold, each followed
// by the folds into it, in line order.
func foldOrder(all []lineCommit, targetOf map[int]int, first, last int) []int {
	folds := map[int][]int{}
	for i := range all {
		if j, ok := targetOf[i]; ok {
			folds[j] = append(folds[j], i)
		}
	}
	var order []int
	for m := first; m <= last; m++ {
		if !isFold(all[m].subject) {
			order = append(append(order, m), folds[m]...)
		}
	}
	return order
}

// replayCommits replays the commits order names, a foldOrder, onto the tree
// start (replayLine): what came of it, the commit it conflicts at, -1 when
// none, and the files of that conflict.
func replayCommits(dir string, all []lineCommit, order []int, start string) (replayedLine, int, []string, error) {
	in := slices.Sorted(slices.Values(order))
	line := make([]moveCommit, len(in))
	for k, i := range in {
		line[k] = moveCommit{sha: all[i].sha, subject: all[i].subject}
	}
	stop := -1
	var conflicted []string
	res, _, err := replayLine(dir, line, nil, replayFrom{tree: start,
		conflict: func(k int, conflicts []Conflict) string {
			stop = in[k]
			for _, c := range conflicts {
				conflicted = append(conflicted, c.Path)
			}
			return "conflict"
		},
	})
	return res, stop, conflicted, err
}

// mayLiftAt is the first place in order, replayed from start with the trees
// after, where the run may lift a version (liftAt): a commit it picks, which
// is no fold, that changes a file a lifting rule claims and is applied on
// another copy of it than its parent's. -1 when there is none. A fold is
// lifted only where it conflicts, and nothing is said to apply past that.
func mayLiftAt(dir string, cfg *Config, all []lineCommit, targetOf map[int]int, order []int, start string, after map[string]string) (int, error) {
	patterns := cfg.liftPatterns()
	if len(patterns) == 0 {
		return -1, nil
	}
	on := start
	for at, i := range order {
		c := all[i].sha
		if _, fold := targetOf[i]; !fold {
			changed, err := changedBlobs(dir, c+"^", c)
			if err != nil {
				return -1, err
			}
			var claimed []string
			for p := range changed {
				if matchesAny(patterns, p) {
					claimed = append(claimed, p)
				}
			}
			if len(claimed) > 0 {
				differs, err := changedBlobs(dir, c+"^", on, claimed...)
				if err != nil || len(differs) > 0 {
					return at, err
				}
			}
		}
		tree, ok := after[c]
		if !ok {
			break
		}
		on = tree
	}
	return -1, nil
}

// rootOf is the commit that is no fold behind which i ends up: i itself, or
// the end of the targets from it.
func rootOf(targetOf map[int]int, i int) int {
	for {
		j, ok := targetOf[i]
		if !ok {
			return i
		}
		i = j
	}
}

// nestedUnder is whether the folds behind root include one into another fold.
func nestedUnder(targetOf map[int]int, root int) bool {
	for i, j := range targetOf {
		if _, ok := targetOf[j]; ok && rootOf(targetOf, i) == root {
			return true
		}
	}
	return false
}

// replayAfter is the tree each ordinary commit between target j and fixup i
// comes to in the fold, from start, the target's tree: each commit applied
// in line order, and the clean fixups of it seen before i folded in right
// behind it, as autosquash orders them. nil when the replay cannot be
// established: a commit or a fixup of one that does not merge clean, a
// fixup of one that conflicted, or a fixup of a commit below j seen before i,
// whose fold changes the trees from j on in a way the replay does not apply.
func replayAfter(dir string, all []lineCommit, isFoldAt map[int]bool, targetOf map[int]int, cleanFold map[int]bool, start string, j, i int) (map[int]string, error) {
	for f := 0; f < i; f++ {
		if t, ok := targetOf[f]; ok && t < j {
			return nil, nil
		}
	}
	trees := map[int]string{}
	cur := start
	apply := func(c int) (bool, error) {
		parent, err := gitEnv(dir, nil, nil, "rev-parse", all[c].sha+"^^{tree}")
		if err != nil {
			return false, err
		}
		tree, clean, _, _, err := mergeTree(dir, parent, cur, all[c].sha)
		if err != nil || !clean {
			return false, err
		}
		cur = tree
		return true, nil
	}
	for m := j + 1; m < i; m++ {
		if isFoldAt[m] {
			continue
		}
		if ok, err := apply(m); err != nil || !ok {
			return nil, err
		}
		for f := m + 1; f < i; f++ {
			if t, ok := targetOf[f]; !ok || t != m {
				continue
			}
			if !cleanFold[f] {
				return nil, nil
			}
			if ok, err := apply(f); err != nil || !ok {
				return nil, err
			}
		}
		trees[m] = cur
	}
	return trees, nil
}

// firstReplayedWith is the first commit after j, before i, whose replayed
// tree has the hunk's context in path.
func firstReplayedWith(blobs blobCache, trees map[int]string, j, i int, path string, before []string) (int, bool, error) {
	for m := j + 1; m < i; m++ {
		tree, ok := trees[m]
		if !ok {
			continue
		}
		has, err := blobs.has(tree, path, before)
		if err != nil {
			return 0, false, err
		}
		if has {
			return m, true, nil
		}
	}
	return 0, false, nil
}

// pathCache reads the files each commit changes once.
type pathCache struct {
	dir string
	got map[string][]string
}

func (p pathCache) of(c string) ([]string, error) {
	if paths, ok := p.got[c]; ok {
		return paths, nil
	}
	paths, err := pathsOfCommit(p.dir, c)
	if err == nil {
		p.got[c] = paths
	}
	return paths, err
}

// layerStepOver is the command of a layer step of cfg that owns a file one of
// commits changes, "" when none: it commits at a layer's end, and what comes
// above is merged onto its output. A step with no paths owns every file.
func layerStepOver(cfg *Config, paths pathCache, commits []string) (string, error) {
	for _, step := range cfg.LayerSteps() {
		if len(step.Paths) == 0 {
			return step.Run, nil
		}
		for _, c := range commits {
			changed, err := paths.of(c)
			if err != nil {
				return "", err
			}
			if slices.ContainsFunc(changed, func(p string) bool { return matchesAny(step.Paths, p) }) {
				return step.Run, nil
			}
		}
	}
	return "", nil
}

// laterFile is a file with no lines to look for, a binary one or one the
// fixup adds, as the fixup finds it in a commit of the line: "" path when
// there is none.
type laterFile struct {
	k      int
	path   string
	absent bool
}

// stopGaps is what of the files fixup i conflicts in, on the tree the fold
// stops it on, a commit between its target j and it has: a gap for each hunk
// whose lines that tree lacks, at the first commit of the line with them, as
// lineGaps reads one, and the first file with no hunk that a commit has as
// the fixup finds it.
func stopGaps(dir string, blobs blobCache, all []lineCommit, j, i int, tree string, conflicted []string) ([]ContextGap, []gapHunk, laterFile, error) {
	var gaps []ContextGap
	var read []gapHunk
	var whole laterFile
	blob := func(rev string) (string, error) {
		// An absent path is exit 1 under --quiet: absence, not an error.
		out, code, err := gitEnvAllow(dir, nil, nil, 1, "rev-parse", "--verify", "--quiet", rev)
		if err != nil || code != 0 {
			return "", err
		}
		return out, nil
	}
	for _, path := range conflicted {
		hunks, err := fixupHunks(dir, all[i].sha, path)
		if err != nil {
			return nil, nil, whole, err
		}
		for _, h := range hunks {
			if len(h.before) == 0 {
				continue
			}
			if has, err := blobs.has(tree, path, h.before); err != nil {
				return nil, nil, whole, err
			} else if has {
				continue
			}
			for k := j + 1; k < i; k++ {
				has, err := blobs.has(all[k].sha+"^{tree}", path, h.before)
				if err != nil {
					return nil, nil, whole, err
				}
				if has {
					gaps = append(gaps, ContextGap{Fixup: all[i].sha, FixupSubject: all[i].subject, Target: all[j].sha, TargetSubject: all[j].subject,
						Path: path, Lines: h.lines(), Needs: all[k].sha, NeedsSubject: all[k].subject})
					read = append(read, gapHunk{before: h.before, first: k})
					break
				}
			}
		}
		if whole.path != "" || slices.ContainsFunc(hunks, func(h hunk) bool { return len(h.before) > 0 }) {
			continue
		}
		before, err := blob(all[i].sha + "^:" + path)
		if err != nil {
			return nil, nil, whole, err
		}
		have, err := blob(tree + ":" + path)
		if err != nil {
			return nil, nil, whole, err
		}
		if before == have {
			continue
		}
		for k := j + 1; k < i; k++ {
			got, err := blob(all[k].sha + ":" + path)
			if err != nil {
				return nil, nil, whole, err
			}
			if got == before {
				whole = laterFile{k, path, before == ""}
				break
			}
		}
	}
	return gaps, read, whole, nil
}

// foldView is where the fold has the context of fixup i's hunks, behind its
// target j: cur is the tree the fixup is merged onto, stopsHere whether the
// replayed fold conflicts at it, and conflicted the files of that conflict.
type foldView struct {
	dir        string
	all        []lineCommit
	targetOf   map[int]int
	blobs      blobCache
	paths      pathCache
	j, i       int
	cur        string
	stopsHere  bool
	conflicted []string
	// after is made on first use, and shared by the fixup's files.
	after *afterReplay
}

// placed is the first commit after the target that has before in path as the
// fold orders the line; false when that cannot be told. inFold is whether a
// fixup is the first on the line to have it.
func (v *foldView) placed(path string, before []string, inFold bool) (int, bool, error) {
	// Past the stop, what the stop leaves of a file is unknown.
	if !v.stopsHere && slices.Contains(v.conflicted, path) {
		return 0, false, nil
	}
	// A fixup of a commit below the target changes what the commits after it
	// come to. At the stop cur has it, and it is in doubt only where the
	// context comes from another fixup.
	if !v.stopsHere || inFold {
		if below, err := v.foldedBelow(path); err != nil || below {
			return 0, false, err
		}
	}
	// The order within a group with a fold of a fold is git's own, so what cur
	// has of it is not what the fold has.
	if nestedUnder(v.targetOf, rootOf(v.targetOf, v.j)) {
		return 0, false, nil
	}
	if v.after == nil {
		v.after = newAfterReplay(v.dir, v.all, v.targetOf, v.cur, v.j, v.i)
	}
	return v.after.first(v.blobs, path, before)
}

// foldedBelow is whether a fixup of a commit below the target changes path.
func (v *foldView) foldedBelow(path string) (bool, error) {
	for f := range v.targetOf {
		if rootOf(v.targetOf, f) >= v.j {
			continue
		}
		paths, err := v.paths.of(v.all[f].sha)
		if err != nil || slices.Contains(paths, path) {
			return err == nil, err
		}
	}
	return false, nil
}

// afterReplay replays the commits between target j and fixup i as the fold
// takes them, each with its folds, from the target's tree: one commit at a
// time, as far as the files looked for need. A file that does not merge clean
// is left as it was and the replay goes on, but says nothing more of that
// file. A commit git drops as empty is passed over; the replay ends at one
// that has folds, which land elsewhere, and after a commit whose folds take
// an order of git's own.
type afterReplay struct {
	dir      string
	all      []lineCommit
	targetOf map[int]int
	// order is what is still to replay, a foldOrder.
	order []int
	cur   string
	done  []replayedGroup
	ended bool
}

// replayedGroup is a commit with its folds replayed: the tree they left, and
// for each file that conflicted among them the tree before it did, "" when
// the commit itself conflicts there.
type replayedGroup struct {
	m    int
	tree string
	held map[string]string
}

// newAfterReplay is an afterReplay with nothing replayed yet. A line with a
// move! commit takes an order wt does not follow, so it replays nothing.
func newAfterReplay(dir string, all []lineCommit, targetOf map[int]int, start string, j, i int) *afterReplay {
	r := &afterReplay{dir: dir, all: all, targetOf: targetOf, cur: start}
	r.ended = slices.ContainsFunc(all, func(c lineCommit) bool { return isMove(c.subject) })
	r.order = foldOrder(all, targetOf, j+1, i-1)
	return r
}

// first is the first commit whose replayed tree has before in path; false
// when the replay cannot tell of one.
func (r *afterReplay) first(blobs blobCache, path string, before []string) (int, bool, error) {
	for k := 0; ; k++ {
		if k == len(r.done) {
			if ok, err := r.next(); err != nil || !ok {
				return 0, false, err
			}
		}
		g := r.done[k]
		tree, held := g.held[path]
		if !held {
			tree = g.tree
		}
		if tree != "" {
			if has, err := blobs.has(tree, path, before); err != nil || has {
				return g.m, has, err
			}
		}
		if held {
			return 0, false, nil
		}
	}
}

// next replays one more commit and its folds; false when there is none to
// replay.
func (r *afterReplay) next() (bool, error) {
	for !r.ended && len(r.order) > 0 {
		m := r.order[0]
		g := replayedGroup{m: m, held: map[string]string{}}
		n := 1
		for n < len(r.order) && r.targetOf[r.order[n]] == m && isFold(r.all[r.order[n]].subject) {
			n++
		}
		group := r.order[:n]
		r.order = r.order[n:]
		if nestedUnder(r.targetOf, m) {
			group, r.ended = group[:1], true
		}
		dropped := false
		for _, c := range group {
			parent, err := gitEnv(r.dir, nil, nil, "rev-parse", r.all[c].sha+"^^{tree}")
			if err != nil {
				return false, err
			}
			tree, clean, conflicts, _, err := mergeTree(r.dir, parent, r.cur, r.all[c].sha)
			if err != nil {
				return false, err
			}
			if !clean {
				for _, f := range conflicts {
					if _, ok := g.held[f.Path]; !ok {
						g.held[f.Path] = ""
						if c != m {
							g.held[f.Path] = r.cur
						}
					}
				}
				if tree, err = cleanPart(r.dir, tree, r.cur, conflicts); err != nil {
					return false, err
				}
			} else if c == m && tree == r.cur {
				own, err := gitEnv(r.dir, nil, nil, "rev-parse", r.all[c].sha+"^{tree}")
				if err != nil {
					return false, err
				}
				if dropped = own != parent; dropped {
					break
				}
			}
			r.cur = tree
		}
		if dropped {
			r.ended = r.ended || n > 1
			continue
		}
		g.tree = r.cur
		r.done = append(r.done, g)
		return true, nil
	}
	return false, nil
}

// cleanPart is merged, a merge-tree result, with each conflicted file as
// cur has it: what of a fixup that conflicts folds in clean. It writes only
// objects, through a private index.
func cleanPart(dir, merged, cur string, conflicts []Conflict) (string, error) {
	idx, cleanup, err := scratchIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, nil, "read-tree", merged); err != nil {
		return "", err
	}
	var info strings.Builder
	zero := strings.Repeat("0", len(cur))
	for _, c := range conflicts {
		entry, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "ls-tree", "-z", "--full-tree", cur, "--", c.Path)
		if err != nil {
			return "", err
		}
		// Every stage the merged index holds for the path goes, then the
		// target's own entry, if it has one, comes back.
		fmt.Fprintf(&info, "0 %s\t%s\x00", zero, c.Path)
		if meta, path, ok := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t"); ok && path == c.Path {
			f := strings.Fields(meta) // mode type sha
			fmt.Fprintf(&info, "%s %s\t%s\x00", f[0], f[2], c.Path)
		}
	}
	if _, err := gitEnv(dir, env, strings.NewReader(info.String()), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	return gitEnv(dir, env, nil, "write-tree")
}

// lineCommit is a commit of the line and its subject.
type lineCommit struct{ sha, subject string }

// foldTarget is the commit of earlier a fold subject names, as git's
// autosquash finds it (foldMatches). ok is false for a commit that is no
// fold, and for a name that finds no commit or more than one: the run
// refuses those itself.
func foldTarget(dir string, earlier []lineCommit, subject string, index map[string]int) (int, bool) {
	name, ok := foldName(subject)
	if !ok {
		return 0, false
	}
	found := foldMatches(dir, earlier, name, index)
	if len(found) != 1 {
		return 0, false
	}
	return found[0], true
}

// foldName is what a fold subject names, after its prefixes and their
// whitespace; false for a subject that is no fold, or names nothing.
func foldName(subject string) (string, bool) {
	name, folding := subject, false
	for {
		rest, ok := cutFoldPrefix(name)
		if !ok {
			return name, folding && name != ""
		}
		name, folding = strings.TrimSpace(rest), true
	}
}

// foldMatches is the commits of earlier a fold's name may mean, looked for in
// autosquash's order: those with that subject; else the one the name, with no
// space in it, is a revision of; else those whose subject begins with it.
// index places each commit of the line.
func foldMatches(dir string, earlier []lineCommit, name string, index map[string]int) []int {
	var exact, prefix []int
	for j, c := range earlier {
		switch {
		case c.subject == name:
			exact = append(exact, j)
		case strings.HasPrefix(c.subject, name):
			prefix = append(prefix, j)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if !strings.Contains(name, " ") {
		if sha, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}"); err == nil {
			if j, ok := index[strings.TrimSpace(sha)]; ok && j < len(earlier) {
				return []int{j}
			}
		}
	}
	return prefix
}

// hunk is one hunk of a fixup's diff: the lines its change needs before it
// (context and removed lines, in order), and the lines of the fixup's own
// file it adds, or where it removes, for a person to find it.
type hunk struct {
	before     []string
	first, end int
}

func (h hunk) lines() string {
	if h.end <= h.first {
		return strconv.Itoa(h.first)
	}
	return strconv.Itoa(h.first) + "-" + strconv.Itoa(h.end)
}

// fixupHunks is the hunks of commit c's change to path against its parent.
func fixupHunks(dir, c, path string) ([]hunk, error) {
	out, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "--no-renames", "-U3",
		"--src-prefix=a/", "--dst-prefix=b/", c+"^", c, "--", path)
	if err != nil {
		return nil, err
	}
	return parseHunks(out), nil
}

// parseHunks reads the hunks of a one-file unified diff.
func parseHunks(diff string) []hunk {
	var hunks []hunk
	var h *hunk
	newLine := 0
	flush := func() {
		if h != nil {
			hunks = append(hunks, *h)
		}
		h = nil
	}
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "@@ ") {
			flush()
			newLine = hunkNewStart(l)
			h = &hunk{}
			continue
		}
		if h == nil || l == "" {
			continue
		}
		switch l[0] {
		case ' ':
			h.before = append(h.before, l[1:])
			newLine++
		case '-':
			h.before = append(h.before, l[1:])
			if h.first == 0 {
				h.first, h.end = newLine, newLine
			}
		case '+':
			if h.first == 0 {
				h.first = newLine
			}
			h.end = newLine
			newLine++
		}
	}
	flush()
	for i := range hunks {
		if hunks[i].first == 0 {
			hunks[i].first = 1
		}
	}
	return hunks
}

// hunkNewStart is the first line of the new side a hunk header names.
func hunkNewStart(header string) int {
	_, rest, _ := strings.Cut(header, "+")
	n, _, _ := strings.Cut(rest, ",")
	n, _, _ = strings.Cut(n, " ")
	v, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return v
}

// blobCache reads files of trees once each.
type blobCache struct {
	dir string
	got map[string]*string
}

// file is path in tree, nil when the tree has no such file.
func (b blobCache) file(tree, path string) (*string, error) {
	key := tree + ":" + path
	if s, ok := b.got[key]; ok {
		return s, nil
	}
	out, code, err := gitEnvAllow(b.dir, nil, nil, 128, "cat-file", "-p", key)
	if err != nil {
		return nil, err
	}
	var s *string
	if code == 0 {
		s = &out
	}
	b.got[key] = s
	return s, nil
}

// has is whether path in tree holds lines, one after the other.
func (b blobCache) has(tree, path string, lines []string) (bool, error) {
	f, err := b.file(tree, path)
	if err != nil || f == nil {
		return false, err
	}
	return containsRun(strings.Split(*f, "\n"), lines), nil
}

// containsRun is whether lines appear in file, in order and next to each
// other.
func containsRun(file, lines []string) bool { return indexRun(file, lines) >= 0 }

// HunkDrop is lines of a fixup's hunk that a resolution left out: the old
// tip has them, and no commit still to be replayed brings them.
type HunkDrop struct {
	Path  string
	Lines string // where the old tip has them, "13" or "13-15"
	N     int
	// Binary is a binary file whose change the resolution left out.
	Binary bool
}

// ResolutionDrops is what the resolution of a stop at fixup leaves out of
// it, read-only, and what of it could not be checked. Line by line, with
// their counts: each line the fixup adds to a file that the old tip still
// has there (following renames after the fixup) must be in the resolution,
// or added by a commit the sequencer still has to replay; a line short is
// dropped. A line with no letter or digit in it (a blank, a lone brace) is
// too common to prove anything and is not checked; when such lines fall
// short they are named as not checked. A binary file is compared by blob:
// the old tip has the fixup's blob, the index does not, and nothing to come
// writes it, is a drop. A file the old tip no longer has under a name that
// can be followed, a binary file changed again after the fixup, and a file
// still unmerged, are named as not checked, never passed. Only objects and
// the index are read; no ref, index entry or file moves.
func ResolutionDrops(wtPath, fixup, oldTip string) (drops []HunkDrop, unchecked []string, err error) {
	later, err := todoCommits(wtPath)
	if err != nil {
		return nil, nil, err
	}
	binary, err := binaryPaths(wtPath, fixup)
	if err != nil {
		return nil, nil, err
	}
	paths, err := pathsOfCommit(wtPath, fixup)
	if err != nil {
		return nil, nil, err
	}
	renamed, gone, err := followTo(wtPath, fixup, oldTip)
	if err != nil {
		return nil, nil, err
	}
	staged, err := stagedNames(wtPath)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range paths {
		d, why, err := pathDrop(wtPath, fixup, oldTip, p, later, binary[p], renamed, gone, staged)
		if err != nil {
			// A file that cannot be read is said, never a block.
			why = p + ": could not be checked: " + kitOneLine(err.Error())
		}
		if why != "" {
			unchecked = append(unchecked, why)
		}
		if d != nil {
			drops = append(drops, *d)
		}
	}
	return drops, unchecked, nil
}

// pathDrop is what the resolution drops of fixup's change to p, or why it
// cannot tell.
func pathDrop(wtPath, fixup, oldTip, p string, later []string, binary bool, renamed map[string]string, gone map[string]bool, staged func(string) string) (*HunkDrop, string, error) {
	if unmerged, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "ls-files", "--unmerged", "--", p); err != nil {
		return nil, "", err
	} else if unmerged != "" {
		return nil, p + ": still unmerged, so what the resolution keeps of it is not checked", nil
	}
	at := p
	if q, ok := renamed[p]; ok {
		at = q
	}
	if gone[p] {
		return nil, p + ": the old tip no longer has it under a name that can be followed, so what the resolution keeps of it is not checked", nil
	}
	names, err := namesAhead(wtPath, later, p)
	if err != nil {
		return nil, "", err
	}
	if binary {
		return binaryDrop(wtPath, fixup, oldTip, p, at, later, names, staged(p))
	}
	return textDrop(wtPath, fixup, oldTip, p, at, later, names, staged(p))
}

// stagedNames is where the index holds a path of the line: the path itself,
// or the name git carried it to when the rebase is onto a branch that
// renamed it, read by comparing the stop's target with HEAD, its rebased
// self; "" when it is in the index under neither.
func stagedNames(wtPath string) (func(string) string, error) {
	listed, err := gitEnv(wtPath, nil, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, rec := range strings.Split(listed, "\x00") {
		if _, path, ok := strings.Cut(rec, "\t"); ok {
			in[path] = true
		}
	}
	moved := map[string]string{}
	if target, err := stopTarget(wtPath); err == nil && target != "" {
		if out, err := gitEnv(wtPath, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", target, "HEAD"); err == nil {
			f := strings.Split(out, "\x00")
			for i := 0; i+2 < len(f); i++ {
				if strings.HasPrefix(f[i], "R") {
					moved[f[i+1]] = f[i+2]
					i += 2
				}
			}
		}
	}
	return func(p string) string {
		switch {
		case in[p]:
			return p
		case in[moved[p]]:
			return moved[p]
		}
		return ""
	}, nil
}

// stopTarget is the commit the stopped fixup folds into, as the
// sequencer's done list names it: the last pick before the fixups.
func stopTarget(wtPath string) (string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return "", err
	}
	done, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(done)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Fields(lines[i])
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "pick", "p", "edit", "e", "reword", "r":
			return gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", f[1]+"^{commit}")
		}
	}
	return "", nil
}

// textDrop is what the resolution drops of the lines fixup adds to p, at
// in the old tip, rp in the index; why names what it cannot tell. Each
// line counts with what the commits to come do to it: what the
// resolution has of it, plus what they add, less what they remove, must
// come to what the old tip has.
func textDrop(wtPath, fixup, oldTip, p, at string, later []string, names map[string][]string, rp string) (*HunkDrop, string, error) {
	runs, err := fixupHunkAdds(wtPath, fixup, p)
	if err != nil {
		return nil, "", err
	}
	added := map[string]bool{}
	for _, r := range runs {
		for _, l := range r {
			added[l] = true
		}
	}
	if len(added) == 0 {
		return nil, "", nil
	}
	if rp == "" {
		return nil, p + ": not in the resolution under a name that can be followed, so what it keeps of it is not checked", nil
	}
	blobs := blobCache{dir: wtPath, got: map[string]*string{}}
	old, err := blobs.file(oldTip+"^{tree}", at)
	if err != nil || old == nil {
		return nil, "", err
	}
	oldLines := strings.Split(*old, "\n")
	oldCount := countLines(oldLines)
	resolved, err := gitEnv(wtPath, nil, nil, "cat-file", "-p", ":0:"+rp)
	if err != nil {
		return nil, "", err
	}
	expected := countLines(strings.Split(resolved, "\n"))
	for _, c := range later {
		ns := names[c]
		if len(ns) == 0 {
			continue
		}
		adds, removes, err := commitChanges(wtPath, c, ns...)
		if err != nil {
			return nil, "", err
		}
		for _, l := range adds {
			expected[l]++
		}
		for _, l := range removes {
			expected[l]--
		}
	}
	short, common := map[string]int{}, 0
	for l := range added {
		missing := oldCount[l] - expected[l]
		if missing <= 0 {
			continue
		}
		if !strings.ContainsFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			common += missing
			continue
		}
		short[l] = missing
	}
	why := ""
	if common > 0 {
		why = fmt.Sprintf("%s: %s with no letter or digit (blank lines, braces) may be dropped; too common to tell, not checked", at, plural(common, "line"))
	}
	if len(short) == 0 {
		return nil, why, nil
	}
	var at1 []int
	n := 0
	for i := len(oldLines) - 1; i >= 0; i-- {
		// The last ones: a copy the target had already comes first.
		if l := oldLines[i]; short[l] > 0 {
			at1 = append([]int{i + 1}, at1...)
			short[l]--
			n++
		}
	}
	return &HunkDrop{Path: at, Lines: lineRanges(at1), N: n}, why, nil
}

// binaryDrop is whether the resolution drops fixup's change to the binary
// file p, at in the old tip, rp in the index. A commit to come brings it
// back only when its own change writes that blob.
func binaryDrop(wtPath, fixup, oldTip, p, at string, later []string, names map[string][]string, rp string) (*HunkDrop, string, error) {
	blob := func(rev string) (string, error) {
		// An absent path is exit 1 under --quiet: absence, not an error.
		out, code, err := gitEnvAllow(wtPath, nil, nil, 1, "rev-parse", "--verify", "--quiet", rev)
		if err != nil || code != 0 {
			return "", err
		}
		return out, nil
	}
	mine, err := blob(fixup + ":" + p)
	if err != nil {
		return nil, "", err
	}
	idx := ""
	if rp != "" {
		if idx, err = blob(":0:" + rp); err != nil {
			return nil, "", err
		}
	}
	if mine == "" {
		// The fixup deletes it: kept when the resolution has it no more.
		if idx == "" {
			return nil, "", nil
		}
		return nil, p + ": a binary file the fixup deletes and the resolution keeps: not checked", nil
	}
	old, err := blob(oldTip + ":" + at)
	if err != nil {
		return nil, "", err
	}
	if old != mine {
		return nil, at + ": a binary file the line changes again after the fixup, or removes: not checked", nil
	}
	if idx == mine {
		return nil, "", nil
	}
	for _, c := range later {
		if len(names[c]) == 0 {
			continue
		}
		out, err := gitEnv(wtPath, nil, nil, append([]string{"--literal-pathspecs", "diff-tree", "-r", "-z", "--no-renames", c + "^", c, "--"}, names[c]...)...)
		if err != nil {
			return nil, "", err
		}
		for _, rec := range strings.Split(out, "\x00") {
			// ":<mode> <mode> <old> <new> <status>"
			if f := strings.Fields(strings.TrimPrefix(rec, ":")); len(f) >= 4 && f[3] == mine {
				return nil, "", nil
			}
		}
	}
	return &HunkDrop{Path: at, Binary: true}, "", nil
}

// binaryPaths is the files commit c changes that git diffs as binary.
func binaryPaths(dir, c string) (map[string]bool, error) {
	out, err := gitEnv(dir, nil, nil, "diff-tree", "--numstat", "-z", "--no-renames", "-r", c+"^", c)
	if err != nil {
		return nil, err
	}
	bin := map[string]bool{}
	for _, rec := range strings.Split(out, "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) == 3 && f[0] == "-" && f[1] == "-" {
			bin[f[2]] = true
		}
	}
	return bin, nil
}

// followTo is where each path fixup has is in the old tip: renamed is the
// files git sees renamed or copied on the way, gone those the old tip has
// no more under any name git can follow.
func followTo(dir, fixup, oldTip string) (renamed map[string]string, gone map[string]bool, err error) {
	renamed, gone = map[string]string{}, map[string]bool{}
	out, err := gitEnv(dir, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", fixup, oldTip)
	if err != nil {
		return nil, nil, err
	}
	f := strings.Split(out, "\x00")
	for i := 0; i < len(f); i++ {
		status := f[i]
		switch {
		case status == "":
		case (status[0] == 'R' || status[0] == 'C') && i+2 < len(f):
			if status[0] == 'R' {
				renamed[f[i+1]] = f[i+2]
			}
			i += 2
		case status[0] == 'D' && i+1 < len(f):
			gone[f[i+1]] = true
			i++
		default:
			i++
		}
	}
	return renamed, gone, nil
}

// namesAhead is, for each commit still to be replayed, the names p goes by
// in that commit's change, following the renames git sees along the way:
// the name before it and, when it renames p, the name after.
func namesAhead(dir string, later []string, p string) (map[string][]string, error) {
	names := map[string][]string{}
	name := p
	for _, c := range later {
		out, err := gitEnv(dir, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", c+"^", c)
		if err != nil {
			return nil, err
		}
		names[c] = []string{name}
		f := strings.Split(out, "\x00")
		for i := 0; i+2 < len(f); i++ {
			if strings.HasPrefix(f[i], "R") && f[i+1] == name {
				name = f[i+2]
				names[c] = append(names[c], name)
				break
			}
		}
	}
	return names, nil
}

// commitChanges is every line commit c's change to paths adds and every
// line it removes, renames followed, so a moved file's lines count as
// neither.
func commitChanges(dir, c string, paths ...string) (adds, removes []string, err error) {
	out, err := gitEnv(dir, nil, nil, append([]string{"--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "-M", "-U0",
		"--src-prefix=a/", "--dst-prefix=b/", c + "^", c, "--"}, paths...)...)
	if err != nil {
		return nil, nil, err
	}
	body := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			body = false
		case strings.HasPrefix(l, "@@ "):
			body = true
		case body && strings.HasPrefix(l, "+"):
			adds = append(adds, l[1:])
		case body && strings.HasPrefix(l, "-"):
			removes = append(removes, l[1:])
		}
	}
	return adds, removes, nil
}

func countLines(lines []string) map[string]int {
	n := map[string]int{}
	for _, l := range lines {
		n[l]++
	}
	return n
}

// lineRanges is sorted line numbers as "3", "3-5" or "3-5,9".
func lineRanges(at []int) string {
	var parts []string
	for i := 0; i < len(at); {
		j := i
		for j+1 < len(at) && at[j+1] == at[j]+1 {
			j++
		}
		parts = append(parts, hunk{first: at[i], end: at[j]}.lines())
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// DropsLine is what a person is told about drops of fixup.
func DropsLine(drops []HunkDrop, fixup, subject string) string {
	n, bins := 0, 0
	var where []string
	for _, d := range drops {
		if d.Binary {
			bins++
			where = append(where, d.Path+" (binary)")
			continue
		}
		n += d.N
		where = append(where, d.Path+":"+d.Lines)
	}
	var what []string
	if n > 0 {
		what = append(what, plural(n, "line"))
	}
	if bins > 0 {
		what = append(what, "the change to "+plural(bins, "binary file"))
	}
	return fmt.Sprintf("the resolution drops %s of %s %q that the old tip has: %s", strings.Join(what, " and "), git.ShortID(fixup, 7), oneLinePlan(subject), strings.Join(where, ", "))
}

// fixupHunkAdds is each run of lines commit c's change to path adds,
// in order.
func fixupHunkAdds(dir, c, path string) ([][]string, error) {
	out, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "--no-renames", "-U0",
		"--src-prefix=a/", "--dst-prefix=b/", "--root", c, "--", path)
	if err != nil {
		return nil, err
	}
	var runs [][]string
	var run []string
	end := func() {
		if len(run) > 0 {
			runs = append(runs, run)
		}
		run = nil
	}
	body := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "@@ "):
			end()
			body = true
		case !body:
		case strings.HasPrefix(l, "+"):
			run = append(run, l[1:])
		default:
			end()
		}
	}
	end()
	return runs, nil
}

// todoCommits is the commits the sequencer in wtPath still has to replay.
func todoCommits(wtPath string) ([]string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return nil, err
	}
	todo, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(todo), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch f[0] {
		case "pick", "p", "edit", "e", "reword", "r", "squash", "s", "fixup", "f":
			id := f[1]
			if strings.HasPrefix(id, "-") && len(f) > 2 { // fixup -C <id>
				id = f[2]
			}
			sha, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", id+"^{commit}")
			if err == nil && sha != "" {
				out = append(out, sha)
			}
		}
	}
	return out, nil
}

// indexRun is where lines start in file, -1 when they are not in it.
func indexRun(file, lines []string) int {
	for i := 0; i+len(lines) <= len(file); i++ {
		match := true
		for k, l := range lines {
			if file[i+k] != l {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// addVerb is "adds" for one commit, "add" for more.
func addVerb(n int) string {
	if n == 1 {
		return "adds"
	}
	return "add"
}
