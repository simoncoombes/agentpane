// Command agentpane is the read-only subagent monitor (SPEC.md rev 3.1
// PART 6). The default invocation attaches to the current directory's newest
// live Claude Code session and renders the live tree; verbs and flags cover
// the demo replay, the hook forwarder, doctor, run history, the status-bar
// --oneline, and the CI --render-once frame.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
)

// Version is printed by `agentpane version`; override with
// -ldflags "-X main.Version=v1.2.3".
var Version = "dev"

// usageText is PART 6 verbatim.
const usageText = `agentpane                       attach to the current session, render live
agentpane --session <id>        attach to exactly this session id, never guess (autopane panes are pinned this way)
agentpane --demo                replay the §2.10 scenario (no Claude needed)
agentpane --demo-speed 2.0
agentpane --demo-seek 90        jump to t=90s
agentpane install               wire the hooks into ~/.claude/settings.json (asks first; --dry-run, --uninstall, --autopane)
agentpane hook                  read one hook JSON on stdin, forward, exit 0 always
agentpane autopane              SessionStart hook: auto-open the TUI in a split, exit 0 always (--explain says why not)
agentpane doctor                check hooks installed, socket reachable, terminal backend, font glyphs
agentpane doctor --session <id> the same checks for exactly that session's pane (no cwd guess)
agentpane runs                  list persisted runs (every session)
agentpane runs --session <id>   list only that session's runs
agentpane runs show <id>        print one run summary
agentpane --width wide|narrow   fix the layout at 64 or 44 columns (default: fill the pane)
agentpane --oneline             single-row status output for a tmux/iTerm2 status bar (§3.20)
agentpane --oneline --session <id>
                                status of exactly that session's pane

agentpane --no-bell --no-badge --no-notify --no-links
agentpane --log <path>          debug log
agentpane --source hook|transcript|fake|codex
                                watch an OpenAI Codex run with --source codex
agentpane version
`

// cliFlags is the parsed flag set.
type cliFlags struct {
	session    string
	demo       bool
	demoSpeed  float64
	demoSeek   int
	renderOnce bool
	cols, rows int
	oneline    bool
	width      string
	accent     string
	noColor    bool
	noBell     bool
	noBadge    bool
	noNotify   bool
	noLinks    bool
	logPath    string
	source     string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Verbs first: a leading non-flag argument dispatches a subcommand.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "hook":
			return cmdHook(os.Stdin)
		case "autopane":
			return cmdAutopane(os.Stdin, args[1:])
		case "install":
			return cmdInstall(stderr, args[1:])
		case "doctor":
			return cmdDoctor(stdout, stderr, args[1:])
		case "runs":
			return cmdRuns(stdout, stderr, args[1:])
		case "version":
			fmt.Fprintln(stdout, Version)
			return 0
		case "help":
			fmt.Fprint(stdout, usageText)
			return 0
		default:
			fmt.Fprintf(stderr, "agentpane: unknown command %q\n\n%s", args[0], usageText)
			return 2
		}
	}

	// A status bar must never see a usage dump, a stack trace, or a nonzero
	// exit. cmdOneline promises exit-0-always but never runs when the flags
	// themselves are bad, so the promise is kept here instead: the parser's
	// output is swallowed and the failure is answered with ONE short line.
	oneline := wantsOneline(args)
	parseErrOut := stderr
	if oneline {
		parseErrOut = io.Discard
	}
	fl, err := parseFlags(args, parseErrOut)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		if oneline {
			fmt.Fprintln(stdout, onelineFlagError(err))
			return 0
		}
		return 2
	}

	switch {
	case fl.oneline:
		return cmdOneline(stdout, fl)
	case fl.renderOnce:
		return cmdRenderOnce(stdout, fl)
	default:
		return runTUI(fl, stderr)
	}
}

// wantsOneline reports whether these raw arguments ask for the §3.20a status
// row, before the flag parser gets a chance to reject them. It has to be a
// pre-parse scan: the whole point is to answer the parse failures too.
func wantsOneline(args []string) bool {
	for _, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		if name != "--oneline" && name != "-oneline" {
			continue
		}
		if hasValue {
			switch strings.ToLower(value) {
			case "0", "f", "false":
				continue // an explicit --oneline=false is not a status bar
			}
		}
		return true
	}
	return false
}

