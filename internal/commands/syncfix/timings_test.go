package syncfix

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

const slowCheck = `sleep 0.05 && ! grep -q 1.0.6 v.txt`

// A check that stops the run says how long it took; the resume that follows
// a fix prints a summary of its own, which says it covers only the resume,
// and a resume that stops again at the same check prints one too.
func TestAResumeSaysItsTimingsCoverTheResumeAndStopsPrintThem(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	ctx, bump := provisiontest.RunFixture(t, false)
	synccmdtest.DeclareSteps(t, ctx, "each_commit:\n  - run: \""+slowCheck+"\"\n    paths: [v.txt]\n")
	took := regexp.MustCompile(`(?m)^  ✗ ` + regexp.QuoteMeta(slowCheck) + `: exit 1.*  \d+[hms][\dhms]*$`)

	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"bump"}, synccmdtest.NoAgents(), &out); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, out.String())
	}
	if !took.MatchString(out.String()) {
		t.Errorf("the stop's line has no duration:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "⏱ the run took ") {
		t.Errorf("the run that stopped printed no summary:\n%s", out.String())
	}

	// No fix: the resume stops again, and still tells what it took.
	out.Reset()
	if err := SyncResume(ctx, "bump", noResumeAgents(), &out); err == nil {
		t.Fatalf("resume went on past a check that still fails:\n%s", out.String())
	}
	if !took.MatchString(out.String()) || !strings.Contains(out.String(), "⏱ this resume took ") {
		t.Errorf("the resume that stopped again has no duration or no summary:\n%s", out.String())
	}

	testkit.WriteFile(t, bump, "v.txt", "1.0.7\n")
	testkit.GitOut(t, bump, "commit", "-q", "-a", "--amend", "--no-edit")
	out.Reset()
	var js bytes.Buffer
	opts := noResumeAgents()
	opts.Journal = syncrun.NewSyncRunJournal(&js, "sync resume")
	if err := SyncResume(ctx, "bump", opts, &out); err != nil {
		t.Fatalf("resume: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "⏱ this resume took ") || !strings.Contains(out.String(), "covers only what the resume ran") {
		t.Errorf("the finished resume's summary does not say what it covers:\n%s", out.String())
	}
	p := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, js.Bytes()), "bump")
	if len(p.Timings.Runs) != 1 || p.Timings.Runs[0].Kind != "check" || p.Timings.Runs[0].Run != slowCheck || p.Timings.Runs[0].Count < 1 {
		t.Fatalf("the resume's timings %+v, want the check it ran", p.Timings)
	}
	if p.Timings.Runs[0].TotalMs < 50 {
		t.Errorf("the check slept 50 ms: %+v", p.Timings.Runs[0])
	}
}

// An edit resume refused for a stray change returns an error, but it ran
// checks first: its summary and its JSON timings say so.
func TestARefusedResumeStillTellsWhatItTimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCaseWith(t, false, logStep)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.WriteFile(t, c.tmp, "one.txt", "stray\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt", "one.txt")
	res, human, err := c.resume(t)
	if err == nil || !strings.Contains(human, "one.txt") {
		t.Fatalf("resume: %v, want a refusal naming one.txt\n%s", err, human)
	}
	if !strings.Contains(human, "⏱ this resume took ") {
		t.Errorf("the refused resume printed no summary:\n%s", human)
	}
	p := synccmdtest.Participant(t, res, gittest.EditTmp)
	if len(p.Timings.Runs) != 1 || p.Timings.Runs[0].Kind != "check" || p.Timings.Runs[0].Count < 1 {
		t.Errorf("timings %+v, want the checks the resume ran", p.Timings)
	}
}
