package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
	"github.com/simoncoombes/agentpane/internal/source/registry"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
)

// hookEvents is the complete set `agentpane hook` should observe, in the
// order the snippet prints them (DATA-SOURCES §3, SPEC §1.4).
var hookEvents = []string{
	"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop",
	"SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
	"Notification", "PermissionRequest", "PreCompact", "PostCompact",
}

// cmdDoctor parses the verb's own flags and runs the checks. `doctor
// --session <id>` diagnoses the pane pinned to that id; without it the
// attach/socket lines are labelled as the guess an UNPINNED pane would make,
// because reporting a cwd guess as "the" attach target is a wrong answer on a
// machine where every pane is pinned.
func cmdDoctor(stdout, stderr io.Writer, args []string) int {
	session, rest, err := takeSessionFlag(args)
	if err != nil {
		fmt.Fprintf(stderr, "agentpane: %v\n\nusage:\n%s", err, doctorUsage)
		return 2
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "agentpane: unexpected argument %q\n\nusage:\n%s", rest[0], doctorUsage)
		return 2
	}
	return doctorReport(stdout, session)
}

const doctorUsage = "  agentpane doctor [--session <id>]\n"

// doctorReport prints one ✔/✖/⚠ line per check (PART 6, §3.10.3, PART 10:
// "doctor prints the resolved pair and the accent reasoning"). It never
// writes settings or sockets — nothing under ~/.claude — and its only writes
// anywhere are the state-dir writability probes (writableDir: a temp file
// created and removed under the state and run dirs).
func doctorReport(w io.Writer, session string) int {
	ok := func(f string, a ...any) { fmt.Fprintf(w, "✔ "+f+"\n", a...) }
	bad := func(f string, a ...any) { fmt.Fprintf(w, "✖ "+f+"\n", a...) }
	warn := func(f string, a ...any) { fmt.Fprintf(w, "⚠ "+f+"\n", a...) }

	fmt.Fprintf(w, "agentpane %s doctor\n\n", Version)

	// Go runtime.
	ok("go: %s (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	// Config.
	cfg, warnings := config.Load(configPath())
	if len(warnings) == 0 {
		ok("config: %s (defaults apply for missing keys)", configPath())
	} else {
		warn("config: %d warning(s) in %s — first: %s", len(warnings), configPath(), warnings[0].String())
	}

	// Terminal probe + accent.
	var caps term.Caps
	tty, terr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if terr != nil {
		warn("tty: none (%v) — palette probe skipped; run doctor from the terminal agentpane will live in", terr)
	} else {
		caps = term.Probe(tty)
		tty.Close()
		if caps.SupportsPaletteQuery {
			ok("tty: palette query answered (default fg known: %v, %d indices reported)", caps.HasFG, len(caps.Palette))
		} else {
			warn("tty: palette query unanswered within %s — accent falls back to %d (cyan)", term.ProbeTimeout, term.AccentDefault)
		}
	}
	accent, reason := resolveAccent(cfg, caps)
	switch {
	case accent == term.AccentWeightOnly:
		warn("accent: %s", reason)
		warn("live/primary: no distinguishable index — live renders dim (SGR 2) vs normal-weight primary (§3.10.2a)")
	case !caps.SupportsPaletteQuery && (cfg.Accent == "auto" || cfg.Accent == ""):
		warn("accent: %s", reason)
		ok("live/primary: live=ANSI %d, primary=default fg (SGR 39) — distinct pair", accent)
	default:
		ok("accent: %s", reason)
		ok("live/primary: live=ANSI %d, primary=default fg (SGR 39) — distinct pair", accent)
	}

	// Hooks installation (READ ONLY).
	installed := installedHookEvents()
	if len(installed) > 0 {
		sort.Strings(installed)
		missing := missingHookEvents(installed)
		if len(missing) == 0 {
			ok("hooks: 'agentpane hook' registered for all %d events in %s", len(hookEvents), settingsPath())
		} else {
			warn("hooks: 'agentpane hook' registered for %s — missing %s", strings.Join(installed, ", "), strings.Join(missing, ", "))
		}
	} else {
		bad("hooks: no 'agentpane hook' entry in %s — paste this into its \"hooks\" section:", settingsPath())
		fmt.Fprintln(w)
		fmt.Fprintln(w, hookSnippet())
	}

	// Competing settings (READ ONLY) — the same inspection install.sh runs
	// before writing: symlinked/managed settings, competing subagent
	// visualizers, pane-tool env switches, duplicate alerting, statusLine
	// overlap, plus richer checks on agentpane's own entries.
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  competing settings — read-only scan of %s:\n", settingsPath())
	if raw, rerr := os.ReadFile(settingsPath()); rerr != nil {
		// ReadFile on a symlink whose target is gone also returns ENOENT, so
		// probe the link itself before calling the file absent (install.sh
		// errors on this case: "fix the link first").
		if target, isLink := danglingSymlinkTarget(settingsPath()); os.IsNotExist(rerr) && isLink {
			bad("%s is a symlink to a missing target (%s); fix the link first", settingsPath(), target)
		} else if os.IsNotExist(rerr) {
			ok("no settings file — nothing to scan (the hooks line above shows the snippet to create one)")
		} else {
			bad("cannot read %s: %v", settingsPath(), rerr)
		}
	} else {
		resolved := ""
		if fi, lerr := os.Lstat(settingsPath()); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			if r, eerr := filepath.EvalSymlinks(settingsPath()); eerr == nil {
				resolved = r
			}
		}
		binPath := ""
		if exe, eerr := os.Executable(); eerr == nil {
			binPath = exe
			if r, serr := filepath.EvalSymlinks(exe); serr == nil {
				binPath = r
			}
		}
		for _, f := range InspectSettings(raw, settingsPath(), resolved, binPath) {
			switch f.Level {
			case LevelFail:
				bad("%s", f.Line)
			case LevelWarn:
				warn("%s", f.Line)
			default:
				ok("%s", f.Line)
			}
			for _, d := range f.Detail {
				fmt.Fprintf(w, "    %s\n", d)
			}
		}
	}
	fmt.Fprintln(w)

	// Sessions registry + attach target + socket.
	regDir, regErr := registry.DefaultDir()
	var target registry.Session
	var haveTarget bool
	if regErr != nil {
		bad("registry: cannot resolve ~/.claude/sessions: %v", regErr)
	} else if _, err := os.ReadDir(regDir); err != nil {
		if os.IsNotExist(err) {
			warn("registry: %s does not exist — no Claude Code session has run yet", regDir)
		} else {
			bad("registry: %s unreadable: %v", regDir, err)
		}
	} else {
		reg := registry.New(regDir)
		rows := reg.Sessions()
		ok("registry: %s readable, %d live session(s)", regDir, len(rows))
		cwd, _ := os.Getwd()
		switch {
		case session != "":
			// Pinned: exactly that id, no cwd rule and no fallback — the same
			// no-guess resolution `agentpane --session <id>` itself performs.
			if row, found := sessionByID(rows, session); found {
				target, haveTarget = registry.Follow(rows, row), true
				if target.SessionID != row.SessionID {
					// The pane is not on the id it was opened for, and that is
					// correct: this tab has handed its work to a background
					// job. Say it here, because every other line below now
					// describes the job and not the tab.
					ok("attach target: %s → %s (--session, and that tab has parked its work on background job %s)",
						shortID(row.SessionID), shortID(target.SessionID), row.ParkedJobID)
				} else {
					ok("attach target: %s (--session, exact match in %s)", shortID(row.SessionID), tildeDir(row.CWD))
				}
			} else {
				warn("attach target: session %s is not in the registry — a pane pinned to it waits for that id and never falls back to another session", shortID(session))
			}
		case len(rows) == 0:
			warn("attach target: no live sessions — agentpane would idle, watching the registry")
		default:
			// Unpinned: report the §3.8a guess AS a guess. Every autopane pane
			// is pinned, so this line describes a hand-run `agentpane`, not
			// whatever pane the reader has open.
			if t, found := reg.AttachTarget(cwd); found {
				target, haveTarget = t, true
				ok("attach target (no --session): %s — newest live session for %s, the guess an UNPINNED pane makes; pass --session <id> to check a pinned pane",
					shortID(target.SessionID), tildeDir(cwd))
			} else {
				// The socket check below follows the same fallback the TUI takes.
				target, haveTarget = rows[0], true
				warn("attach target (no --session): none for %s — an unpinned agentpane would fall back to %s (%s)",
					tildeDir(cwd), shortID(target.SessionID), tildeDir(target.CWD))
			}
		}
	}
	// The socket is per session. With --session the check follows the pin even
	// when that session has not reached the registry, because that is exactly
	// the pane whose socket the reader is asking about.
	// The pane listens on the socket of the session it ATTACHED to, which is
	// the background job when the pinned tab has parked its work.
	sockSession := session
	if haveTarget {
		sockSession = target.SessionID
	}
	if sockSession != "" {
		sock := hooksrc.SocketPath(sockSession)
		if _, err := os.Stat(sock); err != nil {
			warn("socket: %s absent — normal unless an agentpane TUI is attached (the TUI creates it)", sock)
		} else if conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err != nil {
			warn("socket: %s exists but is not accepting: %v (stale socket from a dead TUI?)", sock, err)
		} else {
			conn.Close()
			ok("socket: %s reachable (an agentpane TUI is listening)", sock)
		}
	} else {
		warn("socket: no session to check — pass --session <id> for one pane's socket")
	}

	// Transcript dir for cwd.
	cwd, _ := os.Getwd()
	tdir := projectDirFor(cwd)
	if fi, err := os.Stat(tdir); err == nil && fi.IsDir() {
		ok("transcripts: %s exists", tdir)
	} else {
		warn("transcripts: %s not found — the transcript fallback has nothing to tail for this directory", tdir)
	}

	// State dir writable.
	if dir, err := statefile.DefaultDir(); err != nil {
		bad("state dir: cannot resolve: %v", err)
	} else if err := writableDir(dir); err != nil {
		bad("state dir: %s not writable: %v — --oneline and run history need it", dir, err)
	} else {
		ok("state dir: %s writable (current-<session>.json, runs/s-<session>/)", dir)
	}
	if dir, err := runstore.DefaultDir(); err == nil {
		if err := writableDir(dir); err == nil {
			ok("run store: %s writable", dir)
		} else {
			bad("run store: %s not writable: %v", dir, err)
		}
	}

	// Spark metric + fallback.
	ok("spark: %q configured — an agent with no token telemetry falls back to calls at render time; the fallback is visible on the row, not from this script (§3.4)", cfg.Spark)

	// Suppressed agents (§1.5, §13.3 Q8). doctor cannot count them: they exist
	// only in an attached pane's state.Machine, and a one-shot process has no
	// snapshot to read — the counted-but-unrendered agents are never written to
	// disk. What it can do is name the one screen that settles the question,
	// because "+n agents with no recorded activity" is the line people mistrust
	// and nothing in a settings scan confirms or denies it.
	warn("suppressed agents: not countable from a script — they live in the attached pane's model, never on disk")
	fmt.Fprintln(w, "    if \"+n agents with no recorded activity\" looks wrong, select that line in the pane and press ⏎:")
	fmt.Fprintln(w, "    the inspector lists every suppressed agent's id, type, spawn time and last event")
	fmt.Fprintln(w, "    equal spawn and last-event times mean it announced itself and returned; a later last event")
	fmt.Fprintln(w, "    means it did work the row rule never saw, and that one is not a phantom")

	// Glyph coverage.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  glyph sample — every glyph below must render one cell wide, no boxes:")
	// The §13.3 Q5 table, complete and final: ▁▂▃▄▅▆ are withdrawn with the
	// sparkline, so probing them would ask the user to check a font for glyphs
	// nothing draws any more.
	fmt.Fprintln(w, "  ● ⚑ ○ ◇ ◌ ◍ ✔ ✖ ⚠ ▪▫· ▇ ▣ ▌ ▏ █ ◐ ↺ ▸ │├╰─╮ ⠹⠿")
	fmt.Fprintln(w)

	// The terminal backend: what auto-open and ⇥ can drive from here, and
	// whether the program that does the driving is actually installed. This
	// is the check that answers "why does nothing happen when a session
	// starts", which used to need `autopane --explain` to find out.
	fmt.Fprintln(w)
	backend := detectBackend(os.Getenv)
	if backend == backendNone {
		warn("terminal: no split backend — %s", backendReason(os.Getenv))
		fmt.Fprintln(w, "    the TUI works here; auto-open (agentpane install --autopane) and ⇥ do not")
		fmt.Fprintln(w, "    supported: iTerm2, tmux, WezTerm, kitty, Windows Terminal — see docs/TERMINALS.md")
		fmt.Fprintln(w, "    run agentpane in a second pane you split yourself, and answer prompts in the session pane")
	} else {
		// A placeholder invocation: doctor only reads argv[0] of each
		// alternative, to ask whether the program that drives the terminal is
		// installed. What the new pane would run does not come into it.
		chain := splitCommands(backend, os.Getenv, paneRunFor("agentpane", "", nil), 64)
		var missing []string
		for _, c := range chain {
			if _, err := exec.LookPath(c.argv[0]); err != nil {
				missing = append(missing, c.argv[0])
			}
		}
		switch {
		case len(chain) == 0:
			bad("terminal: %s detected, but nothing here names a pane to split", backend)
		case len(missing) == len(chain):
			bad("terminal: %s detected, but none of %s is on PATH — the split cannot run",
				backend, strings.Join(missing, ", "))
		default:
			ok("terminal: %s — auto-open and ⇥ can drive this one", backend)
			if len(missing) > 0 {
				fmt.Fprintf(w, "    %s not on PATH; the next alternative in the chain is used instead\n",
					strings.Join(missing, ", "))
			}
		}
	}

	// Capabilities that cannot be probed one-shot.
	warn("focus reporting (CSI ?1004): cannot verify from a script — check via the pane title test in docs/ITERM2.md")
	warn("OSC 8 hyperlinks: cannot verify from a script — ⌘-click a path in the pane to confirm")
	warn("OSC 52 clipboard: cannot verify from a script — press y in the pane and check the clipboard (iTerm2 gates this behind a preference)")

	return 0
}

