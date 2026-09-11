package main

// autopane.go — `agentpane autopane`, the SessionStart half of auto-open.
// ./install.sh --autopane wires it as one extra SessionStart hook group
// (matcher "startup|resume"). It reads the SessionStart hook JSON on stdin,
// decides whether this session deserves an agentpane split, and when every
// guard passes tells iTerm2 (via osascript) to split the CURRENT pane
// vertically and run the TUI in the new pane.
//
// The pane is PINNED: the command is `<exe> --session <id>` with the id from
// the hook payload, never a bare `agentpane`. A bare TUI attaches by the
// §3.8a rule "newest live session whose cwd matches", which is a coin flip
// when several sessions share a directory — the pane would watch a different
// session than the tab it was split from, and look entirely plausible doing
// it. The id is already in the payload, so there is nothing to guess.
//
// Reaping rules (DATA-SOURCES §3.0): a hook may not background work itself —
// anything it forks is reaped when the hook returns — but ASKING iTerm2 to
// create a pane is fine, because the pane's process belongs to iTerm2, not to
// this hook's process tree. That is exactly how the user-side
// it2-pane-hook.sh has always worked; this file mirrors its techniques:
// the env kill switch (AGENT_PANE_OFF there, AGENTPANE_AUTOPANE=0 here), the
// `${ITERM_SESSION_ID##*:}` prefix strip for pane targeting, and the
// profile-with-fallback split (AGENT_PANE_PROFILE there, AGENTPANE_PROFILE
// here). it2-pane-hook.sh carries NO nested-session or parent-tty guard of
// its own — it fires on SubagentStart, which only ever happens mid-session in
// an environment that is interactive by construction. SessionStart is not:
// it also fires for headless `claude -p` runs and for sessions nested inside
// another Claude session, hence guards (d) and (e) below.
//
// The subcommand is SILENT and exits 0 on every path (it runs inside a
// hook; stderr or a nonzero exit could disturb the session). The decide
// phase stays well under 100ms: one ps(1) exec is the only subprocess before
// the osascript call itself.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
)

const (
	// autopaneOsascriptTimeout bounds the osascript call. On expiry the
	// helper is ABANDONED, never killed — no code path may signal a process
	// (same pattern as editor.go's runQuick post-fix); whatever survives is
	// reaped by the harness's hook-tree cleanup, which is fine because the
	// pane iTerm2 already created belongs to iTerm2.
	autopaneOsascriptTimeout = 3 * time.Second

	// autopaneLockStale is the age past which a leftover lockfile is
	// reclaimed (a crash between lock and split, or a very old session id
	// being reused by --resume long after the pane was closed).
	autopaneLockStale = time.Hour

	// autopaneDialTimeout bounds the guard-(f) socket probe. A unix dial to
	// a live local listener completes in microseconds; this only bites in
	// pathological states, where the conservative answer (treat as
	// attached, skip) is taken.
	autopaneDialTimeout = 200 * time.Millisecond
)

// parentHasTTY reports whether pid has a controlling terminal, via
// `ps -o tty= -p pid` (empty, "??" on macOS, or "?" on Linux → none). It is a
// package var so tests can stub the decision while TestParentHasTTY probes
// the real implementation against setsid-detached fixtures.
//
// Honest limits, established empirically (TestParentHasTTY): a
// setsid-detached process — the state of a fully headless run under cron,
// launchd, CI, or a pipeline with no terminal — reliably reports no tty and
// is skipped. But `claude -p` typed INTO a terminal keeps that terminal as
// its controlling tty (controlling terminals are per session, not per
// stdin), so guard (e) does not filter that case; the AGENTPANE_AUTOPANE=0
// kill switch does. Verified-by-inspection for the real `claude -p` process
// shape — flagged for a live check after install.
var parentHasTTY = func(pid int) bool {
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	tty := strings.TrimSpace(string(out))
	return tty != "" && tty != "??" && tty != "?"
}

