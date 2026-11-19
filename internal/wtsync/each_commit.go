package wtsync

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/git"
)

// EachCommitTimeout bounds one each_commit check at one commit. The run's
// own git calls are bounded by GitTimeout; a check is not one of them, and
// is bounded on its own.
var EachCommitTimeout = 30 * time.Minute

// Where a run's each_commit checks run, as --each-commit says it.
const (
	// EachAll checks at every replayed commit that touches a step's paths:
	// what a run does without the flag.
	EachAll = "all"
	// EachTips checks only at the tip of each layer, or of the branch.
	EachTips = "tips"
	// EachTargets checks at the tip of each layer, as tips does, and at each
	// fold target: a commit a fixup, squash or amend folded into. It is for a
	// fold in place.
	EachTargets = "targets"
	// EachOff runs no each_commit check.
	EachOff = "off"
)

// TargetsRefusal is why --each-commit=targets is refused for a run that is
// not a fold in place. An edit run is told what an edit run under tips is: its
// checks sit above the edit, so off, or edit first and check after.
func TargetsRefusal(edit bool) string {
	const why = "--each-commit=targets is for an in-place fold (--stack --autosquash, without --onto): there the commits that are not fold targets keep their own changes"
	if edit {
		return why + "; an --edit run's checks sit at the commits above the edit: use --each-commit=off, or edit first and check after"
	}
	return why + "; use --each-commit=tips for any other run"
}

// atTips is whether mode checks at the tip of each layer: tips, and targets,
// which adds the fold targets to them.
func atTips(mode string) bool { return mode == EachTips || mode == EachTargets }

// EachCommitMode is mode as a run's own: "" is all. An unknown mode is an
// error naming the ones there are.
func EachCommitMode(mode string) (string, error) {
	switch mode {
	case "", EachAll:
		return EachAll, nil
	case EachTips, EachTargets, EachOff:
		return mode, nil
	}
	return "", fmt.Errorf("--each-commit takes all, tips, targets or off, not %q", mode)
}

// EachCommitNote is the plan's and the summary's one line for a mode other
// than all, "" for all. stacked says the run has layers.
func EachCommitNote(mode string, stacked bool) string {
	switch mode {
	case EachTips:
		where := "the tip of the branch only"
		if stacked {
			where = "the tip of each layer only"
		}
		return "each_commit checks run at " + where + " (--each-commit=tips): the commits between are not checked, and are not owed"
	case EachTargets:
		where := "the tip of the branch"
		if stacked {
			where = "the tip of each layer"
		}
		return "each_commit checks run at the commits a fixup folded into and at " + where + " (--each-commit=targets): the commits between are not checked, and are not owed"
	case EachOff:
		return "each_commit checks off (--each-commit=off): none ran, and none is owed"
	}
	return ""
}

// checkTail is how many lines of a failing check's output are kept.
const checkTail = 20

// CheckFailure is an each_commit check that failed at a replayed commit:
// the step, the commit it ran at, where that commit is in the rebase, what
// the step's exit said and the tail of its output.
type CheckFailure struct {
	Step         string
	Commit       string
	Subject      string
	Index, Total int
	Err          string
	Output       string
	// Retried is whether the step's retry ran and failed too: Err then
	// names both exits, and Output is the retry's.
	Retried bool
}

// Line is the failure in one line: the step, the commit and why.
func (c CheckFailure) Line() string {
	return fmt.Sprintf("check %q failed at %s %q: %s", c.Step, git.ShortID(c.Commit, 7), c.Subject, c.Err)
}

// Unchecked is an each_commit check a run did not run because a need of it
// failed its probe: owed for the whole branch.
type Unchecked struct {
	Step string `json:"step"`
	Why  string `json:"why"`
}

// Owed is how the run names the unchecked step among the owed ones.
func (u Unchecked) Owed() string { return u.Step + " (not checked: " + u.Why + ")" }

