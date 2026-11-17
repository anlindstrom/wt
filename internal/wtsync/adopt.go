package wtsync

import "fmt"

// AdoptKeptRef is where wt sync resume --adopt keeps the tip it took over,
// before it moves anything: refs/wt-sync-kept/<branch>/<epoch>-adopted, or
// the first of its -2, -3, ... that is free.
func AdoptKeptRef(branch string, epoch int64) string {
	return fmt.Sprintf("%s-adopted", EditKeptRef(branch, epoch))
}
