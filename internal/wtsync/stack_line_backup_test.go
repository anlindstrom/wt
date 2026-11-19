package wtsync

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A backup is recognised by the matcher wt refs sweep uses, where * crosses
// a slash: a dated backup under backup/ and a branch ending in -backup under
// another folder are listed, never taken as layers.
func TestReadLineSkipsBackupsTheSweepMatches(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "backup/2026-11-11/x", s.Tips["s3"])
	gitIn(t, s.Dir, "branch", "feat/x-backup", s.Tips["s4"])
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	var layers, skipped []string
	for _, l := range line.Layers {
		layers = append(layers, l.Branch)
	}
	for _, k := range line.Skipped {
		skipped = append(skipped, k.Branch)
	}
	if got := strings.Join(layers, " "); got != "s1 s2 s3 s4 s5" {
		t.Errorf("layers %q, want the five real ones", got)
	}
	if got := strings.Join(skipped, " "); got != "backup/2026-11-11/x feat/x-backup" {
		t.Errorf("skipped %q, want both backups", got)
	}
}

// Every name wt treats as a backup is left alone on the line: a deliberate
// backup, kept, and a transient safety copy, swept.
func TestReadLineSkipsKeptAndTransientBackups(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "backup/2026-11-01/huge-redo", s.Tips["s2"])
	gitIn(t, s.Dir, "branch", "safety/x", s.Tips["s3"])
	gitIn(t, s.Dir, "branch", "safe/pre-rebase-abc", s.Tips["s4"])
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, config.DefaultUser().BackupPatterns())
	if err != nil {
		t.Fatal(err)
	}
	var skipped []string
	for _, k := range line.Skipped {
		skipped = append(skipped, k.Branch)
	}
	if got := strings.Join(skipped, " "); got != "backup/2026-11-01/huge-redo safe/pre-rebase-abc safety/x" {
		t.Errorf("skipped %q, want all three", got)
	}
	if len(line.Layers) != 5 {
		t.Errorf("layers %d, want the five real ones", len(line.Layers))
	}
}