// PlanChecks lists, oldest first, the commits of base...branch a rebase
// would check: each one that changes a path some each_commit step lists,
// or every one when a step lists none. A commit trunk already carries is
// left out, since the rebase drops it, unless a lifting rule claims a file
// it changes, which the run replays and lifts.
func PlanChecks(wtPath string, cfg *Config, base, branch string) ([]Commit, error) {
	commits, _, err := checksDue(wtPath, cfg, Target{Onto: base}, branch)
	return commits, err
}

// checksDue is PlanChecks with the each_commit steps that run at one of
// the commits at least, in the order declared.
func checksDue(wtPath string, cfg *Config, t Target, branch string) ([]Commit, []EachCommit, error) {
	if cfg == nil || len(cfg.EachCommit) == 0 {
		return nil, nil, nil
	}
	commits, err := t.commits(wtPath, branch, "--right-only", "--no-merges")
	if err != nil {
		return nil, nil, err
	}
	lift := cfg.liftPatterns()
	ran := make([]bool, len(cfg.EachCommit))
	var out []Commit
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		if c.OnTrunk && !touchesAny(lift, c.Paths) {
			continue
		}
		checked := false
		for j, s := range cfg.EachCommit {
			if len(s.Paths) == 0 || touchesAny(s.Paths, c.Paths) {
				ran[j], checked = true, true
			}
		}
		if checked {
			out = append(out, Commit{SHA: c.SHA, Subject: c.Subject})
		}
	}
	var steps []EachCommit
	for j, s := range cfg.EachCommit {
		if ran[j] {
			steps = append(steps, s)
		}
	}
	return out, steps, nil
}

// checksFor is the each_commit steps a commit changing paths runs.
func (c *Config) checksFor(paths []string) []EachCommit {
	var steps []EachCommit
	for _, s := range c.EachCommit {
		if len(s.Paths) == 0 || touchesAny(s.Paths, paths) {
			steps = append(steps, s)
		}
	}
	return steps
}

// planChecks fills in the driver's list of commits to check, and under
// --each-commit=tips or targets the steps due at each of them.
func (d *driver) planChecks() error {
	d.checks, d.callSteps = nil, nil
	switch d.req.EachCommit {
	case EachOff:
		return nil
	case EachTips, EachTargets:
		return d.planTipChecks()
	}
	commits, _, err := checksDue(d.req.Path, d.cfg, d.req.target(), d.req.Branch)
	if err != nil {
		return err
	}
	for _, c := range commits {
		d.checks = append(d.checks, c.SHA)
	}
	d.checks, err = d.editChecks(d.checks)
	return err
}

// planTipChecks is planChecks for --each-commit=tips and targets: the commits
// that call for a check, as under all, each with the steps it calls for. The
// break that runs them is the end of the layer they land in, which the list
// git writes decides, not this plan: a fixup folded into an earlier commit, or
// a pick git drops as empty, moves where a layer ends. Under targets the group
// a fixup folded into gets a break of its own as well.
func (d *driver) planTipChecks() error {
	cfg := d.cfg
	if cfg == nil || len(cfg.EachCommit) == 0 {
		return nil
	}
	if d.req.Edit != nil {
		return errors.New("--each-commit=tips cannot be combined with --edit: the checks of an edit run sit at the commits above the edit")
	}
	commits, err := d.req.target().commits(d.req.Path, d.req.Branch, "--right-only", "--no-merges")
	if err != nil {
		return err
	}
	lift := cfg.liftPatterns()
	d.callSteps = map[string][]int{}
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		if c.OnTrunk && !touchesAny(lift, c.Paths) {
			continue
		}
		var called []int
		for j, s := range cfg.EachCommit {
			if len(s.Paths) == 0 || touchesAny(s.Paths, c.Paths) {
				called = append(called, j)
			}
		}
		if len(called) > 0 {
			d.checks = append(d.checks, c.SHA)
			d.callSteps[c.SHA] = called
		}
	}
	return nil
}

