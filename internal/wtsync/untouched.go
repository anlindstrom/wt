package wtsync

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// A layer the run leaves as it found it needs no check and no per-layer
// step: its tip is the very commit it was, so its content, and everything
// below it, is what the last run, or the person, left. unchangedLayers says
// from the plan which layers those are; a stop then runs nothing at one only
// when its commit is also, as the sequencer has it, the one the layer had.

// unchangedLayers is how many layers, from the bottom, the rest of this
// rebase leaves as they are: the rebase goes onto the line's own base, and
// the sequencer's list (what it has done and what it has left) picks the
// line's commits one by one in their own order, each with nothing folded
// into it. Zero when that cannot be told.
func (d *driver) unchangedLayers() int {
	if d.req.Stack == nil || d.req.ForceSteps || d.req.Edit != nil {
		return 0
	}
	l := d.req.Stack.Line
	if len(l.Commits) == 0 || l.Base == "" {
		return 0
	}
	// Whatever cannot be read leaves every layer as one to check.
	onto, err := d.git("rev-parse", "--verify", d.req.Onto+"^{commit}")
	if err != nil || onto != l.Base {
		return 0
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return 0
	}
	var cmds [][]string
	for _, name := range []string{"done", "git-rebase-todo"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0 // unreadable: not known
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
				cmds = append(cmds, f)
			}
		}
	}
	transparent := func(c string) bool { return c == "update-ref" || c == "break" || c == "exec" }
	kept := 0
scan:
	for i, c := range cmds {
		switch {
		case transparent(c[0]):
		case c[0] == "pick" && len(c) >= 2 && kept < len(l.Commits) && strings.HasPrefix(l.Commits[kept], c[1]):
			for _, next := range cmds[i+1:] {
				if transparent(next[0]) {
					continue
				}
				if next[0] == "fixup" || next[0] == "squash" {
					break scan
				}
				break
			}
			kept++
		default:
			break scan
		}
	}
	n := 0
	for _, ly := range l.Layers {
		if ly.Count > kept {
			return n
		}
		n++
	}
	if len(l.Commits) <= kept {
		n++
	}
	return n
}

// layerOfCommit is the layer, from 1, the line's commit sha is in, 0 when it
// is not on the line.
func (d *driver) layerOfCommit(sha string) int {
	l := d.req.Stack.Line
	at := slices.Index(l.Commits, sha)
	if at < 0 {
		return 0
	}
	for i, ly := range l.Layers {
		if at < ly.Count {
			return i + 1
		}
	}
	return len(l.Layers) + 1
}

// checkUntouched says the check stop for sha, an original commit of the
// line, is in a layer the run leaves as it was and HEAD is that very commit:
// at a layer's tip, the layer's own tip. It notes the layer for the line the
// run prints once.
func (d *driver) checkUntouched(sha string) (bool, error) {
	if d.req.Stack == nil || d.recheck {
		return false, nil
	}
	n := d.layerOfCommit(sha)
	if n == 0 {
		return false, nil
	}
	if n > d.unchangedLayers() || d.layerDebt(n) {
		return false, nil
	}
	head, err := d.git("rev-parse", "HEAD")
	if err != nil || head != sha {
		return false, err
	}
	if d.tipStop {
		if _, old := d.segment(n); head != old {
			return false, nil
		}
	}
	d.noteUntouched(n)
	return true, nil
}

func (d *driver) noteUntouched(n int) {
	if d.untouched == nil {
		d.untouched = map[int]bool{}
	}
	d.untouched[n] = true
}

// sayUntouched prints, once at the end of the run, that it left the layers
// noted as they were and ran nothing at them.
func (d *driver) sayUntouched() {
	if len(d.untouched) == 0 {
		return
	}
	var layers []int
	for n := range d.untouched {
		layers = append(layers, n)
	}
	slices.Sort(layers)
	d.untouched = nil
	if d.log == nil {
		return
	}
	var runs []string
	for i := 0; i < len(layers); {
		j := i
		for j+1 < len(layers) && layers[j+1] == layers[j]+1 {
			j++
		}
		if i == j {
			runs = append(runs, strconv.Itoa(layers[i]))
		} else {
			runs = append(runs, fmt.Sprintf("%d-%d", layers[i], layers[j]))
		}
		i = j + 1
	}
	what, its := "layer", "its"
	if len(layers) > 1 {
		what, its = "layers", "their"
	}
	fmt.Fprintf(d.log, "  ⏭ %s %s unchanged: %s checks and steps skipped\n", what, strings.Join(runs, ", "), its)
}

// owedBefore says the step was left owed at the layer's branch by the last
// run of this branch: it runs there whether or not the layer changed, so the
// record is settled by this run and not dropped by it.
func (d *driver) owedBefore(step, branch string) bool {
	if !d.owedRead {
		d.owedRead = true
		d.owedSet = map[string]bool{}
		if o, err := ReadOwed(d.mainRoot, d.req.Branch); err == nil && o != nil {
			for _, s := range o.Steps {
				d.owedSet[s.Step+"\x00"+s.Layer] = true
			}
		} else if err != nil {
			// A record that cannot be read is not known to be empty: every
			// step runs.
			d.owedUnknown = true
		}
	}
	return d.owedUnknown || d.owedSet[step+"\x00"+branch]
}

// layerDebt says layer n has work owed: a step the last run left owed at its
// branch, or a step that failed or was owed at a layer below in this run. Such
// a step runs, may change the layer, and settles the debt only by running, so
// nothing at the layer is skipped.
func (d *driver) layerDebt(n int) bool {
	if len(d.stepFailed) > 0 {
		return true
	}
	branch, _ := d.segment(n)
	for _, s := range d.layerSteps() {
		if d.owedBefore(s.Run, branch) {
			return true
		}
	}
	return false
}
