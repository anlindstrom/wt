package synccmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

const (
	sleepyCheck = "sleep 0.05 && test ! -e broken.marker"
	sleepyStep  = "sleep 0.05 && ./gen.sh"
	flakyCheck  = "flaky-check"
)

// sleepyFold is the fold case with a short sleep in the check and in the
// per-layer step, so each takes a measurable moment, and a second check that
// fails once and passes on its retry. flakyMarker is the file that makes it
// fail only the first time.
func sleepyFold(t *testing.T) (yamlRun string) {
	t.Helper()
	flakyMarker := filepath.Join(t.TempDir(), "flaky")
	yaml := strings.NewReplacer(
		"run: ./gen.sh", "run: "+sleepyStep,
		"run: test ! -e broken.marker", "run: "+sleepyCheck,
	).Replace(gittest.FoldYAML("max-plus-patch"))
	return yaml + "  - run: \"test -e " + flakyMarker + " || { touch " + flakyMarker + "; exit 1; }  # " + flakyCheck + "\"\n    retry: sleep 0.05\n"
}

// The issue's shape: a stack with a check at every commit and a step at every
// layer. Each check and step prints how long it took; the end of the run sums
// them up per check and step and names the slowest; --json carries the same.
func TestARunSaysHowLongEachCheckAndStepTookAndSumsThemUp(t *testing.T) {
	skipShort(t)
	synccmdtest.FakeDocker(t)
	ctx, s := synccmdtest.FoldLine(t, sleepyFold(t))
	testkit.GitIn(t, s.Paths["f3"], "checkout", "--", ".")
	main := ctx.Repo.MainRoot
	before := map[string]string{}
	for _, b := range append([]string{gittest.FoldTmp}, s.Layers...) {
		before[b] = testkit.GitOut(t, main, "rev-parse", b)
	}

	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, synccmdtest.NoAgents())
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.FoldTmp)
	if p.Result != syncrun.ResultRebased {
		t.Fatalf("%s %v\n%s", p.Result, p.Reason, human)
	}
	moved := false
	for b, tip := range before {
		moved = moved || testkit.GitOut(t, main, "rev-parse", b) != tip
	}
	if !moved {
		t.Fatalf("nothing moved\n%s", human)
	}

	// A passed check ends with its duration, under its ▸ line.
	// The line names the check by the base name of its command's first word.
	passed := regexp.MustCompile(`(?m)^  ▸ check \d+/\d+ [0-9a-f]{7} .*\n(  ✓ [^\n]*\n)*  ✓ sleep  \d+[hms][\dhms]*\n`)
	if !passed.MatchString(human) {
		t.Errorf("no passed check ends with its duration:\n%s", human)
	}
	// The other check at that commit is told apart by its own name.
	if !regexp.MustCompile(`(?m)^  ✓ test  \d+[hms][\dhms]*$`).MatchString(human) {
		t.Errorf("the second check's pass is not named:\n%s", human)
	}
	// A recheck prints its result and its own duration too.
	if !regexp.MustCompile(`(?m)^  ▸ recheck [0-9a-f]{7}\n(  ✓ [^\n]*\n)+`).MatchString(human) {
		t.Errorf("no recheck prints its result:\n%s", human)
	}
	// A step's result line ends with its duration.
	if !regexp.MustCompile(`(?m)^    ✓ folded into [0-9a-f]{7} .*  \d+[hms][\dhms]*$`).MatchString(human) {
		t.Errorf("no step result line ends with its duration:\n%s", human)
	}

	// The summary: the run's time, a row per check and step, the slowest.
	i := strings.Index(human, "⏱ the run took ")
	if i < 0 {
		t.Fatalf("no summary:\n%s", human)
	}
	summary := human[i:]
	rows := map[string]syncrun.TimingRunJSON{}
	for _, r := range p.Timings.Runs {
		rows[r.Kind+" "+r.Run] = r
	}
	check, step := rows["check "+sleepyCheck], rows["step "+sleepyStep]
	if want := p.Checked + p.Rechecked; check.Count != want || p.Rechecked == 0 {
		t.Errorf("the check ran %d times, want the %d commits checked plus %d rechecked (rechecks must be timed too): %+v", check.Count, p.Checked, p.Rechecked, p.Timings.Runs)
	}
	ran := 0
	for _, st := range p.LayerSteps {
		if st.Ran {
			ran++
			if st.ElapsedMs < 50 {
				t.Errorf("layer step %+v: elapsedMs under the sleep", st)
			}
		}
	}
	if step.Count != ran || ran == 0 {
		t.Errorf("the step ran %d times in the summary, %d in layerSteps", step.Count, ran)
	}
	for _, r := range []syncrun.TimingRunJSON{check, step} {
		if r.TotalMs < int64(r.Count)*50 {
			t.Errorf("%s %q: %d runs total %d ms, each slept 50", r.Kind, r.Run, r.Count, r.TotalMs)
		}
		line := "  " + r.Kind + "  " + r.Run + "  ×" + strconv.Itoa(r.Count) + "  "
		if !strings.Contains(summary, line) {
			t.Errorf("the summary lacks %q:\n%s", line, summary)
		}
	}
	if p.Timings.WallMs < check.TotalMs+step.TotalMs {
		t.Errorf("wall %d ms is under the checks and steps it ran one after the other: %d + %d", p.Timings.WallMs, check.TotalMs, step.TotalMs)
	}
	if len(p.Timings.Slowest) != 3 {
		t.Fatalf("slowest %+v, want three", p.Timings.Slowest)
	}
	for i, sl := range p.Timings.Slowest {
		if sl.Ms < 50 || sl.At == "" || (i > 0 && sl.Ms > p.Timings.Slowest[i-1].Ms) {
			t.Errorf("slowest %+v: not three runs, slowest first, each with where", p.Timings.Slowest)
		}
	}
	if !strings.Contains(summary, "slowest") || !strings.Contains(summary, p.Timings.Slowest[0].At) {
		t.Errorf("the summary does not name the slowest run:\n%s", summary)
	}
}