// sessionByID finds one registry row by exact session id — the pinned
// resolution, with no cwd rule and no fallback.
func sessionByID(rows []registry.Session, id string) (registry.Session, bool) {
	for _, row := range rows {
		if row.SessionID == id {
			return row, true
		}
	}
	return registry.Session{}, false
}

// missingHookEvents diffs the installed set against hookEvents.
func missingHookEvents(installed []string) []string {
	have := map[string]bool{}
	for _, e := range installed {
		have[e] = true
	}
	var missing []string
	for _, e := range hookEvents {
		if !have[e] {
			missing = append(missing, e)
		}
	}
	return missing
}

// hookSnippet renders the exact settings.json fragment doctor prescribes:
// every event → `agentpane hook`, timeout 5 (SPEC §1.4, PART 8).
func hookSnippet() string {
	var b strings.Builder
	b.WriteString("{\n  \"hooks\": {\n")
	for i, ev := range hookEvents {
		fmt.Fprintf(&b, "    %q: [\n      { \"hooks\": [ { \"type\": \"command\", \"command\": \"agentpane hook\", \"timeout\": 5 } ] }\n    ]", ev)
		if i < len(hookEvents)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  }\n}")
	return b.String()
}

// danglingSymlinkTarget distinguishes a broken settings symlink from a
// genuinely absent file (ReadFile returns ENOENT for both). When p is a
// symlink it reports the link target, made absolute like install.sh's
// realpath (which resolves even when the target is missing).
func danglingSymlinkTarget(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(p)
	if err != nil {
		return p, true
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	return target, true
}

// writableDir verifies dir exists (creating it if needed) and accepts a
// write, cleaning up after itself.
func writableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
