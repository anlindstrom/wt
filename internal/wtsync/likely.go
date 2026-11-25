package wtsync

import (
	"strings"
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
