package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

func TestSyncOwnerUsageMistakes(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sync", "owner", "a.go:3", "--json"}, "<path>:<from>-<to>"},
		{[]string{"sync", "owner", "a.go:5-3"}, "<path>:<from>-<to>"},
	} {
		out, err := runStreams(c.args...)
		var u usageError
		if !errors.As(err, &u) || !strings.Contains(err.Error(), c.want) || strings.HasPrefix(out, "{") {
			t.Errorf("%v: err %v, stdout %q, want a usage mistake saying %q", c.args, err, out, c.want)
		}
	}
}

// A run outside any repository still answers --json with an object, and a
// colon that no line range follows is part of the file's name.
func TestSyncOwnerAndFixupRefuseInJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, args := range map[string][]string{
		"sync-owner": {"sync", "owner", "w", "a:b.go", "--json"},
		"sync-fixup": {"sync", "fixup", "HEAD", "--json"},
	} {
		out, err := runStreams(args...)
		if err == nil || errorStatus(err) != 1 {
			t.Errorf("%v: err %v, want exit 1", args, err)
		}
		testkit.ValidateJSON(t, name, []byte(out))
	}
}

// --into without a range asks about every line of the file.
func TestSyncOwnerIntoAWholeFile(t *testing.T) {
	_, s := synccmdtest.StackLine(t, "")
	t.Chdir(s.Paths[gittest.StackTmp])
	out, err := runStreams("sync", "owner", "b1.txt", "--into", "s2", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	testkit.ValidateJSON(t, "sync-owner", []byte(out))
	if !strings.Contains(out, `"lines": {`) || !strings.Contains(out, `"verdict"`) {
		t.Errorf("%s", out)
	}
}