// cmdAutopane implements `agentpane autopane`: read one SessionStart hook
// JSON on stdin, decide, act, exit 0 ALWAYS — even on panic — because a
// nonzero exit or output noise from a hook can disturb the session
// (DATA-SOURCES §3.0, same contract as cmdHook). extraArgs are appended to
// the TUI command the new pane runs.
func cmdAutopane(stdin io.Reader, extraArgs []string) (code int) {
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	// --explain runs the SAME guard chain against the current environment and
	// prints each verdict instead of swallowing it, without acquiring the lock
	// or opening a pane. A hook that silently does nothing is undiagnosable,
	// and this is the only way to find out which guard stopped it.
	var explain io.Writer
	rest := extraArgs[:0:0]
	for _, a := range extraArgs {
		switch a {
		case "--explain":
			explain = os.Stdout
			continue
		case "--on-prompt":
			// Consumed by runAutopaneMode, not forwarded to the pane.
			rest = append(rest, a)
			continue
		case "--debug-on", "--debug-off":
			autopaneDebugToggle(a == "--debug-on")
			return 0
		}
		rest = append(rest, a)
	}
	// When run as a real hook there is no stdout to read, so the same trace
	// goes to a log file if the debug marker exists (`agentpane autopane
	// --debug-on` creates it). This is the only way to see why a hook-context
	// invocation skipped; it needs no settings change and stays off by default.
	if explain == nil {
		if f, err := autopaneDebugLog(); err == nil && f != nil {
			defer f.Close()
			fmt.Fprintf(f, "\n=== %s autopane hook fired (ppid %d)\n", time.Now().Format(time.RFC3339), os.Getppid())
			// The log must not change behavior: act for real, just narrated.
			runAutopaneMode(stdin, rest, os.Getppid(), f, false)
			return 0
		}
	}
	runAutopane(stdin, rest, os.Getppid(), explain)
	return 0
}

// autopanePayload is the SessionStart subset this verb reads
// (DATA-SOURCES §3.3).
type autopanePayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
}

// autopaneSkip reports why autopane stopped. The hook path passes a nil
// writer and stays silent (DATA-SOURCES §3.0); --explain passes stdout.
func autopaneSkip(explain io.Writer, format string, args ...any) {
	if explain != nil {
		fmt.Fprintf(explain, "SKIP  "+format+"\n", args...)
	}
}

func autopanePass(explain io.Writer, format string, args ...any) {
	if explain != nil {
		fmt.Fprintf(explain, "ok    "+format+"\n", args...)
	}
}

// runAutopane is the decide-then-act core, separated from cmdAutopane so
// tests can inject the ppid. Every guard failure returns silently unless
// explain is non-nil (--explain), which reports each verdict and never acts.
func runAutopane(stdin io.Reader, extraArgs []string, ppid int, explain io.Writer) {
	// --explain is a DRY run: it reports without taking the lock or opening a
	// pane. The debug log (runAutopaneMode with dry=false) narrates a real one.
	runAutopaneMode(stdin, extraArgs, ppid, explain, explain != nil)
}

// onPromptNote spells out the other accepted event when --on-prompt is on, so a
// skip line says everything that WOULD have been accepted.
func onPromptNote(on bool) string {
	if on {
		return " (or UserPromptSubmit)"
	}
	return ""
}

