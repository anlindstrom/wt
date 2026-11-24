package wtsync

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Dropped is one commit a finished rebase set out to replay and is reported
// on: by its id before the rebase, its subject, and, for a stack held on one
// line, the layer it was in.
type Dropped struct {
	Commit  string
	Subject string
	Layer   string
	// Unmatched says the commit could not be told apart from another
	// commit of the same author, author date and subject, of which fewer
	// came out than went in. Only an Emptied entry is ever unmatched.
	Unmatched bool
}

// ReplayReport is what a finished rebase did to the commits it replayed
// that its caller should be told of.
type ReplayReport struct {
	// Emptied is each commit the rebase set out to replay that left no
	// commit in the result: trunk already had its change, or a conflict was
	// resolved to trunk's side.
	Emptied []Dropped
	// TestsOnly is each commit whose replay changed only paths matching the
	// declared test_paths while the original changed others too: trunk had
	// the rest.
	TestsOnly []Dropped
}

// ReplayPlan is what a rebase driven by wt planned for the commits it
// replays, read from the list git started it with: the one record of which
// commits it folded and which subjects it changed. A nil plan is no record.
type ReplayPlan struct {
	// Folds are the full ids of the commits the list folds into another.
	// A fixup that git left out of the list, because its destination
	// carries its patch, is not one of them.
	Folds []string `json:"folds"`
	// Subjects is the subject a replayed commit carries where an amend!
	// commit changes it, by the commit's full id.
	Subjects map[string]string `json:"subjects,omitempty"`
	// Confirmed says wt drove the rebase from its start to its end, so the
	// folds were carried out as planned.
	Confirmed bool `json:"confirmed,omitempty"`
	// Observed says wt has watched the rebase from its start, through every
	// stop and resume, so Folded is what ran. A plan recorded before wt
	// watched is not observed: its folds are checked against the result.
	Observed bool `json:"observed,omitempty"`
	// Folded are the full ids of the folds wt saw the rebase carry out,
	// recorded as they ran. Only an observed plan has them.
	Folded []string `json:"folded,omitempty"`
}

// replayPlanOf reads the plan from the list a rebase of the line started
// with.
func replayPlanOf(dir string, line Line, todo string) (*ReplayPlan, error) {
	plan := &ReplayPlan{Folds: []string{}}
	start, amend := "", ""
	flush := func() error {
		if start == "" || amend == "" {
			return nil
		}
		subject, err := plannedSubject(dir, start, amend)
		if err != nil {
			return err
		}
		own, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", start)
		if err != nil {
			return err
		}
		if subject != own {
			if plan.Subjects == nil {
				plan.Subjects = map[string]string{}
			}
			plan.Subjects[start] = subject
		}
		return nil
	}
	for _, e := range readStackTodo(todo, line.Commits) {
		if e.start {
			if err := flush(); err != nil {
				return nil, err
			}
			start, amend = e.sha, ""
			continue
		}
		plan.Folds = append(plan.Folds, e.sha)
		if e.amend {
			amend = e.sha
		}
	}
	return plan, flush()
}

// ReplayInput is a finished rebase, for ReportReplay.
type ReplayInput struct {
	// Cutoff and Old are the commits the rebase set out to replay:
	// Cutoff...Old, the right side, merges left out. Onto and New are what
	// it came to: Onto..New.
	Cutoff, Old, Onto, New string
	// TestPaths are the globs of the test files; none reports no TestsOnly.
	TestPaths []string
	// Line is the stack held on the line, when it was; its layers name each
	// commit.
	Line *Line
	// Plan is what wt planned: the folds that happened and the subjects an
	// amend! changed. Nil when no record of it exists, as for a rebase a
	// person finished by hand: a commit the result lacks is then reported
	// as a drop only where nothing could have changed what identifies it.
	Plan *ReplayPlan
	// Skip are full ids of commits to leave out: the ones an --edit run
	// reports itself.
	Skip []string
}

