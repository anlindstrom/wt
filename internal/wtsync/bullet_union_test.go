package wtsync

import (
	"errors"
	"strings"
	"testing"
)

func readme(items ...string) string {
	return "# Server\n\n## Prerequisites\n\n" + strings.Join(items, "\n") + "\n\n## Setup\n\nRun it.\n"
}

func resolveBullets(t *testing.T, base, trunk, branch string) string {
	t.Helper()
	out, err := BulletUnion{}.Resolve(conflict(base, trunk, branch))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	return string(out)
}

func refusedBullets(t *testing.T, base, trunk, branch, why string) {
	t.Helper()
	_, err := BulletUnion{}.Resolve(conflict(base, trunk, branch))
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), why) {
		t.Errorf("refusal %q does not say %q", err, why)
	}
}

func TestBulletUnionKeepsTrunkItemsThenTheBranchsNewOnes(t *testing.T) {
	t.Parallel()
	base := readme("- Java 21", "- Docker")
	got := resolveBullets(t, base,
		readme("- Java 21", "- Docker", "- Node 22"),
		readme("- Java 21", "- Docker", "- Gradle 9", "- Postgres 16"))
	want := readme("- Java 21", "- Docker", "- Node 22", "- Gradle 9", "- Postgres 16")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBulletUnionWritesAnItemBothSidesAddedOnce(t *testing.T) {
	t.Parallel()
	base := readme("- Java 21")
	got := resolveBullets(t, base,
		readme("- Java 21", "- Docker", "- Node 22"),
		readme("- Java 21", "- Docker", "- Gradle 9"))
	if want := readme("- Java 21", "- Docker", "- Node 22", "- Gradle 9"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBulletUnionMovesAnItemWithItsNestedLinesWhole(t *testing.T) {
	t.Parallel()
	base := readme("- Java 21")
	got := resolveBullets(t, base,
		readme("- Java 21", "- Node 22"),
		readme("- Java 21", "- Docker", "  - with compose", "  continued text", "- Gradle 9"))
	want := readme("- Java 21", "- Node 22", "- Docker", "  - with compose", "  continued text", "- Gradle 9")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBulletUnionRefusesWhatIsNotAdding(t *testing.T) {
	t.Parallel()
	base := readme("- Java 21", "- Docker")
	cases := []struct{ name, trunk, branch, why string }{
		{"the branch removes an item", readme("- Java 21", "- Docker", "- Node 22"), readme("- Java 21", "- Gradle 9"), "branch removes"},
		{"trunk removes an item", readme("- Java 21", "- Node 22"), readme("- Java 21", "- Docker", "- Gradle 9"), "trunk removes"},
		{"the branch rewords an item", readme("- Java 21", "- Docker", "- Node 22"), readme("- Java 21", "- Docker 27", "- Gradle 9"), "branch removes"},
		{"a block holds prose", readme("- Java 21", "- Docker", "- Node 22"), readme("- Java 21", "- Docker", "Install these first."), "not a bullet item"},
		{"different markers", readme("- Java 21", "- Docker", "- Node 22"), readme("- Java 21", "- Docker", "* Gradle 9"), "marker"},
		{"different indents", readme("- Java 21", "- Docker", "- Node 22"), readme("- Java 21", "- Docker", "  - Gradle 9"), "indent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { refusedBullets(t, base, c.trunk, c.branch, c.why) })
	}
}

func TestBulletUnionRefusesNumberedLists(t *testing.T) {
	t.Parallel()
	refusedBullets(t, readme("1. Java 21"),
		readme("1. Java 21", "2. Node 22"), readme("1. Java 21", "2. Gradle 9"), "not a bullet item")
}

// The conflict block holds only the nested lines; their parent bullet sits
// outside it, unchanged in the block: an item that exists in the base
// changes, which is a decision.
func TestBulletUnionRefusesNestedLinesUnderAnItemOutsideTheBlock(t *testing.T) {
	t.Parallel()
	refusedBullets(t, readme("- Java 21", "- Docker"),
		readme("- Java 21", "  - trunk note", "- Docker"),
		readme("- Java 21", "  - branch note", "- Docker"), "removes")
}

// two is a README with two lists, Build's and Test's prerequisites.
func two(build, test []string) string {
	return "# Server\n\n## Build prerequisites\n\n" + strings.Join(build, "\n") +
		"\n\n## Test prerequisites\n\n" + strings.Join(test, "\n") + "\n\n## Setup\n\nRun it.\n"
}

// An item is written once in its list, not once in the file: Test's list
// gets the branch's Docker although Build's list already has one.
func TestBulletUnionKeepsAnItemAnotherListAlreadyHas(t *testing.T) {
	t.Parallel()
	build := []string{"- Java 21", "- Docker"}
	got := resolveBullets(t, two(build, []string{"- Node 22"}),
		two(build, []string{"- Node 22", "- Postgres 16"}),
		two(build, []string{"- Node 22", "- Docker"}))
	if want := two(build, []string{"- Node 22", "- Postgres 16", "- Docker"}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A side that replaces Test's Docker with another item has removed an item of
// that list, although Build's list still has a Docker.
func TestBulletUnionRefusesARemovalWhileAnotherListKeepsTheItem(t *testing.T) {
	t.Parallel()
	build := []string{"- Java 21", "- Docker"}
	refusedBullets(t, two(build, []string{"- Docker", "- Node 22"}),
		two(build, []string{"- Docker", "- Node 22", "- Postgres 16"}),
		two(build, []string{"- Gradle 9", "- Node 22"}), "branch removes")
}

// Two trailing spaces end a line of a paragraph; the added item keeps them.
func TestBulletUnionKeepsTheTrailingSpacesOfAnAddedItem(t *testing.T) {
	t.Parallel()
	base := readme("- Java 21")
	got := resolveBullets(t, base,
		readme("- Java 21", "- Node 22"),
		readme("- Java 21", "- Docker  ", "  and compose"))
	if want := readme("- Java 21", "- Node 22", "- Docker  ", "  and compose"); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// A list with two conflict blocks, the branch adding the same item in both:
// the list gets it once.
func TestBulletUnionWritesAnItemOnceAcrossTwoBlocksOfOneList(t *testing.T) {
	t.Parallel()
	got := resolveBullets(t,
		readme("- A", "- B", "- C", "- D", "- E", "- F"),
		readme("- A", "- T1", "- B", "- C", "- D", "- T2", "- E", "- F"),
		readme("- A", "- Docker", "- B", "- C", "- D", "- Docker", "- E", "- F"))
	if want := readme("- A", "- T1", "- Docker", "- B", "- C", "- D", "- T2", "- E", "- F"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
