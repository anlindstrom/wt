package wtsync

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// todoPlan is what the run does to the sequencer's list: the lift's marks
// and drops (liftPlan), and checks, the picks after which it stops to run
// the each_commit steps.
type todoPlan struct {
	liftPlan
	checks []string
	stack  stackMarks
	// stops puts a break at the end of every layer, where the per-layer
	// steps run: before each layer's update-ref line, and, with tail, at
	// the end of the list for the commits above the top layer (or, for a
	// rebase of one branch, the whole of it).
	stops, tail bool
	// open says the list starts inside a group whose pick was already run,
	// as a resume's does: the stopped command is the pick or a fixup or
	// squash folded into it, and any fixup or squash the list goes on with
	// belongs to it. due says the group is to be checked, so the break that
	// ends it is written even where no line of the list asks for one.
	open, due bool
	// tips puts a break at the end of every layer (the end of the list, for
	// a branch with none) whose commits include one of checks, in place of a
	// break after each of those commits. carry says the first layer of the
	// list already holds such a commit: a resume's list starts inside it.
	tips, carry bool
	// targets, with tips, also puts a break after every group a fixup or
	// squash folded into, when the group or one of those called for a check:
	// the fold target, as it stays. A target that ends a layer shares the
	// layer's break.
	targets bool
	// folded says the open group already took a fixup or squash.
	folded bool
	// edit is the full id of the commit a --stack --edit run stops at for a
	// person; final ends the list in a break, where the run checks the
	// line's content before git finishes. Empty and false for any other.
	edit  string
	final bool
}

// stackMarks is where a run rebasing a stack held on one line puts each
// layer's marker: an update-ref line for prefix plus the layer's number,
// after the last line of the layers up to it. layerOf numbers every commit
// of the line by its layer, 1 the bottom, top+1 the line's own commits
// above every layer. The zero value marks nothing.
type stackMarks struct {
	prefix  string
	top     int
	layerOf map[string]int
	// copy is where the editor leaves a copy of the list it wrote, for the
	// run to read the plan from; empty leaves none.
	copy string
	// forkPrefix and forksBelow put a marker for each fork commit of the
	// line's side branches, forkPrefix plus its number, before the first
	// pick above it (after that pick's layer markers), or at the end of the
	// list before its last stops: forksBelow numbers every commit of the
	// line by how many forks lie below it, and forks is how many there are.
	forkPrefix string
	forksBelow map[string]int
	forks      int
}

func (p todoPlan) edits() bool {
	return p.liftPlan.edits() || len(p.checks) > 0 || p.stack.top > 0 || p.stops || p.edit != "" || p.final
}

