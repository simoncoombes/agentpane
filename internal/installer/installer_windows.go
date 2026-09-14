package installer

// installer_windows.go — `agentpane install` on Windows.
//
// Same contract as install.sh, same flags, same [warn]/[info]/[plan] report,
// same exit codes (0 done, 1 refused, 2 environment). What differs is only
// what had to: there is no bash to run the script and no python3 to do its
// JSON, so hooks.go decides and ojson.go serialises, and this file does the
// I/O, the asking, and the atomic write.
//
// The two safety properties the script has are the two that matter most here,
// because the file being edited is the user's and is not ours:
//
//   - nothing is written without either --yes or a yes on a real console;
//   - the write is a backup, then a temp file, then a rename onto the
//     RESOLVED path, so a settings.json that is a symlink into a dotfiles
//     repo is followed rather than replaced.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Run is the installer. binary is the agentpane the written hook lines will
// invoke — normally the running executable, since a binary installing its own
// hooks is the one thing it can be sure about. An explicit --binary wins.
func Run(args []string, binary string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.binary == "" {
		f.binary = binary
	}

	if f.printSnippet {
		return printSnippet(f.binary)
	}

	// Removal matches any agentpane binary path, so it needs no binary at all.
	resolved := ""
	if !f.uninstall {
		if resolved, err = resolveBinary(f); err != nil {
			return err
		}
		fmt.Printf("[info] binary:   %s\n", resolved)
	}

	settings, err := filepath.Abs(f.settings)
	if err != nil {
		return envErr("cannot resolve %s: %v", f.settings, err)
	}
	fmt.Printf("[info] settings: %s\n", settings)

	in := planInput{
		settingsPath: settings,
		binary:       resolved,
		uninstall:    f.uninstall,
		autopane:     f.autopane,
	}
	if data, rerr := os.ReadFile(settings); rerr == nil {
		in.exists = true
		in.oldText = string(data)
	} else if !os.IsNotExist(rerr) {
		return envErr("cannot read %s: %v", settings, rerr)
	}
	if target, lerr := os.Readlink(settings); lerr == nil && target != "" {
		if abs, aerr := filepath.Abs(target); aerr == nil {
			in.symlinkTarget = abs
		} else {
			in.symlinkTarget = target
		}
	}

	p, err := computePlan(in)
	if err != nil {
		return envErr("%v", err)
	}
	for _, line := range p.report {
		fmt.Println(line)
	}
	if p.status != statusChanges {
		return nil
	}

	diff := unifiedDiff(settings, in.oldText, p.proposed)
	if f.dryRun {
		fmt.Println("[plan] proposed diff:")
		fmt.Print(diff)
		fmt.Println("[info] dry run: nothing was written")
		return nil
	}

	if !f.assumeYes {
		if !stdinIsConsole() {
			return refused("stdin is not a console; refusing to write without --yes (use --dry-run to preview)")
		}
		fmt.Printf("Apply these changes to %s? [y/N] ", settings)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println("[info] aborted; nothing written")
			return &ExitError{Code: 1}
		}
	}

	if err := writeSettings(settings, p.proposed); err != nil {
		return err
	}

	if f.uninstall {
		if f.autopane {
			fmt.Println("[info] agentpane autopane entry removed; hook entries and everything else were left untouched")
		} else {
			fmt.Println("[info] agentpane hook entries removed; everything else was left untouched")
		}
		return nil
	}
	fmt.Println("[info] hooks are re-read per invocation: running Claude Code sessions pick this up on their next tool call, no restart needed")
	fmt.Println(`[info] run "agentpane doctor" to verify the installation`)
	return nil
}

// resolveBinary finds the agentpane the hook lines will name, and refuses
// anything it could not recognise again later.
func resolveBinary(f flags) (string, error) {
	path := f.binary
	if path == "" {
		if p, err := exec.LookPath("agentpane"); err == nil {
			path = p
		} else if home, herr := os.UserHomeDir(); herr == nil {
			candidate := filepath.Join(home, "go", "bin", "agentpane.exe")
			if _, serr := os.Stat(candidate); serr == nil {
				path = candidate
			}
		}
	}
	if path == "" {
		return "", envErr("no agentpane binary found. Install one with:\n" +
			"[error]   go install github.com/simoncoombes/agentpane/cmd/agentpane@latest\n" +
			"[error] or point this command at one with --binary PATH")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", envErr("cannot resolve %s: %v", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", envErr("%s does not exist", abs)
	}
	// Re-runs and --uninstall recognise their own entries by the program's
	// basename, so any other name would duplicate groups on every re-run and
	// be orphaned by --uninstall.
	if !isAgentpaneProgram(abs) {
		return "", envErr("%s is not named \"agentpane\"; re-runs and --uninstall only recognize agentpane hook commands\n"+
			"[error] rename or copy it so the basename is exactly \"agentpane.exe\", or omit --binary to auto-detect", abs)
	}
	// Hooks run with an unpredictable PATH, so the absolute path above is
	// what gets written. Verify it actually executes before writing it in.
	if err := exec.Command(abs, "version").Run(); err != nil {
		return "", envErr("%q version failed to execute; refusing to install a broken hook command", abs)
	}
	return abs, nil
}

// stdinIsConsole reports whether there is a real console to ask a question
// on. GetConsoleMode succeeds only for a console handle, so a piped or
// redirected stdin answers false — the same test `[ -t 0 ]` makes in the
// script, and the reason a scripted run must pass --yes.
func stdinIsConsole() bool {
	var mode uint32
	err := windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode)
	return err == nil
}