// checkAt reports whether the break the sequencer stopped at is a check
// stop, the break placed after the pick of a commit to check, and which
// commit (its full id) that pick replayed. The lift's own breaks come
// before a pick, never directly after a checked one, since the check's
// break is written first.
func (d *driver) checkAt() (sha string, edited bool, err error) {
	if len(d.checks) == 0 {
		return "", false, nil
	}
	d.tipStop = false
	if atTips(d.req.EachCommit) {
		sha, edited, err = d.tipAt()
		if err != nil || sha != "" || d.req.EachCommit == EachTips {
			d.tipStop = sha != ""
			return sha, edited, err
		}
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return "", false, err
	}
	done, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return "", false, err
	}
	var cmds [][]string
	for _, line := range strings.Split(string(done), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		cmds = append(cmds, f)
	}
	if len(cmds) < 2 || cmds[len(cmds)-1][0] != "break" {
		return "", false, nil
	}
	// In a fold the break follows the group's fixup and squash lines: the
	// commit checked is the pick they were folded into, and git's record of
	// what it rewrote ends at the last of them.
	i := len(cmds) - 2
	d.checkTail = ""
	var members []string
	for ; i > 0 && (cmds[i][0] == "fixup" || cmds[i][0] == "squash"); i-- {
		id := cmds[i][1]
		if len(cmds[i]) >= 3 && strings.HasPrefix(id, "-") {
			id = cmds[i][2]
		}
		if d.checkTail == "" {
			d.checkTail = id
		}
		members = append(members, id)
	}
	prev := cmds[i]
	if (prev[0] != "pick" && prev[0] != "edit") || len(prev) < 2 {
		return "", false, nil
	}
	if d.req.EachCommit == EachTargets && len(members) == 0 {
		// A break after a pick nothing was folded into is not a target's.
		return "", false, nil
	}
	for _, c := range d.checks {
		if strings.HasPrefix(c, prev[1]) {
			return c, prev[0] == "edit", nil
		}
	}
	// A group is checked when a fixup or squash folded into its pick is to
	// be checked: the commit checked is still the pick, with them folded in.
	for _, m := range members {
		for _, c := range d.checks {
			if strings.HasPrefix(c, m) {
				sha, err := d.git("rev-parse", "--verify", prev[1]+"^{commit}")
				if err != nil {
					return "", false, err
				}
				return sha, prev[0] == "edit", nil
			}
		}
	}
	return "", false, nil
}

// stoppedGroup says whether the sequencer sits inside a group, the last
// command it ran being a pick, edit, or a fixup or squash folded into one,
// and whether that group is to be checked. A stop the run handed over is
// such a place: the break that would have followed the group was taken out
// with the rest of the run's own stops, and the list that is left has the
// pick behind it. A list that already goes on with a break needs nothing.
func (d *driver) stoppedGroup() (open, due, folded bool, err error) {
	if len(d.checks) == 0 {
		return false, false, false, nil
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return false, false, false, err
	}
	done, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return false, false, false, err
	}
	if next, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo")); err == nil {
		for _, line := range strings.Split(string(next), "\n") {
			if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
				if f[0] == "break" {
					return false, false, false, nil
				}
				break
			}
		}
	}
	var cmds [][]string
	for _, line := range strings.Split(string(done), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
			cmds = append(cmds, f)
		}
	}
	i := len(cmds) - 1
	var ids []string
	for ; i >= 0 && (cmds[i][0] == "fixup" || cmds[i][0] == "squash"); i-- {
		id := cmds[i][1]
		if len(cmds[i]) >= 3 && strings.HasPrefix(id, "-") {
			id = cmds[i][2]
		}
		ids = append(ids, id)
	}
	if i < 0 || (cmds[i][0] != "pick" && cmds[i][0] != "edit") || len(cmds[i]) < 2 {
		return false, false, false, nil
	}
	ids = append(ids, cmds[i][1])
	if d.req.EachCommit == EachTips {
		// No break closes a group under tips: a layer's end does.
		return true, false, false, nil
	}
	// Under targets the break that closes the group is written when a fixup
	// or squash has been folded into it, now or by the rest of the list.
	folded = slices.ContainsFunc(cmds[i+1:], func(c []string) bool { return c[0] == "fixup" || c[0] == "squash" })
	for _, id := range ids {
		for _, c := range d.checks {
			if strings.HasPrefix(c, id) {
				return true, true, folded, nil
			}
		}
	}
	return true, false, false, nil
}

