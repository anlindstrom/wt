package syncrun_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/jsonout"
)

// sha is a full commit id made of one character.
func sha(c string) string { return strings.Repeat(c, 40) }

// everyRow is a --stack participant with a row of every kind the summary
// tells apart: the branch name says whether it is kept.
func everyRow(p *syncrun.SyncParticipant) {
	s := jsonout.Strp
	layer := func(branch, state string, edit func(l *syncrun.StackLayer)) syncrun.StackLayer {
		l := syncrun.StackLayer{Branch: branch, OldTip: sha("a"), Commits: 1, Subject: "s", FoundBy: syncrun.LayerFoundTip, State: state}
		if edit != nil {
			edit(&l)
		}
		return l
	}
	result := func(r string) func(*syncrun.StackLayer) {
		return func(l *syncrun.StackLayer) { l.Result = s(r) }
	}
	side := func(branch string, edit func(x *syncrun.StackSide)) syncrun.StackSide {
		x := syncrun.StackSide{Branch: branch, Kind: syncrun.SideForked, OldTip: sha("b"), Fork: sha("c"), ForkSubject: "f", Base: sha("c"),
			State: "none", SameTip: []string{}}
		edit(&x)
		return x
	}
	pushed := func(branch, outcome string) syncrun.StackPushed {
		return syncrun.StackPushed{Branch: branch, Remote: "origin", RemoteBranch: branch, New: sha("d"), Outcome: outcome}
	}
	p.Stack = &syncrun.Stack{Branch: "top", Base: sha("e"), Head: sha("f"), Skipped: []syncrun.StackSkipped{},
		ContextGaps: []syncrun.StackContextGap{}, ContextUnchecked: []string{},
		Top: &syncrun.StackTop{Branch: "top", OldTip: sha("f"), Commits: 1, Subject: "s"},
		Layers: []syncrun.StackLayer{
			layer("keep-held", "dirty", result("held")),
			layer("keep-recovery", "none", result("needsRecovery")),
			layer("keep-moving", "none", result("moving")),
			layer("keep-planned", "none", result("planned")),
			layer("keep-nopush", "none", func(l *syncrun.StackLayer) { l.Result, l.NoPushReason = s("moved"), s("no remote branch") }),
			layer("keep-note", "clean", func(l *syncrun.StackLayer) { l.Result, l.Note = s("moved"), s("checked out") }),
			layer("keep-command", "none", func(l *syncrun.StackLayer) { l.Result, l.MoveCommand = s("unchanged"), s("git branch -f") }),
			layer("keep-look-held", "busy", nil),
			layer("drop-moved", "clean", func(l *syncrun.StackLayer) { l.Result, l.Patch = s("moved"), s("changed") }),
			layer("drop-unchanged", "none", func(l *syncrun.StackLayer) { l.Result, l.Patch = s("unchanged"), s("same") }),
			layer("drop-look-clean", "clean", nil),
			layer("drop-look-none", "none", nil),
		},
		SideBranches: []syncrun.StackSide{
			side("keep-side-held", func(x *syncrun.StackSide) { x.Result, x.Held = s("held"), s("dirty") }),
			side("keep-side-left", func(x *syncrun.StackSide) { x.Left = s("a merge commit") }),
			side("keep-side-command", func(x *syncrun.StackSide) { x.Result, x.MoveCommand = s("unchanged"), s("git rebase") }),
			side("drop-side-moved", func(x *syncrun.StackSide) { x.Result, x.Carry = s("moved"), true }),
			side("drop-side-backup", func(x *syncrun.StackSide) { x.Backup, x.Result, x.Left = true, s("held"), s("a backup") }),
		},
		Push: &syncrun.StackPush{Conflicted: []syncrun.StackConflicted{},
			Branches: []syncrun.StackPushed{pushed("drop-pushed", "pushed"), pushed("keep-lease", "leaseFailed"), pushed("keep-unsent", "notSent")}},
	}
	step := func(branch, result string) syncrun.LayerStepJSON {
		return syncrun.LayerStepJSON{Layer: 1, Layers: 1, Branch: branch, Step: "./gen", Result: result}
	}
	p.LayerSteps = []syncrun.LayerStepJSON{step("drop-folded", "folded"), step("drop-committed", "committed"),
		step("drop-unchanged-step", "unchanged"), step("drop-untouched", "untouched"),
		step("keep-owed", "owed"), step("keep-failed", "failed"), step("drop-skipped", "skipped")}
	p.Timings = syncrun.TimingsJSON{WallMs: 5, Runs: []syncrun.TimingRunJSON{{Kind: "check", Run: "./c", Count: 1, TotalMs: 3}},
		Slowest: []syncrun.TimingSlowJSON{{Kind: "check", Run: "./c", At: "x", Ms: 3}}}
}

