package wtsync

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseEachCommitAndNeeds(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`
defer:
  - run: ./gen.sh
    needs: [docker]
each_commit:
  - run: ./check.sh
    paths: ["**/*.java"]
    needs: [docker]
  - run: ./lint.sh
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Defer[0].Needs, []string{"docker"}) {
		t.Errorf("defer needs %v", cfg.Defer[0].Needs)
	}
	if len(cfg.EachCommit) != 2 {
		t.Fatalf("each_commit %+v", cfg.EachCommit)
	}
	c := cfg.EachCommit[0]
	if c.Run != "./check.sh" || !reflect.DeepEqual(c.Paths, []string{"**/*.java"}) || !reflect.DeepEqual(c.Needs, []string{"docker"}) {
		t.Errorf("each_commit[0] %+v", c)
	}
	if cfg.EachCommit[1].Run != "./lint.sh" || len(cfg.EachCommit[1].Paths) != 0 {
		t.Errorf("each_commit[1] %+v", cfg.EachCommit[1])
	}
}

func TestParseRefusesAnUnknownNeed(t *testing.T) {
	t.Parallel()
	for _, yaml := range []string{
		"defer:\n  - run: ./gen.sh\n    needs: [podman]\n",
		"each_commit:\n  - run: ./check.sh\n    needs: [podman]\n",
	} {
		_, err := Parse([]byte(yaml))
		if err == nil || !strings.Contains(err.Error(), `unknown need "podman"`) || !strings.Contains(err.Error(), "docker") {
			t.Errorf("%q: err %v, want an unknown need naming the known ones", yaml, err)
		}
	}
}

func TestParseRefusesAnEachCommitStepWithoutRun(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("each_commit:\n  - paths: [a.txt]\n"))
	if err == nil || !strings.Contains(err.Error(), "each_commit[0] has no run") {
		t.Fatalf("err %v", err)
	}
}

func TestParseRefusesACommitOnAnEachCommitStep(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("each_commit:\n  - run: ./check.sh\n    commit: fix\n"))
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err %v", err)
	}
}

func TestParseAcceptsAnEnvNeedOnEveryStepKind(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`
defer:
  - run: pnpm install
    needs: [env:GH_PACKAGES_TOKEN, docker]
each_commit:
  - run: ./check.sh
    needs: [env:_OTHER1]
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Defer[0].Needs, []string{"env:GH_PACKAGES_TOKEN", "docker"}) {
		t.Errorf("defer needs %v", cfg.Defer[0].Needs)
	}
	if got := cfg.DeclaredNeeds(); !reflect.DeepEqual(got, []string{"docker", "env:GH_PACKAGES_TOKEN", "env:_OTHER1"}) {
		t.Errorf("declared needs %v", got)
	}
}

func TestParseRefusesAnEnvNeedWithABadName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "1ABC", "A-B", "A B", "A=B", "$X", "a.b"} {
		for _, kind := range []string{"defer", "each_commit"} {
			_, err := Parse([]byte(kind + ":\n  - run: ./x.sh\n    needs: [\"env:" + name + "\"]\n"))
			if err == nil || !strings.Contains(err.Error(), "env:"+name) || !strings.Contains(err.Error(), "not a valid environment variable name") {
				t.Errorf("%s env:%q: err %v", kind, name, err)
			}
		}
	}
}

func TestProbeNeedsEnvChecksSetAndNonEmptyWithoutNamingTheValue(t *testing.T) {
	const value = "tok-VALUE-9f2a"
	t.Setenv("WT_NEEDS_TEST_SET", value)
	t.Setenv("WT_NEEDS_TEST_EMPTY", "")
	t.Setenv("WT_NEEDS_TEST_UNSET", "")
	if err := os.Unsetenv("WT_NEEDS_TEST_UNSET"); err != nil {
		t.Fatal(err)
	}
	got := ProbeNeeds([]string{"env:WT_NEEDS_TEST_SET", "env:WT_NEEDS_TEST_EMPTY", "env:WT_NEEDS_TEST_UNSET"})
	if got["env:WT_NEEDS_TEST_SET"] != nil {
		t.Errorf("set: %v", got["env:WT_NEEDS_TEST_SET"])
	}
	for _, n := range []string{"env:WT_NEEDS_TEST_EMPTY", "env:WT_NEEDS_TEST_UNSET"} {
		err := got[n]
		if err == nil || !strings.Contains(err.Error(), n[4:]) || strings.Contains(err.Error(), value) {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func TestProbeNeedsEnvReadsTheEnvironmentTheStepGets(t *testing.T) {
	// NO_COLOR is set by wt on every step, whatever the caller has.
	t.Setenv("NO_COLOR", "")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	if got := ProbeNeeds([]string{"env:NO_COLOR"})["env:NO_COLOR"]; got != nil {
		t.Errorf("NO_COLOR is given to every step, but the probe says %v", got)
	}
	// GIT_EDITOR is replaced by wt: the caller's own value does not count
	// for a step, and an unset one is still there.
	t.Setenv("GIT_EDITOR", "")
	if err := os.Unsetenv("GIT_EDITOR"); err != nil {
		t.Fatal(err)
	}
	if got := ProbeNeeds([]string{"env:GIT_EDITOR"})["env:GIT_EDITOR"]; got != nil {
		t.Errorf("GIT_EDITOR is given to every step, but the probe says %v", got)
	}
	// What the probe says is what a step process sees.
	t.Setenv("WT_NEEDS_TEST_SET", "x")
	if _, _, err := runStep(t.TempDir(), `test "$NO_COLOR" = 1 && test "$GIT_EDITOR" = true && test -n "$WT_NEEDS_TEST_SET"`, time.Minute); err != nil {
		t.Errorf("step environment: %v", err)
	}
}

func TestProbeEnvInTakesTheFirstEntryOfAName(t *testing.T) {
	t.Parallel()
	if err := probeEnvIn([]string{"NAME=tok", "NAME="}, "NAME"); err != nil {
		t.Errorf("first entry is set: %v", err)
	}
	if err := probeEnvIn([]string{"NAME=", "NAME=tok"}, "NAME"); err == nil {
		t.Error("first entry is empty, the step sees it empty")
	}
	if err := probeEnvIn([]string{"OTHER=x"}, "NAME"); err == nil {
		t.Error("an absent name passed")
	}
}
