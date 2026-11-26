package kit

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/schema"
)

// The statuses of a doctor's check record. Unknown is "could not tell".
const (
	CheckOK      = "ok"
	CheckWarn    = "warn"
	CheckFail    = "fail"
	CheckUnknown = "unknown"
)

// CheckRecord is one finding as `wt doctor --json` prints it.
type CheckRecord struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Owner  string  `json:"owner"`
	Detail string  `json:"detail"`
	Fix    *string `json:"fix"`
}

// WithFix names the command that fixes what the record found.
func (r *CheckRecord) WithFix(command string) *CheckRecord {
	r.Fix = jsonout.Strp(command)
	return r
}

// As gives the record a status other than the one its line's mark says.
func (r *CheckRecord) As(status string) *CheckRecord {
	r.Status = status
	return r
}

// Findings is what a doctor found: each finding's line in the text form,
// written as it is found, and its record for --json, made from the same
// words.
type Findings struct {
	w        io.Writer
	records  []*CheckRecord
	problems int
}

// NewFindings writes the text form to w; io.Discard where only the records
// are wanted.
func NewFindings(w io.Writer) *Findings { return &Findings{w: w} }

// Printf writes text that is no finding.
func (f *Findings) Printf(format string, args ...any) { fmt.Fprintf(f.w, format, args...) }

// OK is a "✓" line.
func (f *Findings) OK(id, format string, args ...any) *CheckRecord {
	return f.line("✓", id, CheckOK, format, args...)
}

// Note is a "-" line about something in use here: never a problem on its own.
func (f *Findings) Note(id, status, format string, args ...any) *CheckRecord {
	return f.line("-", id, status, format, args...)
}

// Unused is a "-" line about something not in use here, which is no record.
func (f *Findings) Unused(format string, args ...any) {
	f.Printf("  - "+format+"\n", args...)
}

// Fail is a "!" line, a problem the text form counts.
func (f *Findings) Fail(id, format string, args ...any) *CheckRecord {
	f.problems++
	return f.line("!", id, CheckFail, format, args...)
}

func (f *Findings) line(mark, id, status, format string, args ...any) *CheckRecord {
	detail := fmt.Sprintf(format, args...)
	f.Printf("  %s %s\n", mark, detail)
	return f.Add(id, status, detail)
}

// Add records a finding the text form says some other way.
func (f *Findings) Add(id, status, detail string) *CheckRecord {
	r := &CheckRecord{ID: id, Status: status, Owner: "wt", Detail: OneLine(strings.TrimSpace(detail))}
	f.records = append(f.records, r)
	return r
}

// Problems is how many "!" lines there were.
func (f *Findings) Problems() int { return f.problems }

// Records is every record, never nil.
func (f *Findings) Records() []CheckRecord {
	out := make([]CheckRecord, 0, len(f.records))
	for _, r := range f.records {
		out = append(out, *r)
	}
	return out
}

// StatusOf is the status of a check err stopped: unknown when something did
// not answer in time, fail otherwise.
func StatusOf(err error) string {
	if Unanswered(err) {
		return CheckUnknown
	}
	return CheckFail
}

// Unanswered reports that err is a command, git included, given up on at its
// deadline.
func Unanswered(err error) bool {
	var none git.NoAnswer
	var gitErr *git.Error
	return errors.As(err, &none) || (errors.As(err, &gitErr) && gitErr.TimedOut)
}

// ConfigProblems is the repository's configuration problems, one "!" line
// and one config.parse record each.
func ConfigProblems(c *Context, f *Findings) {
	if c.ConfigError == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(c.ConfigError.Error()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		r := f.Fail("config.parse", "%s", strings.TrimPrefix(line, "- "))
		if errors.Is(c.ConfigError, config.ErrNoConfig) {
			r.WithFix("wt init")
		}
	}
}

// DoctorRepo is one repository's records in `wt doctor --all --json`.
type DoctorRepo struct {
	Name   string        `json:"name"`
	Path   string        `json:"path"`
	Checks []CheckRecord `json:"checks"`
}

// doctorDocument is the object schema/doctor.v1.json describes.
type doctorDocument struct {
	Schema        int           `json:"schema"`
	SchemaVersion string        `json:"schemaVersion"`
	Command       string        `json:"command"`
	Checks        []CheckRecord `json:"checks"`
	Repos         []DoctorRepo  `json:"repos"`
}

// WriteDoctorJSON prints command's one object and ends it: exit 1 when a
// record anywhere is fail. repos is nil unless several repositories were
// asked for.
func WriteDoctorJSON(w io.Writer, command string, checks []CheckRecord, repos []DoctorRepo) error {
	if err := jsonout.WriteJSON(w, doctorDocument{
		Schema:        1,
		SchemaVersion: schema.VersionOf("doctor"),
		Command:       command,
		Checks:        checks,
		Repos:         repos,
	}); err != nil {
		return err
	}
	failed := func(c CheckRecord) bool { return c.Status == CheckFail }
	if slices.ContainsFunc(checks, failed) ||
		slices.ContainsFunc(repos, func(r DoctorRepo) bool { return slices.ContainsFunc(r.Checks, failed) }) {
		return ExitError{Code: 1}
	}
	return nil
}