func runAutopaneMode(stdin io.Reader, extraArgs []string, ppid int, explain io.Writer, dry bool) {
	onPrompt := false
	kept := extraArgs[:0:0]
	for _, a := range extraArgs {
		if a == "--on-prompt" {
			onPrompt = true
			continue
		}
		kept = append(kept, a)
	}
	extraArgs = kept
	// (a) payload parses, event is SessionStart, source startup|resume (the
	// clear/compact/fork sources re-fire mid-session where a pane either
	// already exists or was deliberately closed). A missing session_id also
	// fails here: guards (f) and (g) key off it.
	var p autopanePayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		// Only the DRY --explain path may invent an event. A real invocation
		// (including one being traced to the debug log) must still refuse an
		// unreadable payload, or garbage on stdin would open a pane.
		if !dry {
			autopaneSkip(explain, "(a) payload unreadable: %v", err)
			return
		}
		// No hook payload on stdin (run by hand from a shell): synthesize the
		// event so the environment guards can still be reported.
		p = autopanePayload{SessionID: "explain-no-stdin", HookEventName: "SessionStart", Source: "startup"}
		autopanePass(explain, "(a) payload: none on stdin — using a synthetic SessionStart/startup event")
	}
	// UserPromptSubmit opens a pane too, but only when asked (--on-prompt).
	//
	// Claude Code starts a session — and fires SessionStart — the moment it
	// launches. Codex does not: an interactive Codex sitting at its prompt has
	// no session at all, writes no rollout file, and fires nothing. The session
	// begins with the first thing you ask it, so on that platform the first
	// prompt is the earliest honest moment to open a pane.
	//
	// It is off by default because on Claude Code it would be wrong: a prompt
	// there is not the start of anything, and the pane is already open.
	// Duplicates are the per-session lock's job (guard (g)) either way.
	switch {
	case p.HookEventName == "SessionStart" && p.SessionID != "":
		if p.Source != "startup" && p.Source != "resume" {
			autopaneSkip(explain, "(a) source=%q — only startup|resume open a pane", p.Source)
			return
		}
		autopanePass(explain, "(a) event: SessionStart source=%s session=%s", p.Source, p.SessionID)
	case onPrompt && p.HookEventName == "UserPromptSubmit" && p.SessionID != "":
		autopanePass(explain, "(a) event: UserPromptSubmit session=%s (--on-prompt)", p.SessionID)
	default:
		autopaneSkip(explain, "(a) payload: hook_event_name=%q session_id=%q — want SessionStart with an id%s",
			p.HookEventName, p.SessionID, onPromptNote(onPrompt))
		return
	}

	// (b) kill switch: AGENTPANE_AUTOPANE=0 disables entirely (mirrors
	// it2-pane-hook.sh's AGENT_PANE_OFF=1).
	if os.Getenv("AGENTPANE_AUTOPANE") == "0" {
		autopaneSkip(explain, "(b) AGENTPANE_AUTOPANE=0 — auto-open disabled by the kill switch")
		return
	}
	autopanePass(explain, "(b) kill switch: not set")

	// (c) the session must live in an iTerm2 pane we can target.
	// ITERM_SESSION_ID carries "wNtNpN:<uuid>"; the AppleScript matches the
	// uuid against `id of session`, so strip through the last ":" — the same
	// technique as it2-pane-hook.sh's `${ITERM_SESSION_ID##*:}`.
	if os.Getenv("TERM_PROGRAM") != "iTerm.app" {
		autopaneSkip(explain, "(c) TERM_PROGRAM=%q — auto-open needs iTerm.app (the split is an iTerm2 AppleScript)", os.Getenv("TERM_PROGRAM"))
		return
	}
	paneUUID := os.Getenv("ITERM_SESSION_ID")
	if i := strings.LastIndex(paneUUID, ":"); i >= 0 {
		paneUUID = paneUUID[i+1:]
	}
	if paneUUID == "" {
		autopaneSkip(explain, "(c) ITERM_SESSION_ID empty — no pane to target")
		return
	}
	autopanePass(explain, "(c) iTerm2 pane: %s", paneUUID)

	// (d) nested Claude session: an interactive session spawned from inside
	// another Claude session inherits CLAUDE_CODE_CHILD_SESSION
	// (DATA-SOURCES §4.1). The outer session already owns the pane real
	// estate; a second split per nested session would stack up.
	if parentIsNestedClaude(ppid) {
		autopaneSkip(explain, "(d) parent claude (pid %d) carries CLAUDE_CODE_CHILD_SESSION — nested session, the outer one owns the pane", ppid)
		return
	}
	autopanePass(explain, "(d) parent claude (pid %d) is a top-level session", ppid)

	// (e) headless run: the claude process (our parent) must have a
	// controlling terminal. See the parentHasTTY note for what this does and
	// does not catch.
	if !parentHasTTY(ppid) {
		autopaneSkip(explain, "(e) parent pid %d has no controlling tty — headless run", ppid)
		return
	}
	autopanePass(explain, "(e) parent pid %d has a tty", ppid)

	// (f) a TUI is already attached to this session: something is LISTENING
	// on its socket (the TUI creates it, SPEC §1.4). A bare stat is not
	// enough — a pane closed by iTerm2 SIGHUPs the TUI with no cleanup, so
	// the socket FILE survives, and --resume can reuse the session id later;
	// treating the dead file as "attached" would silently disable auto-open
	// for that session id forever. Dial decides; a stale file is unlinked so
	// the new TUI's listener starts clean.
	sock := hooksrc.SocketPath(p.SessionID)
	switch autopaneSocketState(sock) {
	case socketLive:
		autopaneSkip(explain, "(f) a TUI already listens on %s", sock)
		return
	case socketStale:
		autopanePass(explain, "(f) stale socket at %s — unlinking and continuing", sock)
		if !dry {
			os.Remove(sock) //nolint:errcheck // listenUnix re-unlinks anyway
		}
	default:
		autopanePass(explain, "(f) no TUI attached")
	}

	// (g) per-session lockfile: O_CREATE|O_EXCL is the atomic "first one
	// wins" so rapid duplicate SessionStart events cannot double-open. On a
	// SUCCESSFUL act the lock is left in place deliberately — after the
	// split, guard (f) takes over — and a stale lock older than
	// autopaneLockStale is reclaimed. But an act that fails before osascript
	// even starts (executable path unresolvable, osascript missing, macOS
	// automation permission denied at exec) must release the lock, or the
	// failure would silently disable retry for the whole stale window.
	lock := autopaneLockPath(p.SessionID)
	if dry {
		// Never take the lock in explain mode: acquiring it here would make a
		// diagnostic run suppress the very auto-open it is diagnosing.
		if _, err := os.Stat(lock); err == nil {
			autopanePass(explain, "(g) lock %s exists — a real event would skip unless it is over %v old", lock, autopaneLockStale)
		} else {
			autopanePass(explain, "(g) no lock at %s", lock)
		}
	} else if !autopaneLockAcquire(lock, time.Now()) {
		autopaneSkip(explain, "(g) another invocation holds the lock %s", lock)
		return
	}

	// ACT: split the current pane. The new pane runs this same binary (the
	// plain TUI / attach flow) by absolute path — hooks and iTerm2 command
	// panes both run with an unpredictable PATH.
	exe, err := os.Executable()
	if err != nil {
		autopaneSkip(explain, "act: cannot resolve my own path: %v", err)
		if !dry {
			os.Remove(lock) //nolint:errcheck // best-effort release, stay silent
		}
		return
	}
	cmdline := paneCommand(exe, p.SessionID, extraArgs)
	script := autopaneScript(paneUUID, autopaneProfile(), cmdline)
	osascript := os.Getenv("AGENTPANE_OSASCRIPT")
	if osascript == "" {
		osascript = "osascript"
	}
	if dry {
		fmt.Fprintf(explain, "\nWOULD OPEN a pane: profile %q running %s\n", autopaneProfile(), cmdline)
		fmt.Fprintf(explain, "via %s -e <AppleScript targeting session %s>\n", osascript, paneUUID)
		if _, err := exec.LookPath(osascript); err != nil {
			fmt.Fprintf(explain, "SKIP  act: %s not found on PATH: %v\n", osascript, err)
		}
		return
	}
	autopanePass(explain, "act: running %s to split pane %s (profile %q)", osascript, paneUUID, autopaneProfile())
	if !runAbandoning(osascript, autopaneOsascriptTimeout, "-e", script) {
		autopaneSkip(explain, "act: %s failed to start — lock released", osascript)
		os.Remove(lock) //nolint:errcheck // best-effort release, stay silent
	} else {
		autopanePass(explain, "act: osascript started — pane should appear")
	}
}

