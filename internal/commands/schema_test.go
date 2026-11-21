package commands

import (
	"testing"

	"github.com/anlindstrom/wt/schema"
)

// Every command that speaks JSON has its schema. That each one compiles is
// testkit's TestEveryShippedSchemaCompilesClosed.
func TestEveryCommandHasASchema(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"status", "up", "sweep", "sweep-plan", "recovery", "restore-plan", "restore", "sync", "sync-run", "new-plan", "new", "checkout-plan", "checkout", "remove-plan", "remove", "refs-sweep-plan", "refs-sweep", "refs-restore-plan", "refs-restore", "refs-purge-plan", "refs-purge", "refs-swept", "session-list", "session-send", "session-stop", "quarantine-purge-plan", "quarantine-purge"} {
		if _, ok := schema.Get(name); !ok {
			t.Errorf("no %s schema", name)
		}
	}
}
