package keepstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
)

// HasLaunchd says whether m runs launchd, which the keeper's
// start and stop need.
func HasLaunchd(m kit.Machine) bool { return m.Platform() == "darwin" }

// KeepJob is one repository's launchd job: everything the plist is made of.
type KeepJob struct {
	Label     string
	PlistPath string
	Exe       string // the wt binary, absolute
	MainRoot  string
	GitDir    string
	Every     time.Duration
	NoPush    bool
	// Env is what the job runs with. launchd gives a job neither the shell's
	// PATH nor its ssh settings, so start captures them.
	Env map[string]string
	// Launchctl loads and unloads the job.
	Launchctl Launchctl
}

// KeepJobFor names the job for ctx's repository: a label from the
// repository's name and a hash of the main checkout's path, so two checkouts
// of one repository get two jobs, under ~/Library/LaunchAgents.
func KeepJobFor(ctx *kit.Context) (KeepJob, error) {
	home, err := ctx.Machine.HomeDir()
	if err != nil {
		return KeepJob{}, err
	}
	sum := sha256.Sum256([]byte(ctx.Repo.MainRoot))
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, ctx.Repo.Name)
	label := "se.wt.sync-keep." + name + "-" + hex.EncodeToString(sum[:4])
	return KeepJob{
		Label:     label,
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		MainRoot:  ctx.Repo.MainRoot,
		Launchctl: Launchctl{Exe: ctx.Machine.Launchctl},
	}, nil
}

// Launchctl runs launchctl: Exe, or the one on the PATH, looked up each
// time, when Exe is "".
type Launchctl struct {
	Exe string
}

func (d Launchctl) command(args ...string) (*exec.Cmd, error) {
	exe := d.Exe
	if exe == "" {
		var err error
		if exe, err = exec.LookPath("launchctl"); err != nil {
			return nil, errors.New("launchctl is not on the PATH")
		}
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	return cmd, nil
}

func (d Launchctl) run(args ...string) (string, error) {
	cmd, err := d.command(args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			text = err.Error()
		}
		return "", errors.New(text)
	}
	return strings.TrimSpace(string(out)), nil
}

func (d Launchctl) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// probeDeadline bounds the question whether a job is loaded. launchd answers
// in milliseconds; one that has not answered in this long is not going to.
const probeDeadline = 10 * time.Second

// noSuchService is `launchctl print`'s exit status for a service that is not
// in the domain.
const noSuchService = 113

// Loaded asks launchd whether the job is in the user's domain, taking a
// launchd that would not say as a no.
func (d Launchctl) Loaded(label string) bool {
	loaded, err := d.Query(label)
	return err == nil && loaded
}

// Query is Loaded keeping apart a job that is not loaded from a launchd that
// would not say, which is the error.
func (d Launchctl) Query(label string) (loaded bool, err error) {
	cmd, err := d.command("print", d.domain()+"/"+label)
	if err != nil {
		return false, err
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	timedOut, code, err := git.RunBounded(probeDeadline, cmd)
	switch {
	case timedOut:
		return false, git.NoAnswer(fmt.Sprintf("launchctl print did not answer within %s", probeDeadline))
	case err == nil:
		return true, nil
	case code == noSuchService:
		return false, nil
	}
	if said := strings.TrimSpace(out.String()); said != "" {
		return false, errors.New(said)
	}
	return false, err
}

// Load bootstraps the plist into the user's domain, falling back to the
// older load verb where bootstrap is refused.
func (d Launchctl) Load(plist string) error {
	_, err := d.run("bootstrap", d.domain(), plist)
	if err == nil {
		return nil
	}
	if _, lerr := d.run("load", plist); lerr == nil {
		return nil
	}
	return fmt.Errorf("launchctl bootstrap: %w", err)
}

// Unload takes the job out of the user's domain; a job that is not there
// is not an error, the plist is going anyway.
func (d Launchctl) Unload(label, plist string) error {
	_, err := d.run("bootout", d.domain()+"/"+label)
	if err == nil {
		return nil
	}
	if _, lerr := d.run("unload", plist); lerr == nil {
		return nil
	}
	if strings.Contains(err.Error(), "No such process") || strings.Contains(err.Error(), "not find") {
		return nil
	}
	return fmt.Errorf("launchctl bootout: %w", err)
}
