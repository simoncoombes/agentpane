//go:build !windows

package main

// platformConsoleColumns reports "unknown" on unix, which costs nothing: the
// backends here (iTerm2, tmux, WezTerm, kitty) all size a new pane in CELLS,
// so none of them ever asks. Only `wt --size`, which wants a fraction of the
// parent, needs a width to divide by.
func platformConsoleColumns() int { return 0 }
