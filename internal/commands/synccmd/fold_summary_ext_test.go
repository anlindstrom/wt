package synccmd_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// wantSummary checks the --json fold counts of f against want, "analysis
// fixups appliesAtTarget aheadOfContext conflicts notAnalysed".
func wantSummary(t *testing.T, f *synccmd.FoldLook, want string) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range []string{"analysis", "fixups", "appliesAtTarget", "aheadOfContext", "conflicts", "notAnalysed"} {
		v, err := json.Marshal(m[k])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Trim(string(v), `"`))
	}
	if g := strings.Join(got, " "); g != want {
		t.Errorf("fold summary %q, want %q", g, want)
	}
}

// summaryBlock is the summary line of a look and the lines indented under it,
// "" when it has none.
func summaryBlock(look string) string {
	var block []string
	for _, l := range strings.Split(look, "\n") {
		switch {
		case strings.HasPrefix(l, "  fold: "):
			block = append(block, l)
		case len(block) > 0 && strings.HasPrefix(l, "    "):
			block = append(block, l)
		case len(block) > 0:
			return strings.Join(block, "\n")
		}
	}
	return strings.Join(block, "\n")
}

// id is the short id of the commit of tmp/low with subject.
func id(t *testing.T, ctx *kit.Context, subject string) string {
	t.Helper()
	for _, l := range strings.Split(testkit.GitOut(t, ctx.Repo.MainRoot, "log", "--format=%H %s", "tmp/low"), "\n") {
		if sha, s, _ := strings.Cut(l, " "); s == subject {
			return git.ShortID(sha, 7)
		}
	}
	t.Fatalf("no commit %q", subject)
	return ""
}

// put commits files, name and content in pairs, as msg.
func (g gitLine) put(msg string, files ...string) {
	for i := 0; i+1 < len(files); i += 2 {
		g.write(files[i], files[i+1])
	}
	g.commit(msg)
}

// twoHunkLine has one fixup of "feat: a" with a hunk in each of two files,
// both needing lines "feat: c" adds later.
func twoHunkLine(g gitLine) {
	g.put("feat: a", "y.txt", "y\na\n", "z.txt", "z\na\n")
	g.put("feat: c", "y.txt", "y\na\nc\n", "z.txt", "z\na\nc\n")
	g.put("fixup! feat: a", "y.txt", "y\na\nc\nf\n", "z.txt", "z\na\nc\nf\n")
}

// summaryLine is builtLine with a layer, low1, below what line makes.
func summaryLine(t *testing.T, yaml string, line lineStep) *kit.Context {
	t.Helper()
	return builtLine(t, yaml, func(g gitLine) {
		g.put("feat: low", "low.txt", "low\n")
		g.git("branch", "low1")
		line(g)
	}, nil)
}

// The look sums the fixups up by what the analysis finds of each, one count
// per fixup commit.
func TestFoldLookSumsUpWhatBecomesOfEachFixup(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, c := range map[string]struct {
		line lineStep
		json string
		// text is the summary block, {subject} standing for that commit's id.
		text []string
		gaps int
		// noNote says nothing is in contextUnchecked.
		noNote bool
	}{
		"two hunks of one fixup": {
			line: twoHunkLine,
			json: "done 1 0 1 0 0", gaps: 2,
			text: []string{"  fold: 1 fixup: 1 ahead of its context"},
		},
		"a gap on a fixup that applies": {
			line: func(g gitLine) {
				g.put("feat: f", "f.txt", "0\n")
				g.put("feat: a", "a.txt", "a\n")
				g.put("feat: b", "b.txt", "b\n")
				g.put("feat: c", "f.txt", "1\n")
				g.put("feat: back", "f.txt", "0\n")
				g.put("fixup! feat: a", "f.txt", "1\n")
				g.put("fixup! feat: b", "f.txt", "2\n")
			},
			json: "done 2 2 0 0 0", gaps: 1,
			text: []string{"  fold: 2 fixups, each applies at its target"},
		},
		"a conflict": {
			line: func(g gitLine) {
				g.put("feat: o", "o.txt", "o\n")
				g.put("feat: a", "a.txt", "a\n")
				g.put("fixup! feat: a", "n.txt", "one\n")
				g.git("rm", "-q", "n.txt")
				g.put("feat: drop")
				g.put("fixup! feat: o", "n.txt", "three\n")
			},
			json: "done 2 1 0 1 0",
			text: []string{"  fold: 2 fixups: 1 applies at its target, 1 conflicts", "    {fixup! feat: a} conflicts"},
		},
		"a reason with no note": {
			line: func(g gitLine) {
				g.put("feat: f", "f.txt", "0\n")
				g.put("feat: a", "a.txt", "a\n")
				g.put("feat: x", "f.txt", "1\n")
				g.put("feat: back", "f.txt", "0\n")
				g.put("fixup! feat: a", "f.txt", "1\n")
				g.put("fixup! feat: x", "c.txt", "c\n")
			},
			json: "done 2 1 0 0 1", noNote: true,
			text: []string{"  fold: 2 fixups: 1 applies at its target, 1 not analysed",
				"    {fixup! feat: x} not analysed: git drops its target {feat: x} as empty, so it folds into the commit below"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := summaryLine(t, "conflicts: []\n", c.line)
			o := synccmdtest.OverviewJSONWith(t, ctx, summaryLook())
			if o.Stack == nil || o.Fold == nil || o.Fold.Refusal != nil || len(o.Stack.ContextGaps) != c.gaps {
				t.Fatalf("stack %+v fold %+v, want %d gaps", o.Stack, o.Fold, c.gaps)
			}
			if c.noNote && len(o.Stack.ContextUnchecked) != 0 {
				t.Errorf("unchecked %q, want the reason to be the outcome's alone", o.Stack.ContextUnchecked)
			}
			wantSummary(t, o.Fold, c.json)
			got := summaryBlock(foldLook(t, ctx, "tmp/low", false))
			lines := strings.Split(got, "\n")
			if len(lines) != len(c.text) {
				t.Fatalf("summary\n%s\nwant %d lines like %q", got, len(c.text), c.text)
			}
			for _, want := range c.text {
				for _, subject := range []string{"fixup! feat: o", "fixup! feat: a", "fixup! feat: x", "feat: x"} {
					if strings.Contains(want, "{"+subject+"}") {
						want = strings.ReplaceAll(want, "{"+subject+"}", id(t, ctx, subject))
					}
				}
				if !strings.Contains(got, want) {
					t.Errorf("summary lacks %q:\n%s", want, got)
				}
			}
		})
	}
}