// A check that fails once and passes on its retry is one timed run, with both
// tries in its time, and the line that says it passed on retry carries it.
func TestACheckWithARetryIsTimedWithBothTries(t *testing.T) {
	skipShort(t)
	synccmdtest.FakeDocker(t)
	ctx, s := synccmdtest.FoldLine(t, sleepyFold(t))
	testkit.GitIn(t, s.Paths["f3"], "checkout", "--", ".")
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, synccmdtest.NoAgents())
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.FoldTmp)
	var flaky *syncrun.TimingRunJSON
	for i, r := range p.Timings.Runs {
		if strings.Contains(r.Run, flakyCheck) {
			flaky = &p.Timings.Runs[i]
		}
	}
	if flaky == nil || flaky.Kind != "check" || flaky.TotalMs < 50 {
		t.Fatalf("the check that needed its retry is not in the timings, or is too quick: %+v", p.Timings.Runs)
	}
	if !regexp.MustCompile(`(?m)^  ✓ .*` + flakyCheck + `.* passed on retry  \d+[hms][\dhms]*$`).MatchString(human) {
		t.Errorf("the retry's line has no duration:\n%s", human)
	}
}

// A run with no check and no step has nothing to time: no summary.
func TestARunWithNoChecksAndNoStepsPrintsNoSummary(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := synccmdtest.StackLine(t, "")
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("stopped: %v\n%s", err, human)
	}
	if strings.Contains(human, "⏱") {
		t.Errorf("a summary for a run that timed nothing:\n%s", human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); len(p.Timings.Runs) != 0 || len(p.Timings.Slowest) != 0 {
		t.Errorf("timings %+v, want none", p.Timings)
	}
}

// A signal ends the process without running a deferred summary, so the JSON
// the handler writes must hold what the run had timed by then: a run caught
// in a slow step has run its checks, and must not say it ran nothing.
func TestAnInterruptedRunKeepsWhatItTimedInItsJSON(t *testing.T) {
	// Not parallel: it interrupts every git process group in the test binary.
	skipShort(t)
	ctx, _ := provisiontest.RunFixture(t, false)
	main := ctx.Repo.MainRoot
	started := filepath.Join(t.TempDir(), "started")
	yaml, err := os.ReadFile(filepath.Join(main, ".wt-sync.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, main, ".wt-sync.yaml", string(yaml)+"each_commit:\n  - run: sleep 0.05\n    paths: [v.txt]\n"+
		"defer:\n  - run: touch "+started+"; sleep 30\n    paths: [v.txt]\n")
	testkit.GitIn(t, main, "commit", "-q", "-am", "declare a check and a slow step")
	testkit.GitIn(t, main, "fetch", "-q", "origin")

	var out, human testkit.LockedBuffer
	opts := synccmdtest.NoAgents()
	j := syncrun.NewSyncRunJournal(&out, "sync rebase")
	opts.Journal = j
	go func() { _ = synccmd.SyncRebase(ctx, []string{"bump"}, opts, &human) }()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the step never started:\n%s", human.String())
		}
	}
	before := gittest.Parked()
	j.Interrupted("bump", "interrupted")
	n := git.Interrupt()
	gittest.WaitParked(t, before, n)
	p := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, []byte(out.String())), "feat_wt/bump")
	if p.Result != syncrun.ResultInterrupted {
		t.Fatalf("result %s", p.Result)
	}
	if len(p.Timings.Runs) != 1 || p.Timings.Runs[0].Kind != "check" || p.Timings.Runs[0].Run != "sleep 0.05" || p.Timings.Runs[0].TotalMs < 50 {
		t.Errorf("timings %+v, want the check the run had finished", p.Timings)
	}
}
