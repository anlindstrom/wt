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
// A fold counts when it ran into the commit it was planned for: the done
// list holds it right behind that commit's pick, and the commit was not
// skipped. A fold the rebase is stopped at, or has still to run, counts when
// the commit HEAD is at is its target, so a target a person skipped is seen.
// What a person did by hand between wt's looks is not seen and not counted.
type foldWatch struct {
	// skip is the leading fold commands of the done list that wt did not
	// see run: they are in the plan or are not counted.
	skip     int
	executed []string
	// inFlight is what the next finished rebase will have carried out: the
	// fold a conflict is stopped at, and the folds after it, each with its
	// target in place.
	inFlight []string
	// targets is each fold's planned target, and subjects the subject an
	// amend! gave a target, by the target's id.
	targets  map[string]string
	subjects map[string]string
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

// atTarget is whether HEAD is the replay of commit target, by author, author
// date and subject: the subject target has, or one an amend! of it gave.
func (w *foldWatch) atTarget(wtPath, target string) bool {
	const format = "--format=%an%x00%ae%x00%ad%x00%s"
	key := func(rev string) (string, string) {
		out, err := gitEnv(wtPath, nil, nil, "log", "-1", "--date=raw", format, rev)
		if err != nil {
			return "", ""
		}
		f := strings.SplitN(out, "\x00", 4)
		if len(f) < 4 {
			return "", ""
		}
		return strings.Join(f[:3], "\x00"), f[3]
	}
	want, subject := key(target)
	got, head := key("HEAD")
	if want == "" || want != got {
		return false
	}
	if head == subject {
		return true
	}
	return w.subjects[target] == head
}

// look reads the sequencer's lists and counts the folds it has run since
// wt's last look. A list that cannot be read counts nothing.
func (w *foldWatch) look(wtPath string) {
	done, err := readSequencerList(wtPath, "done")
	if err != nil {
		return
	}
	todo, err := readSequencerList(wtPath, "git-rebase-todo")
	if err != nil {
		return
	}
	w.inFlight = nil
	var stopped *seqCommand
	if n := len(done); n > 0 && done[n-1].isFold() && conflictedPick(wtPath) {
		stopped = &done[n-1]
		done = done[:n-1]
	}
	// A fold run since the last look counts when the pick right before it,
	// folds and nothing else between, is the commit it was planned for.
	pick, nth := "", 0
	for _, c := range done {
		if c.isPick() {
			pick = c.sha
			continue
		}
		if nth++; nth > w.skip && w.targets[c.sha] == pick && !slices.Contains(w.executed, c.sha) {
			w.executed = append(w.executed, c.sha)
		}
	}
	// The folds still to run count when their target is picked first, or is
	// the commit HEAD is at.
	picks := map[string]bool{}
	for _, c := range todo {
		if c.isPick() {
			picks[c.sha] = true
		}
	}
	if n := len(done); stopped == nil && n > 0 && done[n-1].isPick() && conflictedPick(wtPath) {
		// A pick stopped at its conflict is committed by the --continue.
		picks[done[n-1].sha] = true
	}
	later := todo
	if stopped != nil {
		later = append([]seqCommand{*stopped}, todo...)
	}
	for _, c := range later {
		if !c.isFold() {
			continue
		}
		t := w.targets[c.sha]
		if t != "" && (picks[t] || (t == pick && w.atTarget(wtPath, t))) {
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
// it: seen is what the plan recorded, and the folds the sequencer holds in
// done now that are not the command it is stopped at were run, or not, out
// of wt's sight.
func startAt(wtPath string, seen []string, plan *ReplayPlan) *foldWatch {
	w := newFoldWatch(plan)
	w.executed = slices.Clone(seen)
	done, err := readSequencerList(wtPath, "done")
	if err != nil {
		return w
	}
	for _, c := range done {
		if c.isFold() {
			w.skip++
		}
	}
	if n := len(done); n > 0 && done[n-1].isFold() && conflictedPick(wtPath) {
		w.skip--
	}
	// What the --continue wt makes next carries out.
	w.look(wtPath)
	return w
}

func newFoldWatch(plan *ReplayPlan) *foldWatch {
	w := &foldWatch{}
	if plan != nil {
		w.targets, w.subjects = plan.Targets, plan.Subjects
	}
	return w
}
