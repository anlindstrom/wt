package wtsync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FoldStop is a commit a rewrite stops at to check, named as it is on the
// line now.
type FoldStop struct {
	Commit
	// Untouched is the layer the run leaves unchanged, where it runs no check
	// at this commit; empty for a stop it makes.
	Untouched string
}

// rewriteGroup is a pick of the list with what folds into it.
type rewriteGroup struct {
	at      int // the pick's place on the line
	members []string
}

// rewrite is a rebase that rewrites a line, read as the driver will drive it:
// its groups, and which layers it leaves unchanged.
type rewrite struct {
	cfg     *Config
	req     Request
	commits []string // the line, oldest first
	groups  []rewriteGroup
	// subjects and paths are each commit's and each move part's.
	subjects map[string]string
	paths    map[string][]string
	// called is the each_commit steps each commit to check calls for.
	called  map[string][]int
	sources []string
	// kept is how many commits from the bottom the rebase leaves as they are.
	kept     int
	owed     map[string]bool
	owedLost bool
	// onBase says the rebase goes onto the line's own base.
	onBase bool
	// unsure says a listed stop may turn out to run no check.
	unsure bool
}

// inPlace is whether the rebase replays the line onto its own base.
func (w *rewrite) inPlace() bool {
	return w.req.Stack != nil && w.req.Upstream == "" && w.onBase
}

// Folds is the fold commits of a line as a caller already resolved them.
type Folds struct {
	Pairs []FoldPair
	Moves []PartMove
}

// RewriteDue is what a fold, an edit or wt sync steps may run: the commits it
// stops to check, and the steps with them, req being the rebase the run makes.
// It need not be exact, but misses nothing the driver may run: a check at each
// commit once its fixups are folded in, or where --each-commit puts it; none at
// a commit the rebase surely leaves as it was; each per-layer step where its
// paths changed, it is owed or it is forced; and every check where such a step
// or an edit may change what a commit holds. Due.AtMost says the run may check
// fewer commits than listed. A fold the run refuses has nothing due. known,
// when not nil, is the line's folds, not resolved again.
func RewriteDue(mainRoot string, cfg *Config, req Request, known *Folds) (Due, []FoldStop, error) {
	var due Due
	if cfg == nil || (len(cfg.EachCommit) == 0 && len(cfg.Defer) == 0) {
		return due, nil, nil
	}
	w, why, err := readRewrite(mainRoot, cfg, req, known)
	if err != nil || why != "" {
		return due, nil, err
	}
	stops, steps, err := w.checkStops()
	if err != nil {
		return due, nil, err
	}
	if due.Deferred, err = w.deferredDue(); err != nil {
		return due, nil, err
	}
	// A per-layer step that commits or folds, and an edit, change what a
	// later check reads: any check may then run.
	perLayer := slices.ContainsFunc(due.Deferred, Deferred.PerLayer)
	every := req.EachCommit != EachOff && (perLayer || req.Edit != nil)
	for j, s := range cfg.EachCommit {
		if every || steps[j] {
			due.Checks = append(due.Checks, s)
		}
	}
	for _, s := range stops {
		if s.Untouched == "" {
			due.CheckCommits = append(due.CheckCommits, s.Commit)
		}
	}
	due.AtMost = len(due.CheckCommits) > 0 && (w.unsure || perLayer || !w.inPlace() || len(w.owed) > 0 || w.owedLost)
	return due, stops, nil
}