func summaryLook() synccmd.SyncOptions {
	return synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}
}

// A move! commit is no fixup: it is not counted, and a line with only a move
// has no summary.
func TestFoldLookCountsNoMoveAsAFixup(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, c := range map[string]struct {
		shape gittest.MoveShape
		json  string
		block bool
	}{
		"beside a fixup": {gittest.MoveShape{Move: true, SourceFixup: true}, "done 1 0 0 0 1", true},
		"alone":          {gittest.MoveShape{Move: true}, "none 0 0 0 0 0", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, _, _ := synccmdtest.MoveLineShaped(t, c.shape, "", false)
			look := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}}
			o := synccmdtest.OverviewJSONWith(t, ctx, look)
			if o.Fold == nil || o.Fold.Refusal != nil {
				t.Fatalf("fold %+v", o.Fold)
			}
			wantSummary(t, o.Fold, c.json)
			if got := summaryBlock(foldLook(t, ctx, gittest.MoveTmp, false)); (got != "") != c.block {
				t.Errorf("summary %q, want one: %v", got, c.block)
			}
		})
	}
}

// A refusal found after the analysis ran, here an unmet need, withholds the
// summary, whether the analysis read the fixups or failed; the stack keeps
// what it found.
func TestARefusedFoldLookWithholdsTheSummary(t *testing.T) {
	// Not parallel: it swaps syncrun.AnalyseFixupsOf.
	skipShort(t)
	ctx := summaryLine(t, "conflicts: []\ndefer:\n  - run: 'true'\n    needs: [docker]\n", twoHunkLine)
	opts := summaryLook()
	opts.Probes = synccmdtest.DockerDown().Probes()
	check := func(gaps, notes int) {
		t.Helper()
		o := synccmdtest.OverviewJSONWith(t, ctx, opts)
		if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "docker") {
			t.Fatalf("fold %+v, want the need refused", o.Fold)
		}
		wantSummary(t, o.Fold, "none 0 0 0 0 0")
		if o.Stack == nil || len(o.Stack.ContextGaps) != gaps || len(o.Stack.ContextUnchecked) != notes {
			t.Errorf("stack %+v, want %d gaps and %d notes", o.Stack, gaps, notes)
		}
		if got := lookOf(t, ctx, summaryLook()); !strings.Contains(got, "the run refuses the fold") || summaryBlock(got) != "" {
			t.Errorf("the refused look sums up the fixups:\n%s", got)
		}
	}
	check(2, 0)
	was := syncrun.AnalyseFixupsOf
	t.Cleanup(func() { syncrun.AnalyseFixupsOf = was })
	syncrun.AnalyseFixupsOf = func(string, []string, *wtsync.Config) (wtsync.FixupAnalysis, error) {
		return wtsync.FixupAnalysis{}, errors.New("merge-tree: out of cheese")
	}
	check(0, 1)
}