// todoEditor writes the script a run hands git as its sequence editor:
// every pick of a commit in marks is preceded by a break and made an edit,
// so the run sees HEAD before the pick is applied and stops after it; every
// pick of a commit in drops is taken out, which is what git itself does
// with a commit trunk already carries when the list is not asked to keep
// them; and every pick of a commit in checks is followed by a break, after
// the edit when it has one and after the fixup and squash lines folded into
// it, where the run checks the commit as it stays; its break comes before a
// layer marker already in the list, and a group is checked when its pick or
// any fixup or squash folded into it is to be checked. With stack marks, each
// layer's update-ref line goes after the last group of the layers up to it,
// that group's check break included, and before the next group's own lines.
// With stops, every update-ref line follows a break, the check break where
// there is one, and with tail the list ends in one.
// The break before a pick is what tells a pick git dropped as already
// upstream from one it committed: at the edit stop HEAD either moved or did
// not. The list names each commit by the abbreviation the repository is
// configured for, so a pick is matched by its id being a prefix of the full
// one; the abbreviations of a list are unique in the repository, and so is
// the match. Every other line is left as git wrote it. POSIX sh only, since
// git runs it through the shell wherever the run is.
func todoEditor(w io.Writer, p todoPlan) {
	var layers []string
	for sha, n := range p.stack.layerOf {
		layers = append(layers, fmt.Sprintf("%s=%d", sha, n))
	}
	sort.Strings(layers)
	var below []string
	for sha, n := range p.stack.forksBelow {
		if n > 0 {
			below = append(below, fmt.Sprintf("%s=%d", sha, n))
		}
	}
	sort.Strings(below)
	fmt.Fprintln(w, "#!/bin/sh")
	fmt.Fprintln(w, "# wt sync: stop before and after the picks that may need a version lift,")
	fmt.Fprintln(w, "# leave out the picks trunk already carries, stop after the picks to check,")
	fmt.Fprintln(w, "# and mark where each layer of a stack ends")
	fmt.Fprintf(w, "marks='%s'\n", strings.Join(p.marks, " "))
	fmt.Fprintf(w, "drops='%s'\n", strings.Join(p.drops, " "))
	fmt.Fprintf(w, "checks='%s'\n", strings.Join(p.checks, " "))
	fmt.Fprintf(w, "layers='%s'\n", strings.Join(layers, " "))
	fmt.Fprintf(w, "top=%d\n", p.stack.top)
	fmt.Fprintf(w, "mapref=%s\n", shellQuote(p.stack.prefix))
	fmt.Fprintf(w, "forkat='%s'\n", strings.Join(below, " "))
	fmt.Fprintf(w, "forks=%d\n", p.stack.forks)
	fmt.Fprintf(w, "forkref=%s\n", shellQuote(p.stack.forkPrefix))
	fmt.Fprintf(w, "copy=%s\n", shellQuote(p.stack.copy))
	fmt.Fprintf(w, "stops=%s\n", map[bool]string{true: "1"}[p.stops])
	fmt.Fprintf(w, "tail=%s\n", map[bool]string{true: "1"}[p.stops && p.tail])
	fmt.Fprintf(w, "edit=%s\n", shellQuote(p.edit))
	fmt.Fprintf(w, "final=%s\n", map[bool]string{true: "1"}[p.final])
	fmt.Fprintf(w, "tips=%s\n", map[bool]string{true: "1"}[p.tips])
	fmt.Fprintf(w, "targets=%s\n", map[bool]string{true: "1"}[p.tips && p.targets])
	fmt.Fprintf(w, "due=%s\n", map[bool]string{true: "1"}[p.tips && p.carry])
	grouped := p.open && p.due
	fmt.Fprintf(w, "pend=%s; gc=%s; open=%s; marked=0; forked=0; brk=\n", map[bool]string{true: "1"}[grouped && (!p.targets || p.folded)], map[bool]string{true: "1"}[p.targets && grouped], map[bool]string{true: "1"}[p.open])
	fmt.Fprintf(w, "%s\n", `close_group() { if [ -n "$pend" ]; then if [ -n "$1" ] && [ -n "$tips" ] && [ -n "$due" ]; then pend=; else printf 'break\n'; pend=; brk=1; fi; fi; }`)
	fmt.Fprintf(w, "%s\n", `tipbreak() { if [ -n "$tips" ] && [ -n "$due" ]; then printf 'break\n'; due=; brk=1; fi; }`)
	fmt.Fprintf(w, "%s\n", `stop() { [ -z "$stops" ] || [ -n "$brk" ] || printf 'break\n'; brk=; }`)
	fmt.Fprintf(w, "%s\n", `flush() { while [ "$marked" -lt "$1" ]; do marked=$((marked+1)); tipbreak; stop; printf 'update-ref %s%s\n' "$mapref" "$marked"; done; }`)
	fmt.Fprintf(w, "%s\n", `fflush() { while [ "$forked" -lt "$1" ]; do forked=$((forked+1)); printf 'update-ref %s%s\n' "$forkref" "$forked"; brk=; done; }`)
	fmt.Fprintln(w, `{`)
	fmt.Fprintln(w, `while IFS= read -r line || [ -n "$line" ]; do`)
	fmt.Fprintln(w, `  case "$line" in`)
	fmt.Fprintln(w, `    "pick "*|"edit "*)`)
	fmt.Fprintln(w, `      rest=${line#* }; id=${rest%% *}; out=$line; check=; pre=`)
	fmt.Fprintln(w, `      for m in $marks; do case "$m" in "$id"*) pre=1; out="edit $rest"; break ;; esac; done`)
	fmt.Fprintln(w, `      case "$edit" in "$id"*) out="edit $rest" ;; esac`)
	fmt.Fprintln(w, `      for m in $drops; do case "$m" in "$id"*) out=""; break ;; esac; done`)
	fmt.Fprintln(w, `      for m in $checks; do case "$m" in "$id"*) check=1; break ;; esac; done`)
	fmt.Fprintln(w, `      if [ -n "$out" ]; then`)
	fmt.Fprintln(w, `        layer=$((top+1))`)
	fmt.Fprintln(w, `        for l in $layers; do case "${l%%=*}" in "$id"*) layer=${l#*=}; break ;; esac; done`)
	fmt.Fprintln(w, `        fb=0; for l in $forkat; do case "${l%%=*}" in "$id"*) fb=${l#*=}; break ;; esac; done`)
	fmt.Fprintln(w, `        le=; [ "$marked" -ge $((layer-1)) ] || le=1; close_group "$le"; flush $((layer-1)); fflush "$fb"`)
	fmt.Fprintf(w, "%s\n", `        [ -z "$pre" ] || printf 'break\n'`)
	fmt.Fprintln(w, `        [ -z "$tips" ] || { [ -z "$check" ] || due=1; gc=$check; check=; }`)
	fmt.Fprintf(w, "%s\n", `        printf '%s\n' "$out"; pend=$check; open=1; brk=`)
	fmt.Fprintln(w, `      fi ;;`)
	fmt.Fprintln(w, `    "fixup "*|"squash "*)`)
	fmt.Fprintln(w, `      id=${line#* }; case "$id" in -[Cc]\ *) id=${id#* } ;; esac; id=${id%% *}`)
	fmt.Fprintln(w, `      for m in $checks; do case "$m" in "$id"*) if [ -n "$open" ]; then if [ -n "$tips" ]; then due=1; [ -z "$targets" ] || gc=1; else pend=1; fi; fi; break ;; esac; done; [ -z "$targets" ] || [ -z "$gc" ] || [ -z "$open" ] || pend=1`)
	fmt.Fprintf(w, "%s\n", `      printf '%s\n' "$line"; brk= ;;`)
	fmt.Fprintf(w, "%s\n", `    "update-ref `+ForkMapPrefix+`"*) close_group; printf '%s\n' "$line"; brk= ;;`)
	fmt.Fprintf(w, "%s\n", `    "update-ref "*) close_group 1; tipbreak; stop; printf '%s\n' "$line" ;;`)
	fmt.Fprintf(w, "%s\n", `    "break"|"break "*) close_group; printf '%s\n' "$line"; brk=1 ;;`)
	fmt.Fprintf(w, "%s\n", `    ""|"#"*) printf '%s\n' "$line" ;;`)
	fmt.Fprintf(w, "%s\n", `    *) printf '%s\n' "$line"; brk= ;;`)
	fmt.Fprintln(w, `  esac`)
	fmt.Fprintln(w, `done < "$1"`)
	fmt.Fprintln(w, `close_group 1; flush "$top"; fflush "$forks"; tipbreak`)
	fmt.Fprintln(w, `if { [ -n "$tail" ] || [ -n "$final" ]; } && [ -z "$brk" ]; then printf 'break\n'; fi`)
	fmt.Fprintln(w, `} > "$1.wt" && mv "$1.wt" "$1" && { [ -z "$copy" ] || cp "$1" "$copy"; } &&`)
	fmt.Fprintf(w, "%s\n", `{ [ -z "$edit" ] || printf '%s\n' "$edit" > "$(dirname "$1")/wt-sync-edit"; }`)
}

// sequenceEditor is todoEditor for a list with nothing to check.
func sequenceEditor(w io.Writer, marks, drops []string) {
	todoEditor(w, todoPlan{liftPlan: liftPlan{marks: marks, drops: drops}})
}
