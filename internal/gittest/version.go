package gittest

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// MinGit is the git the tests are written against. Older gits (Apple's
// 2.54) replay and report some rebases differently, so a test that depends on
// the newer behaviour says so with Need.
const MinGit = "2.56"

var (
	gitVersionOnce sync.Once
	gitVersionText string
	gitVersionErr  error
)

// Need fails the test, naming the version it wants and the one it found, when
// git is older than minimum ("2.56"). git --version is read once per test
// binary. Put Homebrew's git first on PATH to get past it.
func Need(t testing.TB, minimum string) {
	t.Helper()
	gitVersionOnce.Do(func() {
		out, err := exec.Command("git", "--version").Output()
		gitVersionText, gitVersionErr = strings.TrimSpace(string(out)), err
	})
	if gitVersionErr != nil {
		t.Fatalf("git --version: %v", gitVersionErr)
	}
	if why := tooOld(gitVersionText, minimum); why != "" {
		t.Fatal(why)
	}
}

var versionNumbers = regexp.MustCompile(`(\d+)\.(\d+)`)

// older says whether the git named by the text of git --version is older than
// minimum, compared by major and minor. A text with no version is not older; tooOld refuses it first.
func older(text, minimum string) bool {
	have, want := versionNumbers.FindStringSubmatch(text), versionNumbers.FindStringSubmatch(minimum)
	if have == nil || want == nil {
		return false
	}
	for i := 1; i <= 2; i++ {
		h, _ := strconv.Atoi(have[i])
		w, _ := strconv.Atoi(want[i])
		if h != w {
			return h < w
		}
	}
	return false
}

// tooOld is why the git that printed text cannot run a test that needs
// minimum, or "" when it can. Text with no version in it is refused, never
// passed.
func tooOld(text, minimum string) string {
	if !versionNumbers.MatchString(text) {
		return fmt.Sprintf("git --version printed %q, which has no version to compare with %s", text, minimum)
	}
	if older(text, minimum) {
		return fmt.Sprintf("this test needs git %s or newer, and PATH finds %q: put Homebrew's git (/opt/homebrew/bin) first", minimum, text)
	}
	return ""
}
