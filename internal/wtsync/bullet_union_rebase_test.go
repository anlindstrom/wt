package wtsync

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/repo"
)

const bulletYAML = "conflicts:\n  - paths: [README.md]\n    strategy: bullet-union\n"

// bulletLine is the issue's shape: README's Prerequisites list gets an item
// on trunk, and another on the first layer of a line of two layers, with
// bullet-union declared for README.md. The line is feature, with a layer
// branch l1 under it; the worktree of feature is returned.
func bulletLine(t *testing.T, branchItem string) (dir, wt string, cfg *Config) {
	t.Helper()
	base := readme("- Java 21", "- Docker")
	dir = repoWith(t, map[string]string{"README.md": base, ".wt-sync.yaml": bulletYAML},
		[]map[string]string{{"README.md": readme("- Java 21", "- Docker", "- Node 22")}},
		[]map[string]string{{"README.md": readme("- Java 21", "- Docker", branchItem)}, {"api.txt": "api\n"}})
	gitIn(t, dir, "branch", "l1", "feature~1")
	gitIn(t, dir, "remote", "add", "origin", dir)
	gitIn(t, dir, "fetch", "-q", "origin")
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	var err error
	if cfg, err = LoadFromTrunk(dir, "main"); err != nil {
		t.Fatal(err)
	}
	return dir, featureWorktree(t, dir).Path, cfg
}

func TestBulletUnionIsDeclaredLikeTheOtherStrategies(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(bulletYAML))
	if err != nil {
		t.Fatal(err)
	}
	s, err := FromRule(cfg.Conflicts[0], "", "")
	if err != nil || s.Name() != "bullet-union" {
		t.Fatalf("strategy %v, %v", s, err)
	}
}

// The look calls a branch whose only stop bullet-union resolves a recipe.
func TestBulletUnionMakesTheLookCallItARecipe(t *testing.T) {
	t.Parallel()
	dir, wt, cfg := bulletLine(t, "- Gradle 9")
	a := Assess(dir, "origin/main", cfg, repo.Worktree{Path: wt, Branch: "feature"}, nil)
	if a.Class != Recipe || len(a.Files) != 1 || !a.Files[0].Resolved || a.Files[0].Strategy != "bullet-union" {
		t.Errorf("assessment: %+v", a)
	}
}

// The line finishes with both items, trunk's first; undo puts the line, the
// layer and the worktree back.
func TestBulletUnionFinishesAStackRunAndUndoRestoresIt(t *testing.T) {
	t.Parallel()
	dir, wt, cfg := bulletLine(t, "- Gradle 9")
	oldTip, oldLayer := gitIn(t, dir, "rev-parse", "feature"), gitIn(t, dir, "rev-parse", "l1")
	line, err := ReadLine(dir, "feature", "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Path: wt, Branch: "feature", Trunk: "origin/main", Onto: "origin/main", Epoch: 9, Stack: &StackRequest{Line: line}}
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || res.Restored || len(res.Stops) != 1 || !res.Stops[0].Files[0].Resolved {
		t.Fatalf("%v %+v\n%s", err, res, log.String())
	}
	got, _ := os.ReadFile(filepath.Join(wt, "README.md"))
	if want := readme("- Java 21", "- Docker", "- Node 22", "- Gradle 9"); string(got) != want {
		t.Errorf("README.md:\n%s\nwant:\n%s", got, want)
	}
	if err := WriteResult(dir, "feature", res.NewTip, 9); err != nil {
		t.Fatal(err)
	}
	// Move the layer the way a run does, and see it reach its rewritten tip.
	tips, err := CheckLayers(dir, res.NewTip, "origin/main", res.Layers)
	if err != nil {
		t.Fatal(err)
	}
	rec := &StackRecord{Epoch: 9, Work: "feature", Branch: "feature", Path: wt, Onto: "origin/main", Line: line, Plans: res.Layers}
	if err := MoveLayers(dir, rec, tips, moveOpts(t)); err != nil {
		t.Fatal(err)
	}
	moved := gitIn(t, dir, "rev-parse", "l1")
	if moved == oldLayer || len(tips) != 1 || moved != tips[0].New {
		t.Fatalf("l1 is at %s, want its rewritten tip %v (was %s)", moved, tips, oldLayer)
	}
	if _, err := Undo(dir, worktreesOf(t, dir), nil, "feature", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "rev-parse", "feature") != oldTip || gitIn(t, dir, "rev-parse", "l1") != oldLayer {
		t.Error("the line or its layer is not back")
	}
	if gitIn(t, wt, "rev-parse", "HEAD") != oldTip || gitIn(t, wt, "status", "--porcelain") != "" {
		t.Error("the worktree is not back")
	}
}

// A branch that rewords an item stops where it stopped without the strategy,
// and nothing is lost.
func TestBulletUnionRefusalStopsAndLosesNothing(t *testing.T) {
	t.Parallel()
	dir, wt, cfg := bulletLine(t, "- Gradle 9")
	// Reword an existing item on the branch instead of adding one.
	gitIn(t, wt, "checkout", "-q", "-B", "feature", "l1")
	reword := readme("- Java 21", "- Docker 27")
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte(reword), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "commit", "-q", "-am", "reword")
	old := gitIn(t, dir, "rev-parse", "feature")
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 8), &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	// The first commit's stop is resolved; the reword's is not.
	if n := len(res.Stops); n != 2 || !res.Stops[0].Files[0].Resolved || res.Stops[1].Files[0].Resolved {
		t.Fatalf("stops: %+v", res.Stops)
	}
	if ok, _ := RebaseInProgress(wt); !ok {
		t.Error("the stop is not left for a person")
	}
	if gitIn(t, dir, "rev-parse", "refs/wt-sync/feature/8") != old {
		t.Error("safety ref does not pin the old tip")
	}
}