// ReportReplay compares a finished rebase with the commits it set out to
// replay, and reads history only. A commit is matched to its replay by author
// name, email and author date plus subject, which a rebase keeps, never by
// subject alone; the subject is the one the plan says an amend! gave it.
//
// Where the key of a commit is missing from the result, it is a drop only
// when nothing could have changed the key: a commit of the result that no
// original claims and that has the commit's author and date, or its author
// and subject, is the commit with a changed date or subject, and without a
// plan so is any commit in a range holding an amend!, and any fixup!,
// squash! or amend! commit, which may have been folded. Those are reported
// as emptied and Unmatched, as are commits that share a key when fewer of
// them came out than went in. A commit in the plan's Folds was folded and
// is left out.
//
// A TestsOnly commit needs a key of one commit going in and one coming out.
func ReportReplay(dir string, in ReplayInput) (ReplayReport, error) {
	var rep ReplayReport
	orig, err := readReplayLog(dir, "--right-only", "--no-merges", "--reverse", in.Cutoff+"..."+in.Old)
	if err != nil {
		return rep, err
	}
	repl, err := readReplayLog(dir, "--no-merges", in.Onto+".."+in.New)
	if err != nil {
		return rep, err
	}
	folded := map[string]bool{}
	var subjects map[string]string
	if in.Plan != nil {
		for _, f := range in.Plan.Folds {
			folded[f] = true
		}
		subjects = in.Plan.Subjects
	}
	skip := map[string]bool{}
	for _, s := range in.Skip {
		skip[s] = true
	}
	// An original's key is what its replay carries.
	for i := range orig {
		if subject, ok := subjects[orig[i].sha]; ok {
			orig[i].replayed = subject
			orig[i].key = orig[i].identity + "\x00" + subject
		}
	}
	wentIn := map[string]int{}
	for _, c := range orig {
		wentIn[c.key]++
	}
	cameOut := map[string][]loggedCommit{}
	for _, c := range repl {
		cameOut[c.key] = append(cameOut[c.key], c)
	}
	// What no original claims, by author and date, and by author and
	// subject: a commit of the result that may be one of them, changed.
	unclaimedDate, unclaimedSubject := map[string]bool{}, map[string]bool{}
	for _, c := range repl {
		if wentIn[c.key] == 0 {
			unclaimedDate[c.identity] = true
			unclaimedSubject[c.author+"\x00"+c.subject] = true
		}
	}
	// Surplus: more commits came out under a key than went in. Any of them
	// may be an original whose key changed, so an original left without a
	// partner is then in doubt.
	surplus := false
	for k, outs := range cameOut {
		surplus = surplus || (wentIn[k] > 0 && len(outs) > wentIn[k])
	}
	amended := false
	for _, c := range orig {
		amended = amended || strings.HasPrefix(c.subject, "amend! ")
	}
	var layerOf map[string]int
	if in.Line != nil {
		layerOf = in.Line.layerOf()
	}
	named := func(c loggedCommit, unmatched bool) Dropped {
		d := Dropped{Commit: c.sha, Subject: c.subject, Unmatched: unmatched}
		if n := layerOf[c.sha]; n > 0 {
			if n <= len(in.Line.Layers) {
				d.Layer = in.Line.Layers[n-1].Branch
			} else {
				d.Layer = in.Line.Branch
			}
		}
		return d
	}
	for _, o := range orig {
		if skip[o.sha] {
			continue
		}
		if folded[o.sha] {
			// A fold is left out only once it is confirmed: wt carried the
			// whole rebase out, or saw the fold run. A plan wt did not
			// watch is checked against the result instead; a watched one
			// is not, since a change in the result does not show the fold
			// ran: a rebase a person went on with by hand may have skipped
			// it.
			if in.Plan.Confirmed || slices.Contains(in.Plan.Folded, o.sha) || (!in.Plan.Observed && changePresent(dir, o.sha, in.New)) {
				continue
			}
			rep.Emptied = append(rep.Emptied, named(o, true))
			continue
		}
		out := cameOut[o.key]
		switch n := wentIn[o.key]; {
		case len(out) == 0:
			doubt := surplus || unclaimedDate[o.identity] || unclaimedSubject[o.author+"\x00"+o.replayed] ||
				(in.Plan == nil && (amended || isFold(o.subject)))
			rep.Emptied = append(rep.Emptied, named(o, doubt))
		case len(out) < n:
			rep.Emptied = append(rep.Emptied, named(o, true))
		case n == 1 && len(out) == 1 && testsOnly(in.TestPaths, o.paths, out[0].paths):
			rep.TestsOnly = append(rep.TestsOnly, named(o, false))
		}
	}
	return rep, nil
}

// changePresent is whether the change commit made is in the tree of tip: its
// patch applies in reverse to it. It reads history and writes nothing but a
// scratch index that is removed again. Where that cannot be read, the change
// is not known to be there.
func changePresent(dir, commit, tip string) bool {
	patch, err := runGit(dir, nil, nil, "diff", "--binary", "--no-ext-diff", "--no-renames", commit+"^", commit)
	if err != nil {
		return false
	}
	if len(patch) == 0 {
		// A message-only fixup leaves nothing in the tree to find: only a
		// rebase wt carried out can say it ran.
		return false
	}
	scratch, err := os.MkdirTemp("", "wt-report-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	if _, err := gitEnv(dir, env, nil, "read-tree", tip); err != nil {
		return false
	}
	_, err = gitEnv(dir, env, bytes.NewReader(patch), "apply", "--cached", "--check", "--reverse")
	return err == nil
}

// testsOnly is whether a replay that changed replayed kept only the paths
// matching tests, of an original that changed original.
func testsOnly(tests, original, replayed []string) bool {
	if len(tests) == 0 || len(replayed) == 0 {
		return false
	}
	for _, p := range replayed {
		if !matchesAny(tests, p) {
			return false
		}
	}
	for _, p := range original {
		if !matchesAny(tests, p) {
			return true
		}
	}
	return false
}

// loggedCommit is a commit as ReportReplay reads it: key is what a rebase
// keeps of it.
type loggedCommit struct {
	sha, subject, key string
	// author is the name and email, identity those with the author date.
	author, identity string
	// replayed is the subject the commit's replay carries.
	replayed string
	paths    []string
}

// readReplayLog lists commits with their author, author date, subject and
// changed paths, one git for the whole range.
func readReplayLog(dir string, args ...string) ([]loggedCommit, error) {
	full := append([]string{"log", "--date=raw", "--format=%x01%H%x00%an%x00%ae%x00%ad%x00%s", "--name-only", "-z", "--no-renames"}, args...)
	out, err := runGit(dir, nil, nil, append(full, "--")...)
	if err != nil {
		return nil, err
	}
	var commits []loggedCommit
	for _, rec := range strings.Split(string(out), "\x01") {
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x00", 6)
		if len(f) < 5 {
			return nil, fmt.Errorf("unexpected git log record %q", rec)
		}
		c := loggedCommit{sha: f[0], subject: f[4], key: strings.Join(f[1:5], "\x00"),
			author: strings.Join(f[1:3], "\x00"), identity: strings.Join(f[1:4], "\x00"), replayed: f[4]}
		if len(f) == 6 {
			for _, p := range strings.Split(strings.TrimPrefix(f[5], "\n"), "\x00") {
				if p != "" {
					c.paths = append(c.paths, p)
				}
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}
