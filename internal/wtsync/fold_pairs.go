package wtsync

import "strings"

// FoldPair is a fixup!, squash! or amend! commit of a line and the commit
// git's autosquash folds it into. FixupAt and TargetAt are their places in
// the commits the pairs were read from, oldest first.
type FoldPair struct {
	Fixup, FixupSubject   string
	Target, TargetSubject string
	FixupAt, TargetAt     int
}

// FoldPairs is every fold commit among commits, oldest first, with the
// commit a fold of the line makes it part of. A fold whose name finds no
// commit, or more than one, has no pair: the run refuses those itself.
// Nothing is written.
func FoldPairs(dir string, commits []string) ([]FoldPair, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	out, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H%x00%s"}, commits...)...)
	if err != nil {
		return nil, err
	}
	var all []lineCommit
	for _, l := range strings.Split(out, "\n") {
		if sha, subject, ok := strings.Cut(l, "\x00"); ok {
			all = append(all, lineCommit{sha, subject})
		}
	}
	index := map[string]int{}
	for i, c := range all {
		index[c.sha] = i
	}
	var pairs []FoldPair
	for i, c := range all {
		j, ok := foldTarget(dir, all[:i], c.subject, index)
		if !ok {
			continue
		}
		pairs = append(pairs, FoldPair{Fixup: c.sha, FixupSubject: c.subject, Target: all[j].sha, TargetSubject: all[j].subject, FixupAt: i, TargetAt: j})
	}
	return pairs, nil
}
