//go:build !windows

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Run writes the script to a private temporary file and runs it with args.
//
// binary is the agentpane the installed hook lines will invoke — normally the
// running executable, since a binary installing its own hooks is the one thing
// it can be sure about. The script's own --binary flag wins if the caller
// passed one.
func Run(args []string, binary string) error {
	dir, err := os.MkdirTemp("", "agentpane-install")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return err
	}

	full := []string{path}
	if binary != "" && !hasBinaryFlag(args) {
		full = append(full, "--binary", binary)
	}
	full = append(full, args...)

	sh := "/bin/bash"
	if _, err := os.Stat(sh); err != nil {
		// The script is bash 3.2, not POSIX sh: on a system without /bin/bash
		// take whatever bash is on PATH rather than running it under something
		// that cannot parse it.
		found, lerr := exec.LookPath("bash")
		if lerr != nil {
			return fmt.Errorf("the installer needs bash: %w", lerr)
		}
		sh = found
	}

	cmd := exec.Command(sh, full...) //nolint:gosec // the script is our own embedded copy
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// hasBinaryFlag reports whether the caller already chose a binary, in either
// spelling the script accepts.
func hasBinaryFlag(args []string) bool {
	for _, a := range args {
		if a == "--binary" || len(a) > 9 && a[:9] == "--binary=" {
			return true
		}
	}
	return false
}
