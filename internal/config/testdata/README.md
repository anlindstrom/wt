Seven worktree.conf files, as repositories wrote them, used as parser and
validator conformance fixtures.

AWS_SETUP_ENABLED, REPO_NAME and WORKTREE_LAYOUT lines are stripped: all three
are retired keys that the validator now rejects by design. The fixtures show the
files as they stand after the move off those keys.
