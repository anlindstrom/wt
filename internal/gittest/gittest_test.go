package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A directory holding a claude stays on the PATH for everything else in it.
func TestHideClaudeKeepsTheRestOfItsDirectory(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"claude", "tool"} {
		WriteExecutable(t, filepath.Join(bin, name), "#!/bin/sh\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	hideClaude(t.TempDir())
	if p, err := exec.LookPath("claude"); err == nil {
		t.Errorf("claude is still found, at %s", p)
	}
	p, err := exec.LookPath("tool")
	if err != nil || realPath(p) != realPath(filepath.Join(bin, "tool")) {
		t.Errorf("tool is %q, %v; want the one in %s", p, err, bin)
	}
}

// A devports on the machine is no more a part of a test than its claude.
func TestHideClaudeHidesDevportsToo(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"devports", "tool"} {
		WriteExecutable(t, filepath.Join(bin, name), "#!/bin/sh\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	hideClaude(t.TempDir())
	if p, err := exec.LookPath("devports"); err == nil {
		t.Errorf("devports is still found, at %s", p)
	}
	if _, err := exec.LookPath("tool"); err != nil {
		t.Errorf("tool is gone: %v", err)
	}
}

func TestOlderComparesMajorAndMinor(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]bool{
		"git version 2.54.0 (Apple Git-157)": true,
		"git version 2.55.1":                 true,
		"git version 2.56.0":                 false,
		"git version 2.100.0":                false,
		"git version 3.0.0":                  false,
		"git version 1.9.9":                  true,
		"unreadable":                         false,
	} {
		if got := older(text, MinGit); got != want {
			t.Errorf("older(%q, %s) = %v, want %v", text, MinGit, got, want)
		}
	}
}

func TestNeedRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	if why := tooOld("", MinGit); !strings.Contains(why, "no version") {
		t.Errorf("empty output passed: %q", why)
	}
	if why := tooOld("git: command garbled", MinGit); !strings.Contains(why, "garbled") {
		t.Errorf("unparseable output passed or hid the output: %q", why)
	}
	if why := tooOld("git version 2.54.0 (Apple Git-157)", MinGit); !strings.Contains(why, "2.54.0") {
		t.Errorf("an old git passed: %q", why)
	}
	if why := tooOld("git version 2.56.0", MinGit); why != "" {
		t.Errorf("a new enough git failed: %q", why)
	}
}
