// Package installer carries the hook installer inside the binary.
//
// On unix the script is the implementation and stays the implementation: it
// is 600 lines of careful surgery on a file that is not ours
// (~/.claude/settings.json) with its own fixture harness, and a Go rewrite
// would be a second thing to keep correct. What embedding changes is who can
// run it — the installer travels with the binary, so someone who downloaded
// one file from a release, or ran `go install`, can wire their hooks without
// cloning anything.
//
// Windows has neither bash nor the python3 the script leans on for all its
// JSON handling, so there the same surgery is done natively: hooks.go decides
// what changes, ojson.go writes the file back without reordering it, and
// installer_windows.go does the I/O and the asking. That IS the second
// implementation the paragraph above argues against, accepted on the grounds
// that the alternative was putting a python3 install in front of every
// Windows user. The unix path is untouched by it: install.sh is still the
// only thing that edits a settings file on macOS or Linux.
package installer

import (
	_ "embed"
	"fmt"
)

//go:embed install.sh
var script string

// Script is the installer's source, for callers that want to read it rather
// than run it (the drift guard between this and doctor's inline patterns).
func Script() string { return script }

// ExitError carries the script's exit-code contract through a Go
// implementation of it: 0 done, 1 the user refused, 2 the environment is
// wrong. cmdInstall reads the code off either this or an *exec.ExitError from
// the unix path, so both spellings of "the installer exited 1" reach the shell
// as 1.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) ExitCode() int { return e.Code }
func (e *ExitError) Unwrap() error { return e.Err }

// refused is the "user said no" exit: code 1, nothing written.
func refused(format string, args ...any) error {
	return &ExitError{Code: 1, Err: fmt.Errorf(format, args...)}
}

// envErr is the "this machine cannot do what was asked" exit: code 2.
func envErr(format string, args ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, args...)}
}