// socketState is the guard-(f) probe result for the per-session TUI socket.
type socketState int

const (
	socketAbsent socketState = iota // no file — nothing attached
	socketLive                      // a listener accepted the dial
	socketStale                     // a file exists but nothing listens
)

// autopaneSocketState dials the socket to learn whether a TUI actually
// listens there. ECONNREFUSED (or a non-socket file squatting on the path)
// means a dead leftover → stale; a vanished file means absent; any other
// dial failure is treated as live — the conservative reading, since a wrong
// "stale" risks a second pane while a wrong "live" only skips this event.
func autopaneSocketState(path string) socketState {
	if _, err := os.Lstat(path); err != nil {
		return socketAbsent
	}
	conn, err := net.DialTimeout("unix", path, autopaneDialTimeout)
	if err == nil {
		conn.Close()
		return socketLive
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTSOCK) {
		return socketStale
	}
	if errors.Is(err, os.ErrNotExist) {
		return socketAbsent
	}
	return socketLive
}

// autopaneProfile is the iTerm2 profile for the new pane: $AGENTPANE_PROFILE,
// default "AgentPane" (docs/ITERM2.md ships it via Dynamic Profiles). The
// AppleScript falls back to the session's own profile when it is missing.
func autopaneProfile() string {
	if p := os.Getenv("AGENTPANE_PROFILE"); p != "" {
		return p
	}
	return "AgentPane"
}