// doneCommands is the sequencer's done list as fields, comments left out.
func (d *driver) doneCommands() ([][]string, string, error) {
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return nil, "", err
	}
	done, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return nil, "", err
	}
	var cmds [][]string
	for _, line := range strings.Split(string(done), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
			cmds = append(cmds, f)
		}
	}
	return cmds, dir, nil
}

// layerEndsNext says the next command of the list is a layer's marker, or
// there is none: the place a layer's end break sits before.
func layerEndsNext(dir string) bool {
	next, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo"))
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(next), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") && !isForkMark(f) {
			return f[0] == "update-ref"
		}
	}
	return true
}

// layerSegment is the commands of done, backwards from the end, that make
// up the layer now ending: every pick, edit, fixup and squash since the last
// marker, with their ids as the list names them, and the last pick's id.
func layerSegment(cmds [][]string) (ids []string, pick string, edit bool) {
	for i := len(cmds) - 1; i >= 0; i-- {
		c := cmds[i]
		switch c[0] {
		case "update-ref":
			if isForkMark(c) {
				continue
			}
			return ids, pick, edit
		case "pick", "edit":
			if len(c) < 2 {
				continue
			}
			ids = append(ids, c[1])
			if pick == "" {
				pick = c[1]
			}
			edit = edit || c[0] == "edit"
		case "fixup", "squash":
			if len(c) < 2 {
				continue
			}
			id := c[1]
			if len(c) >= 3 && strings.HasPrefix(id, "-") {
				id = c[2]
			}
			ids = append(ids, id)
		}
	}
	return ids, pick, edit
}

// calledFor is the indexes of the each_commit steps some commit among ids
// called for, in the order declared.
func (d *driver) calledFor(ids []string) []int {
	var out []int
	for j := range d.cfg.EachCommit {
		for sha, called := range d.callSteps {
			if slices.Contains(called, j) && slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(sha, id) }) {
				out = append(out, j)
				break
			}
		}
	}
	return out
}

// tipAt is checkAt for --each-commit=tips: whether the break the sequencer
// stopped at ends a layer (it sits before a layer's marker, or at the end of
// the list) whose commits called for a check, and the full id of the last
// pick of it. d.layerIDs is then the layer's commands, for atCheck.
func (d *driver) tipAt() (sha string, edited bool, err error) {
	cmds, dir, err := d.doneCommands()
	if err != nil {
		return "", false, err
	}
	if len(cmds) < 2 || cmds[len(cmds)-1][0] != "break" || !layerEndsNext(dir) {
		return "", false, nil
	}
	ids, pick, edit := layerSegment(cmds)
	d.layerIDs, d.layerEdit = ids, edit
	if pick == "" || len(d.calledFor(ids)) == 0 {
		return "", false, nil
	}
	sha, err = d.git("rev-parse", "--verify", pick+"^{commit}")
	return sha, edit, err
}

