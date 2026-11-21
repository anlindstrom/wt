package provisiontest

import (
	"bytes"
	"io"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// Locatable builds a repository with one worktree per spec, made with
// wt new --no-setup, and returns its context.
func Locatable(t *testing.T, specs ...string) *kit.Context {
	t.Helper()
	ctx, err := kit.Open(testkit.CommittedRepo(t, testkit.MinimalConf), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, spec := range specs {
		if _, err := provision.New(ctx, spec, provision.NewOptions{NoSetup: true}, &buf); err != nil {
			t.Fatalf("New(%q): %v", spec, err)
		}
	}
	return ctx
}