// paneCommand builds the command string the new pane runs:
// `<exe> --session <id> [extraArgs...]` under /bin/sh -c with everything
// shell-quoted, so an executable path or argument containing spaces survives
// iTerm2's own command splitting (same shape as editor.go's iTermTabScript).
//
// --session is what pins the pane to the session the hook fired for. The id
// is shell-quoted like everything else: it is untrusted payload input (C9),
// and main.go's flag parser is the only thing that interprets it.
func paneCommand(exe, sessionID string, extraArgs []string) string {
	parts := make([]string, 0, 3+len(extraArgs))
	parts = append(parts, shellQuote(exe))
	if sessionID != "" {
		parts = append(parts, "--session", shellQuote(sessionID))
	}
	for _, a := range extraArgs {
		parts = append(parts, shellQuote(a))
	}
	return "/bin/sh -c " + shellQuote(strings.Join(parts, " "))
}

// autopaneScript builds the AppleScript: find the session whose id equals
// the (already prefix-stripped) uuid from ITERM_SESSION_ID, split it
// VERTICALLY with the wanted profile — falling back to the session's own
// profile when that profile does not exist — running command in the new
// pane. Splitting the SESSION (not "current window") targets the pane the
// hook fired in even when the user has focused another window meanwhile.
func autopaneScript(sessionUUID, profile, command string) string {
	u := appleScriptEscape(sessionUUID)
	pr := appleScriptEscape(profile)
	cmd := appleScriptEscape(command)
	// iTerm2 splits 50/50, which is far wider than the TUI needs (SPEC §3.1:
	// wide is 64 columns, narrow 44). Resize the new pane afterwards, clamped
	// so a small window never starves the Claude pane and never drops the TUI
	// below its narrow layout. Resizing is best-effort: a failure leaves the
	// 50/50 pane, which still works.
	want := strconv.Itoa(autopaneColumns())
	minCols := strconv.Itoa(autopaneMinColumns)
	keep := strconv.Itoa(autopaneKeepColumns)
	return `tell application "iTerm2"
	repeat with theWindow in windows
		repeat with theTab in tabs of theWindow
			repeat with theSession in sessions of theTab
				if id of theSession is equal to "` + u + `" then
					set parentCols to columns of theSession
					set target to ` + want + `
					if target > (parentCols - ` + keep + `) then set target to (parentCols - ` + keep + `)
					if target < ` + minCols + ` then set target to ` + minCols + `
					tell theSession
						try
							set newSession to (split vertically with profile "` + pr + `" command "` + cmd + `")
						on error
							set newSession to (split vertically with same profile command "` + cmd + `")
						end try
					end tell
					-- Resizing the PARENT to (total - target) moves the divider
					-- and lets the new pane take the remainder; shrinking the new
					-- pane alone would leave dead width instead. But that write
					-- lands while iTerm2 is still laying the split out, so on its
					-- own it is a race: the same script produced panes of 59, 78,
					-- 86, 91, 126 and 149 columns across six tabs when 64 was
					-- asked for. So verify and correct, then confirm — the direct
					-- write to the new pane is the deterministic one, and by the
					-- time it runs the divider has already moved, so it costs no
					-- dead space. Widths come from pixels, so accept ±2.
					try
						set columns of theSession to (parentCols - target)
						repeat 3 times
							if (columns of newSession) - target > 2 or target - (columns of newSession) > 2 then
								delay 0.08
								set columns of newSession to target
							end if
						end repeat
					end try
					return
				end if
			end repeat
		end repeat
	end repeat
end tell`
}

