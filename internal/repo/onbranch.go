package repo

import "github.com/anlindstrom/wt/internal/git"

// OnBranch reports whether HEAD of the checkout at path is the branch,
// attached: the check every operation that acts on "whatever HEAD is on"
// makes first, since a merge, a reset or a rebase moves that branch, not the
// one the caller meant. head is the ref HEAD names, "" when it is detached or
// git could not say, for the message a refusal gives. A HEAD that cannot be
// read is not on the branch.
func OnBranch(path, branch string) (head string, on bool) {
	head, err := git.Run(path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", false
	}
	return head, head == "refs/heads/"+branch
}
