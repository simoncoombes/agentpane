package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/simoncoombes/agentpane/internal/installer"
)

// cmdInstall runs the embedded hook installer.
//
// The whole point is that one downloaded file is enough: the binary carries
// its own installer, and the hook lines it writes name THIS executable, so a
// binary in ~/.local/bin and a binary in /opt both wire themselves correctly
// without anyone being asked where they put it.
func cmdInstall(stderr io.Writer, args []string) int {
	self, err := os.Executable()
	if err != nil {
		// Not fatal: the script's own detection (PATH, then the repo) is the
		// fallback it has always used.
		self = ""
	}
	err = installer.Run(args, self)
	if err == nil {
		return 0
	}
	// The script's exit codes are the contract (0 done, 1 refused, 2
	// environment): pass them through rather than flattening them to 1.
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "agentpane install: %v\n", err)
	return 2
}