// journalled is the object the journal writes after record, checked against
// the schema.
func journalled(t *testing.T, summary bool, record func(j *syncrun.RunJournal)) syncrun.RunResult {
	t.Helper()
	var out bytes.Buffer
	j := syncrun.NewSyncRunJournal(&out, "sync resume")
	if summary {
		j.Summarize()
	}
	record(j)
	testkit.ValidateJSON(t, "sync-run", out.Bytes())
	if got := bytes.Count(out.Bytes(), []byte("\n")); got != 1 {
		t.Errorf("the object is %d lines, want it compact on one", got)
	}
	var r syncrun.RunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return r
}

// branches is every row's branch in r's one participant, by array.
func branches(p *syncrun.SyncParticipant) map[string][]string {
	m := map[string][]string{}
	for _, l := range p.Stack.Layers {
		m["layers"] = append(m["layers"], l.Branch)
	}
	for _, x := range p.Stack.SideBranches {
		m["sides"] = append(m["sides"], x.Branch)
	}
	for _, b := range p.Stack.Push.Branches {
		m["push"] = append(m["push"], b.Branch)
	}
	for _, x := range p.LayerSteps {
		m["steps"] = append(m["steps"], x.Branch)
	}
	return m
}

func TestTheSummaryKeepsTheRowsThatNeedAttentionAndCountsEveryRow(t *testing.T) {
	t.Parallel()
	record := func(j *syncrun.RunJournal) {
		j.Join("top", "top", "/w/top", sha("f"), nil)
		j.SetSync("top", everyRow)
		j.Finish()
	}
	full, summary := journalled(t, false, record), journalled(t, true, record)
	if full.Detail != "full" || summary.Detail != "summary" || full.Schema != 2 || summary.Schema != 2 {
		t.Fatalf("detail %q/%q schema %d/%d", full.Detail, summary.Detail, full.Schema, summary.Schema)
	}
	f, s := full.Worktrees[0], summary.Worktrees[0]
	all, kept := branches(f), branches(s)
	for array, rows := range all {
		var want []string
		for _, b := range rows {
			if strings.HasPrefix(b, "keep-") {
				want = append(want, b)
			}
		}
		if len(want) == len(rows) {
			t.Fatalf("%s: the full form has no row to leave out: %v", array, rows)
		}
		if !slices.Equal(kept[array], want) {
			t.Errorf("%s: the summary holds %v, want %v", array, kept[array], want)
		}
	}
	if len(f.Timings.Slowest) != 1 || len(s.Timings.Slowest) != 0 || len(s.Timings.Runs) != 1 || s.Timings.WallMs != 5 {
		t.Errorf("timings full %+v summary %+v", f.Timings, s.Timings)
	}
	tally := syncrun.StackCounts{Layers: len(f.Stack.Layers), SideBranches: len(f.Stack.SideBranches),
		LayerResults: map[string]int{}, SideResults: map[string]int{}, LayerStepResults: map[string]int{}}
	for _, l := range f.Stack.Layers {
		if l.Result != nil {
			tally.LayerResults[*l.Result]++
		}
		if l.Patch != nil && *l.Patch == "changed" {
			tally.PatchChanged++
		}
	}
	for _, x := range f.Stack.SideBranches {
		if x.Result != nil {
			tally.SideResults[*x.Result]++
		}
	}
	for _, b := range f.Stack.Push.Branches {
		if b.Outcome == syncrun.PushedOK {
			tally.Pushed++
		}
	}
	for _, x := range f.LayerSteps {
		tally.LayerStepResults[x.Result]++
	}
	want, _ := json.Marshal(tally)
	for form, p := range map[string]*syncrun.SyncParticipant{"full": f, "summary": s} {
		if got, _ := json.Marshal(p.Stack.Counts); string(got) != string(want) {
			t.Errorf("%s counts\n got %s\nwant %s", form, got, want)
		}
	}
}

// A signal's object keeps, in the summary, the rows of the moves it caught.
// One that failed before it began keeps its outcome and why.
func TestASummaryKeepsTheMovesASignalCaughtAndAnEarlyFailure(t *testing.T) {
	t.Parallel()
	r := journalled(t, true, func(j *syncrun.RunJournal) {
		j.Join("top", "top", "/w/top", sha("f"), nil)
		j.SetSync("top", everyRow)
		j.Interrupted("top", "")
	})
	if got := branches(r.Worktrees[0])["layers"]; !slices.Contains(got, "keep-moving") || !slices.Contains(got, "keep-planned") {
		t.Errorf("interrupted: the layers caught are gone: %v", got)
	}
	r = journalled(t, true, func(j *syncrun.RunJournal) { j.Fail(errors.New("not a repository")) })
	if r.Detail != "summary" || r.Outcome != jsonout.OutcomeRefused || r.Error == nil || *r.Error != "not a repository" || len(r.Worktrees) != 0 {
		t.Errorf("early failure: %+v", r)
	}
}

// wt up's object is not wt sync's: its version and shape stay.
func TestWtUpStaysOnItsFirstMajor(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	j := syncrun.NewRunJournal(&out)
	j.Finish()
	testkit.ValidateJSON(t, "up", out.Bytes())
	if !strings.Contains(out.String(), `"schema": 1`) || strings.Contains(out.String(), `"detail"`) {
		t.Errorf("wt up --json: %s", out.String())
	}
}
