package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A layer that moved after the line was read is refused before anything is
// replayed: the scratch would otherwise hold another line than the look shows.
func TestTheScratchRefusesALineThatMoved(t *testing.T) {
	t.Parallel()
	source := gittest.NewRepo(t, t.TempDir(), "source")
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")
	read := gittest.Git(t, source, "rev-parse", "HEAD")
	gittest.Git(t, source, "branch", "layer-a")
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "more")
	head := gittest.Git(t, source, "rev-parse", "HEAD")
	gittest.Git(t, source, "branch", "-f", "layer-a", head)
	r := Request{Branch: "main", Stack: &StackRequest{Line: Line{Branch: "main", Head: head, Layers: []LineLayer{{Branch: "layer-a", Tip: read}}}}}
	refusal, err := buildScratch(source, filepath.Join(t.TempDir(), "repo"), r)
	if err != nil || !strings.Contains(refusal, "layer-a moved since the line was read") {
		t.Fatalf("refusal %q, err %v", refusal, err)
	}
}
