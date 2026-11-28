package syncfix

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A resume of a fold in place asks for no need of a path only trunk changed.
func TestSyncResumeOfAnInPlaceFoldNeedsNothingOfPathsOnlyTrunkChanged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	var tmp string
	ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
		commit := func(file, content, msg string) {
			gittest.WriteFile(t, filepath.Join(main, file), content)
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		commit(".wt-sync.yaml", "conflicts: []\ndefer:\n  - run: 'true'\n    paths: [trunk.txt]\n    needs: [docker]\n", "base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		commit("a.txt", "one\n", "feat: a")
		gittest.Git(t, main, "branch", "low1")
		commit("a.txt", "two\n", "feat: b")
		commit("a.txt", "three\n", "fixup! feat: a")
		gittest.Git(t, main, "checkout", "-q", "main")
		commit("trunk.txt", "trunk\n", "feat: trunk's own")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_low")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, "tmp/low")
	})
	docker := synccmdtest.DockerDown()
	opts := synccmdtest.NoAgents()
	opts.Probes = docker.Probes()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the fixup's conflict handed over\n%s", err, human)
	}
	gittest.WriteFile(t, filepath.Join(tmp, "a.txt"), "three\n")
	gittest.Git(t, tmp, "add", "a.txt")
	resume := noResumeAgents()
	resume.Push = syncrun.PushNever
	resume.Probes = docker.Probes()
	// The resume goes on to the next conflict, past its probe.
	res, err := syncResumeJSON(t, ctx, "tmp/low", resume)
	if err == nil || !strings.Contains(err.Error(), "needs you") || synccmdtest.Participant(t, res, "tmp/low").Result != syncrun.ResultHandedOver {
		t.Fatalf("the resume did not go on to the next stop: %v\n%+v", err, res)
	}
	if docker.Calls() != 0 {
		t.Errorf("docker was probed %d times", docker.Calls())
	}
}
