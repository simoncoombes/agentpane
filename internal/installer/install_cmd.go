package installer

// install_cmd.go — the parts of `agentpane install` that are the same
// everywhere, split out of installer_windows.go for one reason: they are the
// parts that touch the user's file, and they should be under test on the
// machines CI actually runs. Nothing here is Windows-specific; what stays
// behind is the console probe and the binary lookup, which are.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const usage = `usage: agentpane install [--dry-run] [--yes] [--uninstall] [--autopane] [--binary PATH] [--settings PATH] [--print-snippet]

  --dry-run        show the proposed diff, write nothing
  --yes            skip the confirmation prompt
  --uninstall      remove agentpane entries only (hook + autopane), pruning
                   emptied groups; everything else is left alone
  --autopane       also auto-open the TUI in a terminal split when a session
                   starts: appends one extra SessionStart group (matcher
                   "startup|resume") running "agentpane autopane"; combined
                   with --uninstall it removes only that entry
  --binary PATH    use this agentpane binary instead of auto-detection
  --settings PATH  target settings file (default: %USERPROFILE%\.claude\settings.json)
  --print-snippet  print only the JSON snippet for manual pasting, then exit

exit codes: 0 success or nothing-to-do, 1 user abort/refusal, 2 environment error`

type flags struct {
	dryRun       bool
	assumeYes    bool
	uninstall    bool
	autopane     bool
	printSnippet bool
	binary       string
	settings     string
}

func parseFlags(args []string) (flags, error) {
	home, _ := os.UserHomeDir()
	f := flags{settings: filepath.Join(home, ".claude", "settings.json")}

	need := func(i int, name string) (string, error) {
		if i+1 >= len(args) {
			return "", envErr("%s requires a path", name)
		}
		return args[i+1], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			f.dryRun = true
		case a == "--yes":
			f.assumeYes = true
		case a == "--uninstall":
			f.uninstall = true
		case a == "--autopane":
			f.autopane = true
		case a == "--print-snippet":
			f.printSnippet = true
		case a == "--binary":
			v, err := need(i, "--binary")
			if err != nil {
				return f, err
			}
			f.binary, i = v, i+1
		case strings.HasPrefix(a, "--binary="):
			f.binary = strings.TrimPrefix(a, "--binary=")
		case a == "--settings":
			v, err := need(i, "--settings")
			if err != nil {
				return f, err
			}
			f.settings, i = v, i+1
		case strings.HasPrefix(a, "--settings="):
			f.settings = strings.TrimPrefix(a, "--settings=")
		case a == "-h" || a == "--help":
			// WriteString rather than Println: the usage text contains
			// %USERPROFILE%, and vet reads a lone %U in a Println as a
			// mistyped format directive.
			os.Stdout.WriteString(usage + "\n") //nolint:errcheck // usage output
			return f, &ExitError{Code: 0}
		default:
			fmt.Fprintf(os.Stderr, "[error] unknown argument: %s\n%s\n", a, usage)
			return f, &ExitError{Code: 2}
		}
	}
	return f, nil
}

// printSnippet prints the hooks block for someone who would rather paste it
// in by hand than let anything edit their file.
func printSnippet(binary string) error {
	prog := binary
	if prog == "" {
		if p, err := exec.LookPath("agentpane"); err == nil {
			prog = p
		} else {
			prog = "agentpane"
		}
	}
	if prog != "agentpane" {
		if abs, err := filepath.Abs(prog); err == nil {
			prog = abs
		}
	}
	cmd := quoteIfNeeded(prog) + " hook"

	root := newObject()
	hooks := newObject()
	for _, ev := range hookEvents {
		arr := newArray()
		arr.items = append(arr.items, newGroup("", cmd))
		hooks.set(ev, arr)
	}
	root.set("hooks", hooks)
	fmt.Print(root.render("  "))
	return nil
}

// writeSettings backs the file up and replaces it atomically.
//
// Everything happens against the RESOLVED path, so a settings.json that is a
// symlink into a dotfiles repo has its target rewritten and the link itself
// left alone — replacing the link would quietly detach the file from whatever
// manages it.
func writeSettings(settings, content string) error {
	resolved, err := filepath.EvalSymlinks(settings)
	if err != nil {
		resolved = settings // a file that does not exist yet has nothing to resolve
	}
	dir := filepath.Dir(resolved)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return envErr("failed to create %s: %v", dir, err)
	}

	if _, err := os.Stat(resolved); err == nil {
		backup := resolved + ".agentpane-backup-" + time.Now().Format("20060102-150405")
		base := backup
		for n := 1; ; n++ {
			if _, err := os.Stat(backup); os.IsNotExist(err) {
				break
			}
			backup = fmt.Sprintf("%s.%d", base, n)
		}
		data, rerr := os.ReadFile(resolved)
		if rerr != nil {
			return envErr("failed to read %s for backup: %v", resolved, rerr)
		}
		if werr := os.WriteFile(backup, data, 0o600); werr != nil {
			return envErr("failed to write backup %s: %v", backup, werr)
		}
		fmt.Printf("[info] backup written: %s\n", backup)
	}

	tmp := filepath.Join(dir, fmt.Sprintf(".%s.agentpane-tmp-%d", filepath.Base(resolved), os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return envErr("failed to write %s: %v", tmp, err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()      //nolint:errcheck // the write error is what matters
		os.Remove(tmp) //nolint:errcheck // best-effort cleanup
		return envErr("failed to write %s: %v", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()      //nolint:errcheck
		os.Remove(tmp) //nolint:errcheck
		return envErr("failed to flush %s: %v", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return envErr("failed to close %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, resolved); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return envErr("failed to replace %s: %v", resolved, err)
	}
	fmt.Printf("[info] wrote %s\n", resolved)
	return nil
}