func readRewrite(mainRoot string, cfg *Config, req Request, known *Folds) (*rewrite, string, error) {
	listed, err := req.target().commits(req.Path, req.Branch, "--right-only", "--no-merges")
	if err != nil {
		return nil, "", err
	}
	slices.Reverse(listed)
	w := &rewrite{cfg: cfg, req: req, subjects: map[string]string{}, paths: map[string][]string{}, called: map[string][]int{}, owed: map[string]bool{}}
	lift := cfg.liftPatterns()
	replayed := map[string]bool{}
	for _, c := range listed {
		w.subjects[c.SHA], w.paths[c.SHA] = c.Subject, c.Paths
		if c.OnTrunk && !touchesAny(lift, c.Paths) {
			continue
		}
		replayed[c.SHA] = true
		w.commits = append(w.commits, c.SHA)
	}
	if req.Stack != nil {
		w.commits = req.Stack.Line.Commits
	}

	folds := Folds{}
	if req.Stack != nil && req.Stack.Autosquash {
		if known == nil {
			var why string
			if folds.Moves, why, err = PlanMovesOnto(req.Path, cfg, w.commits, req.Onto, req.Upstream); err != nil || why != "" {
				return nil, why, err
			}
			if folds.Pairs, err = FoldPairs(req.Path, w.commits); err != nil {
				return nil, "", err
			}
		} else {
			folds = *known
		}
	}
	into, moves := map[string]string{}, folds.Moves
	for _, p := range folds.Pairs {
		into[p.Fixup] = p.Target
	}
	moved := map[string]bool{}
	for _, m := range moves {
		moved[m.Commit] = true
	}
	groupOf := map[string]int{}
	for at, c := range w.commits {
		// A pick git drops is no stop; its place on the line still counts.
		if into[c] == "" && !moved[c] && replayed[c] {
			groupOf[c] = len(w.groups)
			w.groups = append(w.groups, rewriteGroup{at: at, members: []string{c}})
		}
	}
	join := func(target, member string) {
		for into[target] != "" {
			target = into[target]
		}
		if i, ok := groupOf[target]; ok {
			w.groups[i].members = append(w.groups[i].members, member)
		}
	}
	for _, c := range w.commits {
		if into[c] != "" {
			join(into[c], c)
		}
	}
	for _, c := range w.commits {
		if replayed[c] {
			if called := cfg.calledBy(w.paths[c]); len(called) > 0 {
				w.called[c] = called
			}
		}
	}
	for _, m := range moves {
		join(m.Target, m.Part)
		join(m.Target, m.Commit)
		w.paths[m.Part] = m.touched()
		if called := cfg.calledBy(m.touched()); len(called) > 0 {
			w.called[m.Part] = called
			w.sources = append(w.sources, m.Source)
		}
	}
	if e := req.Edit; e != nil {
		out, err := gitEnv(req.Path, nil, nil, "rev-list", e.Commit+"^.."+req.Branch)
		if err != nil {
			return nil, "", err
		}
		from := strings.Fields(out)
		for c := range w.called {
			if !slices.Contains(from, c) {
				delete(w.called, c)
			}
		}
	}
	o, err := ReadOwed(mainRoot, req.Branch)
	switch {
	case err != nil:
		w.owedLost = true
	case o != nil:
		for _, s := range o.Steps {
			w.owed[s.Step+"\x00"+s.Layer] = true
		}
	}
	if err := w.readKept(); err != nil {
		return nil, "", err
	}
	return w, "", nil
}

// readKept counts the commits from the bottom the rebase surely leaves as they
// are: onto the line's own base, picked in order with nothing folded in, none a
// lift edits, and no layer below owing a step, which may commit there.
func (w *rewrite) readKept() error {
	s := w.req.Stack
	if s == nil || s.Line.Base == "" {
		return nil
	}
	onto, err := gitEnv(w.req.Path, nil, nil, "rev-parse", "--verify", w.req.Onto+"^{commit}")
	if err != nil {
		return err
	}
	w.onBase = onto == s.Line.Base
	if !w.onBase || w.req.ForceSteps || w.req.Edit != nil {
		return nil
	}
	lift := w.cfg.liftPatterns()
	for _, g := range w.groups {
		owedBelow := slices.ContainsFunc(s.Line.Layers, func(l LineLayer) bool { return l.Count <= g.at && w.owes(l.Branch) })
		if g.at != w.kept || len(g.members) > 1 || touchesAny(lift, w.paths[w.commits[g.at]]) || owedBelow {
			break
		}
		w.kept++
	}
	return nil
}

// owes is whether a per-layer step is owed at the layer of branch.
func (w *rewrite) owes(branch string) bool {
	return slices.ContainsFunc(w.layerSteps(), func(d Deferred) bool { return w.owedAt(d.Run, branch) })
}

// layerSteps is the per-layer steps the rebase runs.
func (w *rewrite) layerSteps() []Deferred {
	return slices.DeleteFunc(w.cfg.LayerSteps(), func(d Deferred) bool { return !w.selected(d) })
}

func (w *rewrite) selected(d Deferred) bool { return w.req.Steps == nil || w.req.Steps[d.Run] }

func (w *rewrite) owedAt(step, branch string) bool {
	return w.owedLost || w.owed[step+"\x00"+branch]
}

