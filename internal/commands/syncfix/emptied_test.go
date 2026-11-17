package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// A run handed over at a conflict a person resolved to trunk's side, and
// resumed, names the commit its pick left empty, in the summary and --json.
func TestSyncResumeNamesTheCommitAPersonsResolutionEmptied(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	ctx, bump := provisiontest.ContestedFixture(t)
	main := ctx.Repo.MainRoot
	keep := "conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: keep-trunk\n"
	if err := os.WriteFile(filepath.Join(main, ".wt-sync.yaml"), []byte(keep), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitOut(t, main, "commit", "-q", "-am", "keep trunk's version")
	testkit.GitOut(t, main, "fetch", "-q", "origin")
	synccmdtest.HandOverNow(t, ctx, bump)
	testkit.WriteFile(t, bump, "a.txt", "trunk\n")
	testkit.GitOut(t, bump, "add", "--", "a.txt")
	var out, human bytes.Buffer
	opts := noResumeAgents()
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	if err := SyncResume(ctx, "bump", opts, &human); err != nil {
		t.Fatalf("%v\n%s", err, human.String())
	}
	if !strings.Contains(human.String(), "dropped as empty: ") || !strings.Contains(human.String(), " bump ") {
		t.Fatalf("the summary does not name the bump:\n%s", human.String())
	}
	p := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, out.Bytes()), "feat_wt/bump")
	if len(p.Emptied) != 1 || p.Emptied[0].Subject != "bump" || p.TestsOnly == nil || len(p.TestsOnly) != 0 {
		t.Fatalf("emptied %+v testsOnly %+v", p.Emptied, p.TestsOnly)
	}
}
