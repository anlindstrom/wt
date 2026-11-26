package admin

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/reposel"
)

func snapshotDir(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	snap := map[string]time.Time{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snap[rel] = info.ModTime()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func ageDirTree(t *testing.T, root string, ts time.Time) {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return os.Chtimes(path, ts, ts)
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chtimes(dirs[i], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
}

// Doctor must write nothing inside .git, even transiently, both when no PR cache
// exists yet and when one already exists.
func TestDoctorWritesNothingInGitDir(t *testing.T) {
	t.Parallel()
	for _, withCache := range []bool{false, true} {
		name := "no cache"
		if withCache {
			name = "existing cache"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			main := testkit.CommittedRepo(t, testkit.MinimalConf)
			ctx, err := kit.Open(main, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			testkit.FakeGitHub(t, ctx)

			gitDir, err := ctx.Repo.GitDir()
			if err != nil {
				t.Fatal(err)
			}
			if withCache {
				if err := os.WriteFile(kit.PRCachePath(ctx), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			past := time.Now().Add(-10 * time.Second).Truncate(time.Second)
			ageDirTree(t, gitDir, past)
			before := snapshotDir(t, gitDir)

			var out bytes.Buffer
			if _, err := Doctor(ctx, &out); err != nil {
				t.Fatal(err)
			}

			after := snapshotDir(t, gitDir)
			for rel, mod := range after {
				old, ok := before[rel]
				if !ok {
					t.Errorf("doctor created %s inside .git", rel)
				} else if !old.Equal(mod) {
					t.Errorf("doctor modified %s inside .git (mtime %v -> %v)", rel, old, mod)
				}
			}
			for rel := range before {
				if _, ok := after[rel]; !ok {
					t.Errorf("doctor removed %s from .git", rel)
				}
			}
		})
	}
}

// A malformed .wt-sync.yaml on trunk is a problem in DoctorAll, never skipped
// as though sync were not declared.
func TestDoctorAllReportsMalformedSyncDeclarationAsProblem(t *testing.T) {
	// Not parallel: sets WT_ROOTS.
	root := testkit.RealTempDir(t)
	gittest.WithOrigin(t, testkit.ManagedRepo(t, root, "plain"))
	bad := testkit.ManagedRepo(t, root, "bad-sync")
	if err := os.WriteFile(filepath.Join(bad, ".wt-sync.yaml"), []byte("not_a_key: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, bad, "add", "-A")
	gittest.Git(t, bad, "commit", "-q", "-m", "bad declaration")
	gittest.WithOrigin(t, bad)

	u := &config.User{Roots: []config.NamedRoot{{Name: "r", Path: root}}}
	t.Setenv("WT_ROOTS", "")

	var buf bytes.Buffer
	err := DoctorAll(u, reposel.Selection{All: true}, &buf)
	if err == nil {
		t.Fatalf("DoctorAll succeeded on malformed .wt-sync.yaml:\n%s", buf.String())
	}
	out := buf.String()
	_, under, found := strings.Cut(out, "bad-sync")
	if !found || !strings.HasPrefix(strings.TrimSpace(under), "1 problem(s)") ||
		!strings.Contains(under, "\n  ! sync: run is blocked by: declaration\n") {
		t.Fatalf("expected bad-sync's declaration as its one problem in:\n%s", out)
	}
}
