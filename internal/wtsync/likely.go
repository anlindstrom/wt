package wtsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// LikelyStop is a commit the replay did not reach, because an earlier stop
// belongs to a person, that changes a file trunk changed too. Git may still
// merge it; the overlap is what makes a stop likely, not certain.
type LikelyStop struct {
	Index   int
	Total   int
	Commit  string
	Subject string
	// Paths are the files it shares with what trunk changed, in the order
	// the commit lists them.
	Paths []string
}

// ReworkedCommit is a replayed commit with the subject and author of a
// commit already on trunk. Trunk's copy is a different patch, so git does
// not drop it; whether trunk holds its changes is for a person to say.
type ReworkedCommit struct {
	Index        int
	Total        int
	Commit       string
	Subject      string
	TrunkCommit  string
	TrunkSubject string
}

// trunkChanged is the paths t's target changed since the replayed range
// began: every file a replayed commit can meet there.
func trunkChanged(mainRoot string, t Target, branch string) (map[string]bool, error) {
	base, err := t.base(mainRoot, branch)
	if err != nil {
		return nil, err
	}
	out, err := runGit(mainRoot, nil, nil, "diff", "--name-only", "--no-renames", "-z", base, t.Onto, "--")
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths[p] = true
		}
	}
	return paths, nil
}

// likelyStops lists the commits after the first stop a person owns that
// change a file trunk changed. A declared rule does not take a file out: it
// may refuse the conflict there, which only the replay can say.
func likelyStops(mainRoot string, t Target, branch string, commits []commitPaths, after int) ([]LikelyStop, error) {
	if after >= len(commits) {
		return nil, nil
	}
	changed, err := trunkChanged(mainRoot, t, branch)
	if err != nil || len(changed) == 0 {
		return nil, err
	}
	var likely []LikelyStop
	for i := after; i < len(commits); i++ {
		var shared []string
		for _, p := range commits[i].Paths {
			if changed[p] {
				shared = append(shared, p)
			}
		}
		if len(shared) > 0 {
			likely = append(likely, LikelyStop{Index: i + 1, Total: len(commits), Commit: commits[i].SHA, Subject: commits[i].Subject, Paths: shared})
		}
	}
	return likely, nil
}

type authored struct{ sha, subject, who string }

// authoredLog is the non-merge commits of rng, newest first, with the
// author's name and address.
func authoredLog(mainRoot, rng string) ([]authored, error) {
	out, err := runGit(mainRoot, nil, nil, "log", "--no-merges", "--format=%H%x00%an <%ae>%x00%s%x01", rng, "--")
	if err != nil {
		return nil, err
	}
	var log []authored
	for _, rec := range strings.Split(string(out), "\x01") {
		rec = strings.TrimPrefix(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x00", 3)
		if len(f) == 3 {
			log = append(log, authored{sha: f[0], who: f[1], subject: f[2]})
		}
	}
	return log, nil
}

// reworkedOnTrunk names each replayed commit that shares its subject and
// author with a commit t's target added since the range began.
func reworkedOnTrunk(mainRoot string, t Target, branch string, commits []commitPaths) ([]ReworkedCommit, error) {
	base, err := t.base(mainRoot, branch)
	if err != nil {
		return nil, err
	}
	trunk, err := authoredLog(mainRoot, base+".."+t.Onto)
	if err != nil || len(trunk) == 0 {
		return nil, err
	}
	mine, err := authoredLog(mainRoot, t.cutoff()+"..."+branch)
	if err != nil {
		return nil, err
	}
	whoOf := map[string]string{}
	for _, m := range mine {
		whoOf[m.sha] = m.who
	}
	type key struct{ subject, who string }
	onTrunk := map[key]authored{}
	for _, c := range trunk {
		k := key{c.subject, c.who}
		if _, seen := onTrunk[k]; !seen {
			onTrunk[k] = c
		}
	}
	var found []ReworkedCommit
	for i, c := range commits {
		if tc, ok := onTrunk[key{c.Subject, whoOf[c.SHA]}]; ok && whoOf[c.SHA] != "" {
			found = append(found, ReworkedCommit{Index: i + 1, Total: len(commits), Commit: c.SHA, Subject: c.Subject, TrunkCommit: tc.sha, TrunkSubject: tc.subject})
		}
	}
	return found, nil
}

// TrunkCopy is the commit the target added since the replayed range began
// under a replayed commit's subject and author. Taken is the files both
// changed, which a drop of the replayed commit takes the target's side of;
// Kept is the replayed commit's other files, whose changes it keeps.
type TrunkCopy struct {
	Commit  string   `json:"commit"`
	Subject string   `json:"subject"`
	Taken   []string `json:"taken"`
	Kept    []string `json:"kept"`
}

// DropCommand is the command that takes the drop at the stop at commit.
func DropCommand(work, commit string) string {
	return fmt.Sprintf("wt sync resume %s --drop %s", work, git.ShortID(commit, 7))
}

// Offer is what the stop at commit says of c: trunk's copy, and what the
// drop takes and keeps.
func (c TrunkCopy) Offer(work, commit string) string {
	what := fmt.Sprintf("takes trunk's %s and keeps the commit's %s", strings.Join(c.Taken, ", "), strings.Join(c.Kept, ", "))
	if len(c.Kept) == 0 {
		what = fmt.Sprintf("takes trunk's %s, which is every file of the commit, so the commit is dropped", strings.Join(c.Taken, ", "))
	}
	return fmt.Sprintf("trunk may already carry this commit, reworked, as %s %q (same subject and author). If trunk's copy replaces it: %s %s",
		git.ShortID(c.Commit, 7), c.Subject, DropCommand(work, commit), what)
}

// trunkCopyOf is the newest commit of base..onto with commit's subject and
// author, nil for none, or for one that shares no file with commit.
func trunkCopyOf(mainRoot, base, onto, commit string) (*TrunkCopy, error) {
	mine, err := authoredLog(mainRoot, commit+"^!")
	if err != nil || len(mine) == 0 {
		return nil, err
	}
	trunk, err := authoredLog(mainRoot, base+".."+onto)
	if err != nil {
		return nil, err
	}
	for _, c := range trunk {
		if c.subject != mine[0].subject || c.who != mine[0].who {
			continue
		}
		theirs, err := changedPaths(mainRoot, c.sha)
		if err != nil {
			return nil, err
		}
		ours, err := changedPaths(mainRoot, commit)
		if err != nil {
			return nil, err
		}
		tc := &TrunkCopy{Commit: c.sha, Subject: c.subject, Taken: []string{}, Kept: []string{}}
		for _, p := range ours {
			if slices.Contains(theirs, p) {
				tc.Taken = append(tc.Taken, p)
			} else {
				tc.Kept = append(tc.Kept, p)
			}
		}
		if len(tc.Taken) == 0 {
			return nil, nil
		}
		return tc, nil
	}
	return nil, nil
}

// changedPaths is the files commit changed from its first parent.
func changedPaths(mainRoot, commit string) ([]string, error) {
	out, err := runGit(mainRoot, nil, nil, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", commit+"^", commit, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
