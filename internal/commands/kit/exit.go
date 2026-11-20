package kit

import "fmt"

// ExitError ends a command with an exit code of its own, whatever the
// command's failure would otherwise be. It says nothing: what there was to
// say was printed before it was returned.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