// autopaneLockPath is os.TempDir()/agentpane-autopane-<session>.lock, with
// the session id sanitized the same way hooksrc.SocketPath keeps its socket
// name inside TempDir (never trust input, C9).
func autopaneLockPath(sessionID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		}
		return '-'
	}, sessionID)
	return filepath.Join(os.TempDir(), "agentpane-autopane-"+safe+".lock")
}

// autopaneLockAcquire atomically creates the lockfile (O_CREATE|O_EXCL). A
// pre-existing lock younger than autopaneLockStale means another autopane
// invocation won the race — report false. An older one is reclaimed once,
// via autopaneLockReclaim.
func autopaneLockAcquire(path string, now time.Time) bool {
	if autopaneLockCreate(path) {
		return true
	}
	fi, err := os.Stat(path)
	if err != nil || now.Sub(fi.ModTime()) < autopaneLockStale {
		return false
	}
	return autopaneLockReclaim(path, now)
}

func autopaneLockCreate(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// autopaneLockReclaim takes over a lock the caller has already stat'ed as
// stale. Plain remove+create has a TOCTOU: between the caller's stat and its
// remove, a racer can complete its OWN reclaim and create a FRESH lock,
// which the remove then unlinks — two winners, two panes. Instead the stale
// file is RENAMED to a unique graveyard name (atomic; exactly one racer's
// rename of the original succeeds) and then VERIFIED: if the file that
// arrived in the graveyard is fresh, a racer's new lock was grabbed by
// mistake — put it back and yield. Only a verified-stale capture proceeds to
// create. Two concurrent reclaims of the same stale lock now resolve to
// exactly one winner; a third simultaneous racer can in principle still slip
// through the give-back window, which is accepted (requires an hour-old
// leftover plus three simultaneous SessionStart events).
func autopaneLockReclaim(path string, now time.Time) bool {
	graveyard := path + ".reclaim." + strconv.Itoa(os.Getpid()) + "." + strconv.FormatInt(now.UnixNano(), 10)
	if os.Rename(path, graveyard) != nil {
		return false // a racer reclaimed first
	}
	fi, err := os.Stat(graveyard)
	if err != nil || now.Sub(fi.ModTime()) < autopaneLockStale {
		// Fresh (or unverifiable): this was a racer's live lock — restore
		// it and lose the race. If the restore itself fails, at least drop
		// the graveyard file so nothing lingers in TempDir.
		if err == nil && os.Rename(graveyard, path) == nil {
			return false
		}
		os.Remove(graveyard) //nolint:errcheck // best-effort cleanup
		return false
	}
	os.Remove(graveyard) //nolint:errcheck // best-effort cleanup
	return autopaneLockCreate(path)
}

// runAbandoning starts name and waits at most timeout, reporting whether the
// process STARTED (false only when exec itself failed — binary missing, not
// executable). On expiry the process is abandoned WITHOUT being killed — no
// code path anywhere may signal a process (§5.3, PART 10) — and the Wait
// goroutine reaps it whenever it eventually finishes. stdout/stderr stay nil
// (silent on every path).
func runAbandoning(name string, timeout time.Duration, args ...string) bool {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait() //nolint:errcheck // silent: outcome does not matter
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	return true
}

// autopaneDebugPath is the trace log written when the debug marker exists.
func autopaneDebugPath() (marker, logPath string, err error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	base := filepath.Join(dir, ".local", "state", "agentpane")
	return filepath.Join(base, "autopane-debug"), filepath.Join(base, "autopane.log"), nil
}

// autopaneDebugLog opens the trace log if and only if the debug marker file
// exists. A hook that silently does nothing is undiagnosable, and the hook has
// no stdout anyone reads; `agentpane autopane --debug-on` creates the marker,
// --debug-off removes it. Returns (nil, nil) when debugging is off.
func autopaneDebugLog() (*os.File, error) {
	marker, logPath, err := autopaneDebugPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(marker); err != nil {
		return nil, nil //nolint:nilnil // "debug off" is not an error
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// The mode above applies only on creation, and earlier versions created
	// this file 0644. It records session ids and working directories.
	f.Chmod(0o600) //nolint:errcheck // best-effort
	return f, nil
}

const (
	// autopaneDefaultColumns is the pane width the TUI is designed for
	// (SPEC §3.1 wide layout). iTerm2's own split is 50/50, which is far
	// wider than the tree ever uses.
	autopaneDefaultColumns = 64

	// autopaneMinColumns is the narrow layout (SPEC §3.1) — the floor the
	// pane is never resized below, however small the window.
	autopaneMinColumns = 44

	// autopaneKeepColumns is what the Claude pane keeps: on a window too
	// narrow for both, the TUI yields rather than squeezing the session it
	// is watching.
	autopaneKeepColumns = 80
)

// autopaneColumns is the requested pane width: $AGENTPANE_COLUMNS, else the
// wide layout. Values below the narrow layout are raised to it; the
// AppleScript clamps again against the actual window.
func autopaneColumns() int {
	if v := os.Getenv("AGENTPANE_COLUMNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n < autopaneMinColumns {
				return autopaneMinColumns
			}
			return n
		}
	}
	return autopaneDefaultColumns
}