// targetsAmong says, for each commit of run (the layer's commits as they are
// now, oldest first), whether a fixup, squash or amend! folded into it: the
// layer's own commands as the sequencer ran them name the picks that took one,
// and git's record of what it rewrote names what each became.
func (d *driver) targetsAmong(run []string) ([]bool, error) {
	cmds, dir, err := d.doneCommands()
	if err != nil {
		return nil, err
	}
	var taken []string
	pick := ""
	for _, c := range cmds {
		switch c[0] {
		case "update-ref":
			if !isForkMark(c) {
				pick, taken = "", nil
			}
		case "pick", "edit":
			pick = ""
			if len(c) >= 2 {
				pick = c[1]
			}
		case "fixup", "squash":
			if pick != "" && !slices.Contains(taken, pick) {
				taken = append(taken, pick)
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "rewritten-list"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var became []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && slices.ContainsFunc(taken, func(id string) bool { return strings.HasPrefix(f[0], id) }) {
			became = append(became, f[1])
		}
	}
	out := make([]bool, len(run))
	for i, c := range run {
		out[i] = slices.Contains(became, c)
	}
	return out, nil
}

// targetsOf is the commits of rewritten a run under --each-commit=targets
// checks after a step folded into them: the owner the output went into (the
// first), every commit a fixup had been folded into, and the tip. Without a
// record of the targets every commit is kept.
func (d *driver) targetsOf(rewritten []string) []string {
	if len(d.foldTargets) != len(rewritten) {
		return rewritten
	}
	var out []string
	for i, c := range rewritten {
		if i == 0 || i == len(rewritten)-1 || d.foldTargets[i] {
			out = append(out, c)
		}
	}
	return out
}

// tipCarry says the layer the rest of the list starts in already holds a
// commit that called for a check, replayed before the stop a resume goes on
// from. A stop at a layer's end break is not such a place: that break runs
// the layer's check itself.
func (d *driver) tipCarry() (bool, error) {
	if !atTips(d.req.EachCommit) || len(d.checks) == 0 {
		return false, nil
	}
	cmds, dir, err := d.doneCommands()
	if err != nil {
		return false, err
	}
	if n := len(cmds); n > 0 && cmds[n-1][0] == "break" && layerEndsNext(dir) {
		return false, nil
	}
	ids, _, _ := layerSegment(cmds)
	return len(d.calledFor(ids)) > 0, nil
}

// layerSurvived says some commit of the layer now ending is still in the
// result: git recorded a rewrite of one, or kept one as it was and not the target's (a fold in
// place leaves its leading picks alone, and records nothing for them), or one
// was an edit, whose lift may have committed in its place. A layer whose
// every pick git dropped as empty has no commit of its own to check: HEAD is
// the layer below's tip.
func (d *driver) layerSurvived() (bool, error) {
	if d.layerEdit {
		return true, nil
	}
	for _, id := range d.layerIDs {
		// Kept as it was: HEAD carries it (nothing of id is missing from HEAD),
		// and the target does not, which would make it the target's own.
		missing, err := d.git("rev-list", "--max-count=1", id, "--not", "HEAD")
		if err != nil {
			return false, err
		}
		if missing != "" {
			continue
		}
		fresh, err := d.git("rev-list", "--max-count=1", id, "--not", d.req.Onto)
		if err != nil {
			return false, err
		}
		if fresh != "" {
			return true, nil
		}
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "rewritten-list"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && slices.ContainsFunc(d.layerIDs, func(id string) bool { return strings.HasPrefix(f[0], id) }) {
			return true, nil
		}
	}
	return false, nil
}

// pickCommitted reports whether the pick of sha left a commit at HEAD:
// git's own record of what it rewrote names sha and HEAD, or, at a pick
// the run stopped around (edited), HEAD moved past where the pick was
// applied: the lift commits a dropped pick's bump itself. A pick git
// dropped as empty has neither.
func (d *driver) pickCommitted(sha, head string, edited bool) (bool, error) {
	if edited && d.before != "" && head != d.before {
		return true, nil
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "rewritten-list"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 2 || f[1] != head {
		return false, nil
	}
	return f[0] == sha || (d.checkTail != "" && strings.HasPrefix(f[0], d.checkTail)), nil
}

// atCheck runs the each_commit steps at the check stop for sha. failed is
// the first step that failed, nil when every one passed or none was due.
func (d *driver) atCheck(sha string, edited bool, p Progress, forced bool) (*CheckFailure, error) {
	head, err := d.git("rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if !forced {
		var ok bool
		if d.tipStop {
			ok, err = d.layerSurvived()
		} else {
			ok, err = d.pickCommitted(sha, head, edited)
		}
		if err != nil || !ok {
			return nil, err
		}
	}
	paths, err := d.git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--root", "HEAD")
	if err != nil {
		return nil, err
	}
	if forced {
		// A resume after a person's fix: HEAD may be a commit they added on
		// top of the one that failed, so the checks of that one run too.
		own, err := d.git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--root", sha)
		if err != nil {
			return nil, err
		}
		paths += "\x00" + own
	}
	var changed []string
	for _, p := range strings.Split(paths, "\x00") {
		if p != "" {
			changed = append(changed, p)
		}
	}
	steps := d.cfg.checksFor(changed)
	if d.tipStop {
		// At a layer's end the steps due are those any commit of it called for.
		// HEAD's own paths count too, as under all: a fix a person amended in
		// may touch what another check lists.
		called := d.calledFor(d.layerIDs)
		steps = nil
		for j, s := range d.cfg.EachCommit {
			if slices.Contains(called, j) || len(s.Paths) == 0 || touchesAny(s.Paths, changed) {
				steps = append(steps, s)
			}
		}
	}
	subject, err := d.git("log", "-1", "--format=%s", "HEAD")
	if err != nil {
		return nil, err
	}
	var run []EachCommit
	for _, s := range steps {
		if why := d.unavailable(s.Needs); why != "" {
			d.uncheck(s.Run, why)
			continue
		}
		run = append(run, s)
	}
	if len(run) == 0 {
		return nil, nil
	}
	started := false
	for _, s := range run {
		// Judged now, not before the first step ran: an earlier step may
		// have generated the executable this one runs.
		if why := d.scriptMissing(s.Run, head); why != "" {
			d.uncheck(s.Run, why)
			continue
		}
		if !started {
			started = true
			d.res.Checked++
			if d.log != nil {
				at := slices.Index(d.checks, sha) + 1
				if at == 0 {
					at = d.res.Checked
				}
				fmt.Fprintf(d.log, "  ▸ check %d/%d %s %s\n", at, max(len(d.checks), at), git.ShortID(head, 7), subject)
			}
		}
		why, output, retried := runEachCommit(d.req.Path, s, EachCommitTimeout)
		if why == "" {
			if retried && d.log != nil {
				fmt.Fprintf(d.log, "  ✓ %s passed on retry\n", s.Run)
			}
			continue
		}
		f := &CheckFailure{Step: s.Run, Commit: head, Subject: subject, Index: p.Index, Total: p.Total, Err: why, Output: output, Retried: retried}
		if d.log != nil {
			fmt.Fprintf(d.log, "  ✗ %s: %s\n", s.Run, why)
			for _, line := range strings.Split(output, "\n") {
				if line != "" {
					fmt.Fprintf(d.log, "    %s\n", line)
				}
			}
		}
		return f, nil
	}
	return nil, nil
}

// unavailable is why a step with needs cannot run in this run: the probe
// answer of the first need the run was told is missing, "" when none is.
func (d *driver) unavailable(needs []string) string {
	return unavailableWhy(needs, d.req.Unavailable)
}

// scriptMissing is why a step whose command starts with a repository path
// (bin/x.sh, ./gradlew) cannot run at the commit head: the script is not in
// its tree. A tree that predates a script trunk declares is the usual
// reason, and the step is owed, not failed with exit 127. A script trunk
// does not have either is a declaration error. A file the worktree has is
// there, tracked or not. "" when the command does not
// start with a repository path, or the script is there.
func (d *driver) scriptMissing(run, head string) string {
	script := repoScript(run)
	if script == "" || gitHas(d.req.Path, head, script) {
		return ""
	}
	// A tool the worktree has but git does not track (.venv/bin/pytest,
	// node_modules/.bin/tsc) is there.
	if _, err := os.Stat(filepath.Join(d.req.Path, script)); err == nil {
		return ""
	}
	trunk := cmp.Or(d.req.Trunk, d.req.Onto)
	if gitHas(d.req.Path, trunk, script) {
		why := fmt.Sprintf("%s is not in the tree at %s: the commit predates it", script, git.ShortID(head, 7))
		if d.log != nil {
			fmt.Fprintf(d.log, "  ⏭ %s not checked at %s: %s\n", run, git.ShortID(head, 7), why)
		}
		return why
	}
	why := fmt.Sprintf("%s is in neither the tree nor trunk: the declaration names a script that is not there", script)
	if d.log != nil && !d.declared[run] {
		if d.declared == nil {
			d.declared = map[string]bool{}
		}
		d.declared[run] = true
		fmt.Fprintf(d.log, "  ✗ declaration: %s: %s\n", run, why)
	}
	return why
}

// repoScript is the repository path a step's command starts with, "" when
// it starts with a program looked up on the PATH, an absolute path or a
// shell expansion.
func repoScript(run string) string {
	f := strings.Fields(run)
	if len(f) == 0 {
		return ""
	}
	first := f[0]
	if strings.ContainsAny(first, "$`~\"'=;|&<>(){}*?") || strings.HasPrefix(first, "/") || strings.HasPrefix(first, "../") {
		return ""
	}
	first = strings.TrimPrefix(first, "./")
	if !strings.Contains(first, "/") && first == f[0] {
		return ""
	}
	return path.Clean(first)
}

// gitHas is whether rev has a file at p.
func gitHas(dir, rev, p string) bool {
	_, err := gitEnv(dir, nil, nil, "cat-file", "-e", rev+":"+p)
	return err == nil
}

// uncheck records a check the run did not run, once per step.
func (d *driver) uncheck(step, why string) {
	for _, u := range d.res.Unchecked {
		if u.Step == step {
			return
		}
	}
	d.res.Unchecked = append(d.res.Unchecked, Unchecked{Step: step, Why: why})
}

// runCheck runs one command of a check in the worktree, bounded by timeout. why is ""
// for a pass; otherwise what failed. A check that leaves tracked files
// changed has failed: it must not change the commit it checks.
func runCheck(wtPath, run string, timeout time.Duration) (why, output string) {
	cmd := exec.Command("sh", "-c", run)
	cmd.Dir = wtPath
	cmd.Env = stepEnviron()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	timedOut, code, err := git.RunBounded(timeout, cmd)
	output = tailLines(strings.TrimSpace(out.String()), checkTail)
	var exit *exec.ExitError
	switch {
	case timedOut:
		return fmt.Sprintf("timed out after %s", timeout), output
	case errors.Is(err, exec.ErrWaitDelay):
	case errors.As(err, &exit):
		return fmt.Sprintf("exit %d", code), output
	case err != nil:
		return err.Error(), output
	}
	files, serr := trackedChanges(wtPath)
	switch {
	case serr != nil:
		return "status could not be read: " + serr.Error(), output
	case len(files) > 0:
		return "changed tracked files: " + strings.Join(files, ", "), output
	}
	return "", output
}

// trackedChanges lists the tracked files that differ from HEAD in the
// worktree.
func trackedChanges(wtPath string) ([]string, error) {
	status, err := gitEnv(wtPath, nil, nil, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	if err != nil || status == "" {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(status, "\n") {
		if len(l) > 3 {
			files = append(files, strings.TrimSpace(l[3:]))
		}
	}
	return files, nil
}

// runEachCommit runs one each_commit step: its run and, only when that
// fails, its retry, at the same commit and in the same way, each under
// timeout. why is "" for a pass; retried is whether the retry ran. When the
// retry fails too, why names both failures and output is the retry's. A run
// that left tracked files changed is not retried: the retry would start on
// a tree the step has already broken.
func runEachCommit(wtPath string, s EachCommit, timeout time.Duration) (why, output string, retried bool) {
	why, output = runCheck(wtPath, s.Run, timeout)
	if why == "" || s.Retry == "" {
		return why, output, false
	}
	if files, err := trackedChanges(wtPath); err != nil || len(files) > 0 {
		return why, output, false
	}
	rwhy, routput := runCheck(wtPath, s.Retry, timeout)
	if rwhy == "" {
		return "", routput, true
	}
	return fmt.Sprintf("%s; its retry %q failed too: %s", why, s.Retry, rwhy), routput, true
}

// tailLines keeps the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// failCheck ends the rebase at a check that failed: a stack parent is put
// back, as at a stop nobody claims; anything else is handed over at the
// commit, with nothing unmerged and HEAD the commit that failed, for a
// person to amend or add to, and wt sync resume runs the check again.
func (d *driver) failCheck(f *CheckFailure) (Result, error) {
	d.res.CheckFailed = f
	if d.req.Stacked {
		if d.keep {
			return d.fail(errors.New(f.Line() + "; this branch has descendants in the run, and a resume cannot put it back without discarding your work"))
		}
		rerr := d.restore()
		d.res.Restored = rerr == nil
		return d.res, rerr
	}
	h := &Handover{Index: f.Index, Total: f.Total, Commit: f.Commit, Subject: f.Subject, Staged: map[string]string{}, Check: f}
	d.res.LayerState = d.layerState()
	if err := StripMarks(d.req.Path); err != nil {
		return d.fail(err)
	}
	d.res.Left = h
	return d.res, nil
}

// renderCheckPlan is the brief left at a check that failed: which step, at
// which commit, what it said, and what to do.
func renderCheckPlan(in PlanInput) string {
	c := in.Handover.Check
	var b strings.Builder
	fmt.Fprintf(&b, "# rebase %s onto %s\n", in.Work, in.TrunkRef)
	fmt.Fprintf(&b, "stopped after %d/%d, %s %q: an each_commit check failed there\n",
		c.Index, c.Total, git.ShortID(c.Commit, 7), oneLinePlan(c.Subject))
	if note := EachCommitNote(in.EachCommit, in.Stacked); note != "" {
		fmt.Fprintf(&b, "%s\n", note)
	}
	fmt.Fprintf(&b, "\n## the check\n%s\n%s\n", c.Step, c.Err)
	if c.Output != "" {
		what := "its output"
		if c.Retried {
			what = "its retry's output"
		}
		fmt.Fprintf(&b, "\n## the end of %s\n```\n%s\n```\n", what, c.Output)
	}
	b.WriteString("\n## yours\n")
	b.WriteString("HEAD is the commit that failed, and nothing is unmerged. Fix it and amend the fix into the\n")
	b.WriteString("commit (git commit --amend), or add a commit of your own, so the worktree is clean again.\n")
	fmt.Fprintf(&b, "wt sync resume %s runs the check again at HEAD and goes on only when it passes;\n", in.Work)
	fmt.Fprintf(&b, "wt sync undo %s puts the branch back where the run found it.\n", in.Work)
	return b.String()
}

// CheckStopOf is a failure as the handover records it.
func CheckStopOf(f *CheckFailure) *CheckStop {
	if f == nil {
		return nil
	}
	return &CheckStop{Step: f.Step, Commit: f.Commit, Subject: f.Subject, Err: f.Err}
}

// CheckFailedLine is what a person is told at a handed-over check.
func CheckFailedLine(work string, f CheckFailure) string {
	return fmt.Sprintf("%s needs you. %s · fix it, amend the commit, then wt sync resume %s (or wt sync undo %s)",
		work, f.Line(), work, work)
}