// layer is the branch of the layer the commit at place at is in, and how many
// commits of the line end with it.
func (w *rewrite) layer(at int) (branch string, end int) {
	if s := w.req.Stack; s != nil {
		for _, l := range s.Line.Layers {
			if at < l.Count {
				return l.Branch, l.Count
			}
		}
	}
	return w.req.Branch, len(w.commits)
}

// untouched is whether the run leaves the layer of branch, ending at end, as
// it was and runs nothing there.
func (w *rewrite) untouched(branch string, end int) bool {
	if w.kept == 0 || end > w.kept {
		return false
	}
	return !w.owes(branch)
}

// checkStops is every commit the run stops to check, bottom first, and which
// each_commit steps run at one it makes.
func (w *rewrite) checkStops() ([]FoldStop, map[int]bool, error) {
	steps := map[int]bool{}
	mode := w.req.EachCommit
	if mode == EachOff || len(w.cfg.EachCommit) == 0 {
		return nil, steps, nil
	}
	var stops []FoldStop
	var layerCalls []int
	for i, g := range w.groups {
		pick := w.commits[g.at]
		branch, end := w.layer(g.at)
		var calls []int
		for _, m := range g.members {
			calls = append(calls, w.called[m]...)
		}
		layerCalls = append(layerCalls, calls...)
		layerEnds := i == len(w.groups)-1 || w.groups[i+1].at >= end
		var run []int
		tipStop := false
		switch {
		case atTips(mode) && layerEnds && len(layerCalls) > 0:
			run, tipStop = layerCalls, true
		case mode == EachTips:
		default:
			source := mode == EachTargets && slices.Contains(w.sources, pick)
			if len(calls) == 0 && !source {
				break
			}
			if mode == EachTargets && len(g.members) == 1 && !source {
				break
			}
			changed, err := w.netPaths(g)
			if err != nil {
				return nil, nil, err
			}
			for j, s := range w.cfg.EachCommit {
				if len(s.Paths) == 0 || touchesAny(s.Paths, changed) || source && slices.Contains(w.called[pick], j) {
					run = append(run, j)
				}
			}
		}
		if layerEnds {
			layerCalls = nil
		}
		if len(run) == 0 {
			continue
		}
		stop := FoldStop{Commit: Commit{SHA: pick, Subject: w.subjects[pick]}}
		// A pick git fast-forwards is one only a layer's end stop checks, and
		// then only where a step is owed.
		if g.at < w.kept && !w.req.ForceSteps && (!tipStop || w.untouched(branch, end)) {
			stop.Untouched = branch
		} else {
			for _, j := range run {
				steps[j] = true
			}
		}
		if stop.Untouched == "" && w.scriptAbsent(pick, run) {
			w.unsure = true
		}
		stops = append(stops, stop)
	}
	return stops, steps, nil
}

// netPaths is what g's commit changes once its folds are in it: a path a fold
// puts back is no part of it. Where a fold does not apply on its target alone,
// every path of the group counts.
func (w *rewrite) netPaths(g rewriteGroup) ([]string, error) {
	pick := w.commits[g.at]
	if len(g.members) == 1 {
		return w.paths[pick], nil
	}
	tree := pick + "^{tree}"
	for _, m := range g.members[1:] {
		next, clean, _, _, err := mergeTree(w.req.Path, m+"^", tree, m)
		if err != nil {
			return nil, err
		}
		if !clean {
			w.unsure = true
			var all []string
			for _, c := range g.members {
				all = append(all, w.paths[c]...)
			}
			return all, nil
		}
		tree = next
	}
	return ChangedPaths(w.req.Path, pick+"^", tree)
}

// scriptAbsent is whether a step of run names a repository script that the
// commit at sha does not hold, nor the worktree beside git: the driver skips it
// where it is still missing.
func (w *rewrite) scriptAbsent(sha string, run []int) bool {
	return slices.ContainsFunc(run, func(j int) bool {
		script := repoScript(w.cfg.EachCommit[j].Run)
		if script == "" || gitHas(w.req.Path, sha, script) {
			return false
		}
		_, err := os.Stat(filepath.Join(w.req.Path, script))
		return err != nil
	})
}