// autopaneDebugToggle creates or removes the trace marker. Printing here is
// safe: --debug-on/--debug-off are only ever typed by hand, never by a hook.
func autopaneDebugToggle(on bool) {
	marker, logPath, err := autopaneDebugPath()
	if err != nil {
		return
	}
	if !on {
		os.Remove(marker) //nolint:errcheck // best-effort
		fmt.Printf("autopane tracing off (log kept at %s)\n", logPath)
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		fmt.Printf("cannot enable tracing: %v\n", err)
		return
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Printf("cannot enable tracing: %v\n", err)
		return
	}
	f.Close()
	fmt.Printf("autopane tracing on — next session start writes %s\n", logPath)
}

// parentIsNestedClaude reports whether the claude process that spawned this
// hook is itself running INSIDE another Claude session.
//
// Do not test our own environment for this. Claude Code injects
// CLAUDE_CODE_CHILD_SESSION=1 into every process it spawns — hooks and Bash
// tool calls alike — so reading it here is always true and would disable
// auto-open completely (observed live: a top-level `claude` on ttys005 had no
// such variable, while the hook it spawned did). The marker is only meaningful
// on the CLAUDE process: a claude launched from inside another session
// inherits it from the parent's tool shell, which is the genuine nesting case
// DATA-SOURCES §4.1 describes.
//
// `ps eww` prints a process's environment (same-user processes only, which is
// always our case). When the environment cannot be read the answer is "not
// nested": a false skip breaks the feature entirely, while a false open costs
// one extra pane in a rare nested session.
var parentIsNestedClaude = func(pid int) bool {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "CLAUDE_CODE_CHILD_SESSION=")
}
