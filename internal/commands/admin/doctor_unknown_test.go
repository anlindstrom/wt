package admin

import (
	"io"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/superset"
)

// A Superset that does not answer in time is unknown, even where the
// repository asked for it and the text form counts a problem.
func TestDoctorRecordsAnUnansweredSupersetAsUnknown(t *testing.T) {
	t.Parallel()
	ctx, err := kit.Open(testkit.CommittedRepo(t, testkit.MinimalConf+"SUPERSET_REGISTER=on\n"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	testkit.OptIn(ctx)
	ctx.SupersetFake = &superset.Status{Exe: "/fake/superset", Err: git.NoAnswer("superset status did not answer within 10s")}

	f := kit.NewFindings(io.Discard)
	if err := DoctorFindings(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f.Problems() != 1 {
		t.Errorf("problems = %d, want the text form's one", f.Problems())
	}
	for _, c := range f.Records() {
		if c.ID == "superset.host" {
			if c.Status != kit.CheckUnknown {
				t.Errorf("superset.host = %s, want unknown: %s", c.Status, c.Detail)
			}
			return
		}
	}
	t.Errorf("no superset.host record in %+v", f.Records())
}
