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
// A fold counts once the sequencer's done list holds it and it is not the
// command a conflict is stopped at; the command stopped at, and the folds
// still to come, count when the rebase wt drives goes on to its end. What a
// person did by hand between wt's looks (continue, skip) is not seen, so it
// is not counted.
type foldWatch struct {
	// skip is the leading fold commands of the done list that wt did not
	// see run: they are in the plan or are not counted.
	skip     int
	executed []string
	// inFlight and todo are what the next finished rebase will have carried
	// out: the fold a conflict is stopped at, and the folds after it.
	inFlight []string
}

// foldCommands is the commits the fold commands (fixup, squash) of a
// sequencer list name, as full ids, in order; last is whether the list's
// last command is one of them.
func foldCommands(wtPath, file string) (ids []string, last bool, err error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, false, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		last = false
		switch f[0] {
		case "fixup", "f", "squash", "s":
			id := f[1:]
			if len(id) > 1 && strings.HasPrefix(id[0], "-") { // fixup -C <id>
				id = id[1:]
			}
			if len(id) == 0 {
				continue
			}
			sha, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", id[0]+"^{commit}")
			if err != nil || sha == "" {
				continue
			}
			ids = append(ids, sha)
			last = true
		}
	}
	return ids, last, nil
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

// look reads the sequencer's lists and counts the folds it has run since
// wt's last look. A list that cannot be read counts nothing.
func (w *foldWatch) look(wtPath string) {
	done, lastDone, err := foldCommands(wtPath, "done")
	if err != nil {
		return
	}
	todo, _, err := foldCommands(wtPath, "git-rebase-todo")
	if err != nil {
		return
	}
	w.inFlight = nil
	if lastDone && conflictedPick(wtPath) {
		w.inFlight = append(w.inFlight, done[len(done)-1])
		done = done[:len(done)-1]
	}
	w.inFlight = append(w.inFlight, todo...)
	for i, id := range done {
		if i >= w.skip && !slices.Contains(w.executed, id) {
			w.executed = append(w.executed, id)
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
func startAt(wtPath string, seen []string) *foldWatch {
	w := &foldWatch{executed: slices.Clone(seen)}
	done, lastDone, err := foldCommands(wtPath, "done")
	if err != nil {
		return w
	}
	w.skip = len(done)
	if lastDone && conflictedPick(wtPath) {
		w.skip--
	}
	// What the --continue wt makes next carries out.
	w.look(wtPath)
	return w
}