// deferredDue is the deferred steps that may run: a per-layer step at a layer
// the run changes, where its paths change, it is owed or it is forced; a step
// after the rebase where its paths may change.
func (w *rewrite) deferredDue() ([]Deferred, error) {
	if len(w.cfg.Defer) == 0 {
		return nil, nil
	}
	changed, err := mayChange(w.req.Path, w.req.Branch, w.req.target())
	if err != nil {
		return nil, err
	}
	// The layers, bottom first, those with no pick of their own too, each
	// with the paths its commits change.
	type segment struct {
		branch string
		end    int
		paths  []string
	}
	var segments []segment
	if s := w.req.Stack; s != nil {
		for _, l := range s.Line.Layers {
			segments = append(segments, segment{branch: l.Branch, end: l.Count})
		}
	}
	if n := len(segments); n == 0 || len(w.commits) > segments[n-1].end {
		segments = append(segments, segment{branch: w.req.Branch, end: len(w.commits)})
	}
	for _, g := range w.groups {
		i := slices.IndexFunc(segments, func(s segment) bool { return g.at < s.end })
		if i < 0 {
			continue
		}
		for _, m := range g.members {
			segments[i].paths = append(segments[i].paths, w.paths[m]...)
		}
	}
	var out []Deferred
	for _, d := range w.cfg.Defer {
		if !w.selected(d) {
			continue
		}
		matches := func(paths []string) bool {
			return w.req.ForceSteps || len(d.Paths) == 0 || anyMatches(d.Paths, paths)
		}
		if !d.PerLayer() {
			if matches(changed) {
				out = append(out, d)
			}
			continue
		}
		for n, s := range segments {
			if d.Each != EachLayer && n != len(segments)-1 || w.untouched(s.branch, s.end) {
				continue
			}
			paths := s.paths
			if !w.inPlace() {
				paths = changed
			}
			if matches(paths) || w.owedAt(d.Run, s.branch) {
				out = append(out, d)
				break
			}
		}
	}
	return out, nil
}

// WithOwed is d with every per-layer step the last run of branch left owed,
// which runs whatever its paths.
func (d Due) WithOwed(mainRoot string, cfg *Config, branch string) Due {
	o, err := ReadOwed(mainRoot, branch)
	if err != nil || o == nil {
		return d
	}
	set := o.StepSet()
	for _, s := range cfg.LayerSteps() {
		if set[s.Run] && !slices.ContainsFunc(d.Deferred, func(have Deferred) bool { return have.Run == s.Run }) {
			d.Deferred = append(d.Deferred, s)
		}
	}
	return d
}

// DueAtStop narrows due's checks to those of the commits the stopped rebase in
// wtPath still has to replay, with the group it stopped in: for a run that
// checks at each commit and has no per-layer step to rewrite what it passed.
// Where the sequencer's list cannot be read, due stays as it is.
func DueAtStop(wtPath string, cfg *Config, due Due) Due {
	dir, err := rebaseDir(wtPath)
	if err != nil || len(cfg.LayerSteps()) > 0 {
		return due
	}
	done, derr := os.ReadFile(filepath.Join(dir, "done"))
	todo, terr := os.ReadFile(filepath.Join(dir, "git-rebase-todo"))
	if derr != nil || terr != nil {
		return due
	}
	var ids []string
	// take adds the commit a line of the list names, and says it is a pick.
	take := func(line string) (pick bool) {
		f := strings.Fields(line)
		if len(f) < 2 {
			return false
		}
		switch f[0] {
		case "pick", "edit", "fixup", "squash":
		default:
			return false
		}
		id := f[1]
		if len(f) > 2 && strings.HasPrefix(id, "-") {
			id = f[2]
		}
		ids = append(ids, id)
		return f[0] == "pick" || f[0] == "edit"
	}
	passed := strings.Split(string(done), "\n")
	// Back to the pick the stop's group began at.
	for i := len(passed) - 1; i >= 0; i-- {
		if take(passed[i]) {
			break
		}
	}
	for _, line := range strings.Split(string(todo), "\n") {
		take(line)
	}
	var paths [][]string
	for _, id := range ids {
		p, err := pathsOfCommit(wtPath, id)
		if err != nil {
			return due
		}
		paths = append(paths, p)
	}
	due.Checks = slices.DeleteFunc(slices.Clone(due.Checks), func(s EachCommit) bool {
		return !slices.ContainsFunc(paths, func(p []string) bool { return len(s.Paths) == 0 || touchesAny(s.Paths, p) })
	})
	return due
}
