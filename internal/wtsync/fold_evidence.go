package wtsync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// foldWatch is what a run saw of the folds its rebase carried out, taken from
// the sequencer's own lists as the rebase moves: the execution evidence a
// report of a folded fixup rests on, kept through stops, handovers and
// resumes in ReplayPlan.Folded.
//
// One rule confirms a fold of fixup F into target T: git's list shows T's own
// pick (by its original id) immediately followed by an unbroken run of fixup
// and squash commands that contains F, no other pick between, and wt drove
// the rebase through that stretch. The list cannot show a pick a person
// skipped, so wt records how far the list had gone where it handed over
// (ReplayPlan.Seen and SeenLast, every line of the list): a list that differs
// was moved by hand, and nothing in it so far is confirmed, now or on any later
// look (ReplayPlan.Floor). Break and exec
// commands wt puts in the list are not commands of that run. Nothing else
// confirms a fold: not the commit HEAD is at, not the fixup's change being
// in the result.
type foldWatch struct {
	executed []string
	// inFlight is what the next finished rebase will have carried out: the
	// fold a conflict is stopped at, and the folds after it, each with its
	// target's pick right before its run in the list.
	inFlight []string
	// targets is each fold's planned target.
	targets map[string]string
	// seen and seenLast are how many lines the done list held at the last
	// look, break and exec lines included, and its last line.
	seen     int
	seenLast string
	// floor is how many entries of the filtered done list can never be new
	// evidence: wt saw them and could not confirm them, or a person moved
	// the rebase through them by hand.
	floor int
}

// seqCommand is one command of a sequencer list that names a commit.
type seqCommand struct {
	name, sha string
}

func (c seqCommand) isFold() bool {
	return c.name == "fixup" || c.name == "f" || c.name == "squash" || c.name == "s"
}

func (c seqCommand) isPick() bool {
	switch c.name {
	case "pick", "p", "edit", "e", "reword", "r":
		return true
	}
	return false
}

// readSequencerList is the commands of a sequencer list that name a commit,
// as full ids, in order; other commands (break, exec) are left out.
func readSequencerList(wtPath, file string) ([]seqCommand, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, err
	}
	var out []seqCommand
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		c := seqCommand{name: f[0]}
		if !c.isFold() && !c.isPick() {
			continue
		}
		id := f[1:]
		if len(id) > 1 && strings.HasPrefix(id[0], "-") { // fixup -C <id>
			id = id[1:]
		}
		sha, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", id[0]+"^{commit}")
		if err != nil || sha == "" {
			continue
		}
		c.sha = sha
		out = append(out, c)
	}
	return out, nil
}

// rawDone is how many lines the done list holds, break and exec lines
// included, and the last of them. A list that cannot be read has none.
func rawDone(wtPath string) (int, string) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return 0, ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return 0, ""
	}
	n, last := 0, ""
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			n, last = n+1, l
		}
	}
	return n, last
}

// conflictedPick is whether the sequencer is stopped at a pick with a
// conflict: its command is not finished.
func conflictedPick(wtPath string) bool {
	dirs, err := sequencerDirs(wtPath, "MERGE_MSG")
	if err != nil {
		return false
	}
	_, err = os.Stat(dirs[0])
	return err == nil
}

// confirmedAt is whether the fold at position i of list is the run of folds
// right behind its target's pick, and is not in what a person moved by hand.
func (w *foldWatch) confirmedAt(list []seqCommand, i int) bool {
	tainted := func(pos int) bool { return pos < w.floor }
	t := w.targets[list[i].sha]
	if t == "" || tainted(i) {
		return false
	}
	p := i - 1
	for p >= 0 && list[p].isFold() {
		p--
	}
	return p >= 0 && list[p].sha == t && !tainted(p)
}

// look reads the sequencer's lists and counts the folds it has run, and
// notes the ones it will have run when the rebase wt drives ends. A list that
// cannot be read counts nothing.
func (w *foldWatch) look(wtPath string) {
	done, err := readSequencerList(wtPath, "done")
	if err != nil {
		return
	}
	todo, err := readSequencerList(wtPath, "git-rebase-todo")
	if err != nil {
		return
	}
	w.seen, w.seenLast = rawDone(wtPath)
	list := slices.Concat(done, todo)
	stopped := -1
	if n := len(done); n > 0 && done[n-1].isFold() && conflictedPick(wtPath) {
		stopped = n - 1
	}
	w.inFlight = nil
	for i, c := range list {
		if !c.isFold() || !w.confirmedAt(list, i) {
			continue
		}
		switch {
		case i < len(done) && i != stopped:
			if !slices.Contains(w.executed, c.sha) {
				w.executed = append(w.executed, c.sha)
			}
		default:
			w.inFlight = append(w.inFlight, c.sha)
		}
	}
}

// settle counts what the rebase carried out to its end.
func (w *foldWatch) settle() {
	for _, id := range w.inFlight {
		if !slices.Contains(w.executed, id) {
			w.executed = append(w.executed, id)
		}
	}
	w.inFlight = nil
}

// startAt makes a watch of a rebase wt takes up where an earlier look left
// it: plan carries what wt saw before. A done list longer than the plan says
// it was when wt handed over was moved by hand from the stop it was left at.
func startAt(wtPath string, plan *ReplayPlan) *foldWatch {
	w := newFoldWatch(plan)
	w.executed = slices.Clone(plan.Folded)
	w.floor = plan.Floor
	done, err := readSequencerList(wtPath, "done")
	if err != nil {
		return w
	}
	if n, last := rawDone(wtPath); n != plan.Seen || last != plan.SeenLast {
		// The rebase is not where wt left it: a person moved it, and
		// what they skipped or continued the list cannot show.
		w.floor = max(w.floor, len(done))
	}
	w.look(wtPath)
	return w
}

func newFoldWatch(plan *ReplayPlan) *foldWatch {
	w := &foldWatch{}
	if plan != nil {
		w.targets = plan.Targets
	}
	return w
}

// record puts what the watch saw into plan, which a stop saves for the resume.
func (w *foldWatch) record(plan *ReplayPlan) {
	plan.Folded, plan.Seen, plan.SeenLast, plan.Floor = slices.Clone(w.executed), w.seen, w.seenLast, w.floor
}