// onelineFlagError renders a flag-parse failure as one short single-line row.
// A status bar has one row: it gets the reason, clipped, and never the usage
// block that goes with it.
func onelineFlagError(err error) string {
	msg := strings.TrimSpace(strings.ReplaceAll(err.Error(), "\n", " "))
	if msg == "" {
		msg = "bad flags"
	}
	const max = 44
	if r := []rune(msg); len(r) > max {
		msg = string(r[:max-1]) + "…"
	}
	return "agentpane: " + msg
}

func parseFlags(args []string, stderr io.Writer) (cliFlags, error) {
	var fl cliFlags
	fs := flag.NewFlagSet("agentpane", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText) }

	fs.StringVar(&fl.session, "session", "", "attach to exactly this session id, never guess")
	fs.BoolVar(&fl.demo, "demo", false, "replay the §2.10 scenario")
	fs.Float64Var(&fl.demoSpeed, "demo-speed", 1.0, "demo playback speed multiplier")
	fs.IntVar(&fl.demoSeek, "demo-seek", fake.SnapshotSeconds, "demo seek in seconds")
	fs.BoolVar(&fl.renderOnce, "render-once", false, "print one rendered frame to stdout and exit (CI/screenshots)")
	fs.IntVar(&fl.cols, "cols", 64, "columns for --render-once")
	fs.IntVar(&fl.rows, "rows", 40, "rows for --render-once")
	fs.BoolVar(&fl.oneline, "oneline", false, "single-row status output (§3.20)")
	fs.StringVar(&fl.width, "width", "", "layout width: fill|wide|narrow")
	fs.StringVar(&fl.accent, "accent", "", "live accent: auto|2|3|4|5|6")
	fs.BoolVar(&fl.noColor, "no-color", false, "disable all color (like NO_COLOR)")
	fs.BoolVar(&fl.noBell, "no-bell", false, "disable the ask bell")
	fs.BoolVar(&fl.noBadge, "no-badge", false, "disable the iTerm2 badge")
	fs.BoolVar(&fl.noNotify, "no-notify", false, "disable OSC 9 notifications")
	fs.BoolVar(&fl.noLinks, "no-links", false, "disable OSC 8 hyperlinks")
	fs.StringVar(&fl.logPath, "log", "", "debug log path")
	fs.StringVar(&fl.source, "source", "", "force one source: hook|transcript|fake|codex")

	if err := fs.Parse(args); err != nil {
		return fl, err
	}
	// given distinguishes "flag absent" from "flag present and empty"; only
	// fs.Visit knows the difference, since both leave the value "".
	given := func(name string) bool {
		seen := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == name {
				seen = true
			}
		})
		return seen
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agentpane: unexpected argument %q\n\n%s", fs.Arg(0), usageText)
		return fl, fmt.Errorf("unexpected argument")
	}
	// `--session ""` is not "no pin": it is a pin whose value went missing.
	// Accepting it silently drops the pane back onto the cwd guess — the
	// coin flip in a directory with five live sessions that this flag exists
	// to remove — and re-keys the run store and the state file to whatever
	// was guessed. Any wrapper that expands an unset variable produces
	// exactly this, so it is rejected as loudly as `--render-once --session`.
	if given("session") && fl.session == "" {
		fmt.Fprintf(stderr, "agentpane: --session was given an empty value.\n"+
			"  an empty pin is not \"no pin\" - agentpane would fall back to guessing this\n"+
			"  directory's newest session, which is what --session exists to prevent.\n"+
			"  the session id lives in $CLAUDE_CODE_SESSION_ID; quote it, or drop --session\n"+
			"  entirely to ask for the guess deliberately\n")
		return fl, fmt.Errorf("--session needs a session id")
	}
	// --session names a REAL session; --render-once always renders the
	// built-in §2.10 scenario. Together they printed eight fabricated agents
	// and a fake permission prompt with nothing marking them as fake — a
	// confident answer about a session that was never read (C8). Rendering the
	// real session instead is not worth what it costs: the live source stack
	// binds that session's hook socket, unlinking whatever is already there,
	// so a screenshot command would take the hook stream away from the pane it
	// was meant to inspect.
	//
	// --render-once WITHOUT --session is untouched: it is the CI/screenshot
	// path (PART 10).
	if fl.renderOnce && fl.session != "" {
		fmt.Fprintf(stderr, "agentpane: --render-once renders the built-in demo scenario, "+
			"so it cannot show session %s.\n"+
			"  drop --session for the demo frame, or `agentpane --oneline --session %s` "+
			"for that pane's live status\n", fl.session, fl.session)
		return fl, fmt.Errorf("--render-once with --session")
	}
	return fl, nil
}

// cmdHook implements `agentpane hook`: read one hook JSON document on stdin,
// forward it to the per-session socket, exit 0 ALWAYS — even on panic —
// because a nonzero exit or stderr noise can block the tool call
// (DATA-SOURCES §3.0).
func cmdHook(stdin io.Reader) (code int) {
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	hooksrc.Forward(stdin, hooksrc.SocketPath) //nolint:errcheck // exit 0 always
	return 0
}

// cmdOneline implements `agentpane --oneline [--session <id>]` (§3.20a):
// read a state file, print one row, exit 0 always — a status bar must never
// see an error.
//
// State files are per session (panes are pinned), so "which pane?" is a real
// question: --session answers it exactly, and without it the newest fresh
// pane wins and the row names its session whenever more than one is live.
func cmdOneline(stdout io.Writer, fl cliFlags) (code int) {
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	var (
		s    statefile.State
		ok   bool
		live int
	)
	if dir, err := statefile.DefaultDir(); err == nil {
		if fl.session != "" {
			s, ok = statefile.ReadSession(dir, fl.session, time.Now())
			live = 1 // the caller named the pane: never ambiguous
		} else {
			s, ok, live = statefile.ReadLatest(dir, time.Now())
		}
	}
	fmt.Fprintln(stdout, statefile.RenderOneline(s, ok, live))
	return 0
}

// configPath is ~/.config/agentpane/config.toml (PART 7), honoring
// $XDG_CONFIG_HOME.
func configPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "agentpane", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "agentpane", "config.toml")
}

// applyOverrides layers the PART 6 flags over the loaded config. Invalid
// values warn and keep the config value (PART 7: never exit).
func applyOverrides(cfg *config.Config, fl cliFlags, warnings *[]config.Warning) {
	if fl.width != "" {
		if fl.width == "wide" || fl.width == "narrow" || fl.width == "fill" {
			cfg.Width = fl.width
		} else {
			*warnings = append(*warnings, config.Warning{Key: "width", Msg: fmt.Sprintf("invalid --width %q - using %q", fl.width, cfg.Width)})
		}
	}
	if fl.accent != "" {
		switch fl.accent {
		case "auto", "2", "3", "4", "5", "6":
			cfg.Accent = fl.accent
		default:
			*warnings = append(*warnings, config.Warning{Key: "accent", Msg: fmt.Sprintf("invalid --accent %q - using %q", fl.accent, cfg.Accent)})
		}
	}
	if fl.noColor || os.Getenv("NO_COLOR") != "" {
		cfg.Color = false
	}
	if fl.noBell {
		cfg.Bell = false
	}
	if fl.noBadge {
		cfg.Badge = false
	}
	if fl.noNotify {
		cfg.Notify = false
	}
	if fl.noLinks {
		cfg.Links = false
	}
}

// resolveAccent runs the §3.10.3 pick: probe unless the config/flag forces an
// index. The returned reasoning is what doctor and the `?` overlay show.
func resolveAccent(cfg config.Config, caps term.Caps) (int, string) {
	if cfg.Accent != "auto" && cfg.Accent != "" {
		if n, err := strconv.Atoi(cfg.Accent); err == nil {
			return n, fmt.Sprintf("accent %d forced by config/flag (auto-pick bypassed)", n)
		}
	}
	return term.AccentPick(caps)
}
