package main

// autopane_test.go — guard-by-guard coverage for `agentpane autopane`. The
// osascript side effect is observed through a fake binary (AGENTPANE_OSASCRIPT
// points at a shell script that writes its argv to a file), so no test ever
// opens a real iTerm2 pane. Verification method per guard:
//
//   (a) payload/event/source  — stdin fixtures, fake NOT invoked
//   (b) AGENTPANE_AUTOPANE=0  — env fixture, fake NOT invoked
//   (c) TERM_PROGRAM/ITERM_SESSION_ID — env fixtures, fake NOT invoked
//   (d) nested parent claude — stubbed probe, fake NOT invoked
//   (e) parent tty            — decision stubbed here (fake NOT invoked when
//                               false); the REAL ps(1) probe is tested
//                               empirically in TestParentHasTTY against a
//                               setsid-detached process (no controlling
//                               terminal — the headless `claude -p` shape)
//   (f) TUI attached          — a LIVE listener skips (fake NOT invoked); a
//                               STALE socket file (dead pane + --resume) is
//                               unlinked and the pane opens
//   (g) lockfile              — fresh lock skips, stale (>1h) reclaims,
//                               rapid duplicate events open once; a failed
//                               act releases the lock so retry works
//   pass — fake IS invoked; the AppleScript carries the stripped session
//          uuid, the executable path, `--session <id>` (the pin), the
//          profile, and the fallback clause

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
)

const autopaneTestSession = "292ef2e2-abcd-4444-9999-0123456789ab"

func autopanePayloadJSON(event, source, session string) string {
	return `{"session_id":"` + session + `","hook_event_name":"` + event + `","source":"` + source + `"}`
}

// autopaneShortSession is used by the fixtures that bind a REAL unix
// listener at hooksrc.SocketPath: the darwin sun_path limit is 104 bytes,
// so those paths must stay short (same concern as hooksrc's shortSocket).
const autopaneShortSession = "s1"

// setupAutopaneEnv builds the all-guards-pass environment: isolated TMPDIR,
// iTerm2 env, kill switch off, fake osascript, stubbed tty check. It returns
// the path the fake writes its argv to (existence == invoked). The TMPDIR is
// a SHORT MkdirTemp dir, not t.TempDir(): subtest-named tempdirs push socket
// paths past the darwin sun_path limit for the real-listener fixtures.
func setupAutopaneEnv(t *testing.T) (argvFile string) {
	t.Helper()
	tmp, err := os.MkdirTemp("", "ap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	setFakeTemp(t, tmp+"/") // os.TempDir(), hooksrc.SocketPath, the lock
	// HOME too: the autopane debug marker lives under ~/.local/state, and a
	// real one on the developer's machine would otherwise divert these runs
	// down the traced path (and append to their live log).
	setFakeHome(t, tmp)
	argvFile = filepath.Join(tmp, "osascript-argv.txt")
	fake := filepath.Join(tmp, "fake-osascript")
	writeFakeProgram(t, fake, "printf '%s\\n' \"$@\" > \"$AUTOPANE_TEST_ARGV\"\n")
	t.Setenv("AUTOPANE_TEST_ARGV", argvFile)
	t.Setenv("AGENTPANE_OSASCRIPT", fake)
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("ITERM_SESSION_ID", "w0t2p0:ABCD-1234-EF56")
	t.Setenv("AGENTPANE_AUTOPANE", "1")
	t.Setenv("AGENTPANE_PROFILE", "")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "")
	// Claude Code injects CLAUDE_CODE_CHILD_SESSION into every process it
	// spawns, so `go test` run from a Claude session would otherwise look
	// nested to guard (d). The real probe is exercised by TestParentIsNestedClaude.
	prevNested := parentIsNestedClaude
	parentIsNestedClaude = func(int) bool { return false }
	t.Cleanup(func() { parentIsNestedClaude = prevNested })

	old := parentHasTTY
	parentHasTTY = func(int) bool { return true }
	t.Cleanup(func() { parentHasTTY = old })
	return argvFile
}

func autopaneInvoked(argvFile string) bool {
	_, err := os.Stat(argvFile)
	return err == nil
}

// TestAutopanePass: every guard passes → the fake osascript runs once with
// -e and an AppleScript containing the prefix-stripped session uuid, the
// executable path, the vertical split, the default profile, and the
// same-profile fallback; the per-session lock is left behind.
func TestAutopanePass(t *testing.T) {
	argv := setupAutopaneEnv(t)
	in := strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession))
	if code := cmdAutopane(in, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !autopaneInvoked(argv) {
		t.Fatal("osascript not invoked on the all-pass path")
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.HasPrefix(got, "-e\n") {
		t.Fatalf("first osascript arg must be -e, got:\n%s", got)
	}
	exe, _ := os.Executable()
	for _, want := range []string{
		`"ABCD-1234-EF56"`,  // uuid stripped of the wNtNpN: prefix
		exe,                 // absolute path to self
		"--session",         // the pin
		autopaneTestSession, // ...carrying the payload's own session id
		"split vertically with profile \"AgentPane\"", // default profile
		"split vertically with same profile",          // fallback clause
		"tell application \"iTerm2\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("AppleScript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "w0t2p0") {
		t.Fatalf("window/tab/pane prefix leaked into the AppleScript:\n%s", got)
	}
	// The pane must never run a bare TUI: without --session it would attach
	// by the §3.8a cwd guess and can land on a sibling session.
	if i := strings.Index(got, exe); i >= 0 && !strings.Contains(got[i:], "--session") {
		t.Fatalf("pane command runs the TUI without --session:\n%s", got)
	}
	if _, err := os.Stat(autopaneLockPath(autopaneTestSession)); err != nil {
		t.Fatalf("lockfile not left in place: %v", err)
	}
}

// TestAutopaneProfileAndArgs: AGENTPANE_PROFILE overrides the profile, and
// extra args ride along into the pane command.
func TestAutopaneProfileAndArgs(t *testing.T) {
	argv := setupAutopaneEnv(t)
	t.Setenv("AGENTPANE_PROFILE", "MyMonitor")
	in := strings.NewReader(autopanePayloadJSON("SessionStart", "resume", autopaneTestSession))
	if code := cmdAutopane(in, []string{"--width", "narrow"}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("osascript not invoked: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `with profile "MyMonitor"`) {
		t.Fatalf("AGENTPANE_PROFILE not honored:\n%s", got)
	}
	if !strings.Contains(got, "--width") || !strings.Contains(got, "narrow") {
		t.Fatalf("extra args missing from the pane command:\n%s", got)
	}
	// The pin comes first and the configured extras ride behind it.
	sess := strings.Index(got, "--session")
	width := strings.Index(got, "--width")
	if sess < 0 || width < 0 || sess > width {
		t.Fatalf("--session must precede the configured extra args:\n%s", got)
	}
}

// TestPaneCommand: the exact command the new pane runs. Everything is
// single-quoted for /bin/sh, the session id is pinned right after the
// executable, and a hostile id cannot break out of its quoting.
func TestPaneCommand(t *testing.T) {
	cases := []struct {
		name    string
		exe     string
		session string
		extra   []string
		want    string
	}{
		{"pinned", "/opt/bin/agentpane", "3f9c-1a2b", nil,
			`/bin/sh -c ''\''/opt/bin/agentpane'\'' --session '\''3f9c-1a2b'\'''`},
		{"pinned with extras", "/opt/bin/agentpane", "3f9c", []string{"--width", "narrow"},
			`/bin/sh -c ''\''/opt/bin/agentpane'\'' --session '\''3f9c'\'' '\''--width'\'' '\''narrow'\'''`},
		{"no session id degrades to the plain TUI", "/opt/bin/agentpane", "", nil,
			`/bin/sh -c ''\''/opt/bin/agentpane'\'''`},
		{"path with spaces", "/opt/my bin/agentpane", "3f9c", nil,
			`/bin/sh -c ''\''/opt/my bin/agentpane'\'' --session '\''3f9c'\'''`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := paneCommand(c.exe, c.session, c.extra); got != c.want {
				t.Fatalf("paneCommand = %s\nwant %s", got, c.want)
			}
		})
	}

	// A hostile session id stays inside its quotes: the injected `;` and the
	// closing quote are neutralised by shellQuote's '\'' escape, so the shell
	// still sees exactly one --session argument.
	got := paneCommand("/x/agentpane", "a'; rm -rf ~; echo '", nil)
	if strings.Contains(got, "; rm -rf ~; echo") && !strings.Contains(got, `'\''`) {
		t.Fatalf("hostile session id escaped its quoting: %s", got)
	}
	if !strings.HasPrefix(got, "/bin/sh -c '") || !strings.HasSuffix(got, "'") {
		t.Fatalf("hostile session id broke the outer quoting: %s", got)
	}
}

// TestAutopaneGuards: each guard's skip path, asserted by the fake osascript
// NOT having been invoked.
func TestAutopaneGuards(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
		prep  func(t *testing.T)
	}{
		{"a: malformed JSON", "{ not json", nil},
		{"a: empty stdin", "", nil},
		{"a: wrong event", autopanePayloadJSON("SessionEnd", "startup", autopaneTestSession), nil},
		{"a: source clear", autopanePayloadJSON("SessionStart", "clear", autopaneTestSession), nil},
		{"a: source compact", autopanePayloadJSON("SessionStart", "compact", autopaneTestSession), nil},
		{"a: empty session_id", autopanePayloadJSON("SessionStart", "startup", ""), nil},
		{"b: kill switch", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) { t.Setenv("AGENTPANE_AUTOPANE", "0") }},
		{"c: not iTerm2", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) { t.Setenv("TERM_PROGRAM", "Apple_Terminal") }},
		{"c: no ITERM_SESSION_ID", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) { t.Setenv("ITERM_SESSION_ID", "") }},
		{"c: empty uuid after prefix", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) { t.Setenv("ITERM_SESSION_ID", "w0t0p0:") }},
		{"d: nested claude session", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) {
				prev := parentIsNestedClaude
				parentIsNestedClaude = func(int) bool { return true }
				t.Cleanup(func() { parentIsNestedClaude = prev })
			}},
		{"e: parent has no tty", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) {
				old := parentHasTTY
				parentHasTTY = func(int) bool { return false }
				t.Cleanup(func() { parentHasTTY = old })
			}},
		{"f: TUI attached (live socket)", autopanePayloadJSON("SessionStart", "startup", autopaneShortSession),
			func(t *testing.T) {
				ln, err := net.Listen("unix", hooksrc.SocketPath(autopaneShortSession))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { ln.Close() })
			}},
		{"g: fresh lock held", autopanePayloadJSON("SessionStart", "startup", autopaneTestSession),
			func(t *testing.T) {
				if err := os.WriteFile(autopaneLockPath(autopaneTestSession), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv := setupAutopaneEnv(t)
			if c.prep != nil {
				c.prep(t)
			}
			if code := cmdAutopane(strings.NewReader(c.stdin), nil); code != 0 {
				t.Fatalf("exit %d, want 0 (guards must stay silent successes)", code)
			}
			if autopaneInvoked(argv) {
				t.Fatalf("osascript WAS invoked despite guard %q", c.name)
			}
		})
	}

	// nil stdin: the recover path still exits 0.
	setupAutopaneEnv(t)
	if code := cmdAutopane(nil, nil); code != 0 {
		t.Fatalf("nil stdin: exit %d, want 0", code)
	}
}

// TestAutopaneLock: a rapid duplicate event does not double-open; a stale
// lock (>1h) is reclaimed and the pane opens.
func TestAutopaneLock(t *testing.T) {
	argv := setupAutopaneEnv(t)

	// First event wins.
	cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession)), nil)
	if !autopaneInvoked(argv) {
		t.Fatal("first event must open the pane")
	}
	if err := os.Remove(argv); err != nil {
		t.Fatal(err)
	}

	// Rapid duplicate: lock exists (and no socket yet) → skip.
	cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession)), nil)
	if autopaneInvoked(argv) {
		t.Fatal("duplicate event double-opened despite the lock")
	}

	// Stale lock: backdate 2h → reclaimed, pane opens again.
	lock := autopaneLockPath(autopaneTestSession)
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession)), nil)
	if !autopaneInvoked(argv) {
		t.Fatal("stale lock not reclaimed")
	}
}

// TestAutopaneStaleSocket: a socket FILE with nothing listening — the state
// left behind when iTerm2 closes the TUI's pane (SIGHUP, no cleanup) and
// --resume later reuses the session id — must not block auto-open. The stale
// file is unlinked and the pane opens.
func TestAutopaneStaleSocket(t *testing.T) {
	argv := setupAutopaneEnv(t)
	sock := hooksrc.SocketPath(autopaneShortSession)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// Leave a genuine dead socket file: disable the automatic unlink, then
	// close — exactly the artifact a SIGHUP'd TUI leaves behind.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("fixture: dead socket file missing: %v", err)
	}

	if code := cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneShortSession)), nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !autopaneInvoked(argv) {
		t.Fatal("stale socket blocked auto-open (guard f treated a dead file as attached)")
	}
	if _, err := os.Lstat(sock); err == nil {
		t.Fatal("stale socket file not unlinked")
	}
}

// TestAutopaneFailedActReleasesLock: when the act phase fails before
// osascript starts (binary missing — the same shape as a first-run macOS
// automation denial at exec), the lock must be released so the user's next
// session start can retry, instead of silently doing nothing for the whole
// stale window.
func TestAutopaneFailedActReleasesLock(t *testing.T) {
	argv := setupAutopaneEnv(t)
	t.Setenv("AGENTPANE_OSASCRIPT", filepath.Join(filepath.Dir(argv), "does-not-exist"))

	in := strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession))
	if code := cmdAutopane(in, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if autopaneInvoked(argv) {
		t.Fatal("fake osascript invoked despite the nonexistent-binary fixture")
	}
	if _, err := os.Stat(autopaneLockPath(autopaneTestSession)); err == nil {
		t.Fatal("lock left behind after a failed act — retry disabled for the stale window")
	}

	// Retry with the working fake now succeeds immediately.
	t.Setenv("AGENTPANE_OSASCRIPT", filepath.Join(filepath.Dir(argv), "fake-osascript"))
	cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession)), nil)
	if !autopaneInvoked(argv) {
		t.Fatal("retry after a failed act did not open the pane")
	}
}

// TestAutopaneLockReclaim: the rename-and-verify reclaim. A verified-stale
// capture wins and leaves a fresh lock; capturing a FRESH file (a racer's
// live lock grabbed between our stat and rename) yields and RESTORES it.
func TestAutopaneLockReclaim(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now()

	// Stale capture: reclaim succeeds, a fresh lock replaces the old one,
	// no graveyard file lingers.
	stalePath := filepath.Join(tmp, "stale.lock")
	if err := os.WriteFile(stalePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatal(err)
	}
	if !autopaneLockReclaim(stalePath, now) {
		t.Fatal("stale lock not reclaimed")
	}
	fi, err := os.Stat(stalePath)
	if err != nil {
		t.Fatalf("no fresh lock after reclaim: %v", err)
	}
	if now.Sub(fi.ModTime()) >= autopaneLockStale {
		t.Fatal("reclaimed lock still carries the stale mtime")
	}

	// Fresh capture (the TOCTOU shape): the file at path turns out to be a
	// racer's live lock — reclaim must yield AND put it back.
	freshPath := filepath.Join(tmp, "fresh.lock")
	if err := os.WriteFile(freshPath, []byte("racer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if autopaneLockReclaim(freshPath, now) {
		t.Fatal("reclaim stole a racer's fresh lock and won")
	}
	raw, err := os.ReadFile(freshPath)
	if err != nil {
		t.Fatalf("racer's fresh lock not restored: %v", err)
	}
	if string(raw) != "racer" {
		t.Fatalf("restored lock is not the racer's original (got %q)", raw)
	}

	// Nothing but the two locks may remain (no graveyard leftovers).
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("graveyard leftovers: %v", names)
	}
}

// TestAutopaneAbandon: an osascript that outlives the 3s budget is abandoned
// — cmdAutopane returns at ~3s — and NOT killed: the helper finishes its
// script afterwards (the done marker appears ~1s after abandonment).
func TestAutopaneAbandon(t *testing.T) {
	argv := setupAutopaneEnv(t)
	tmp := filepath.Dir(argv)
	done := filepath.Join(tmp, "slow-done")
	slow := filepath.Join(tmp, "slow-osascript")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AUTOPANE_TEST_ARGV\"\nsleep 4\ntouch \"" + done + "\"\n"
	if err := os.WriteFile(slow, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTPANE_OSASCRIPT", slow)

	start := time.Now()
	cmdAutopane(strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession)), nil)
	elapsed := time.Since(start)
	if elapsed < 2500*time.Millisecond || elapsed > 3900*time.Millisecond {
		t.Fatalf("returned after %v, want ~3s (the abandon budget)", elapsed)
	}
	// Not killed: the helper's post-sleep marker still lands.
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned osascript never finished — was it killed?")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAutopaneLockPathHostile: a hostile session id cannot escape TempDir.
func TestAutopaneLockPathHostile(t *testing.T) {
	tmp := t.TempDir()
	setFakeTemp(t, tmp)
	p := autopaneLockPath("../../etc/x")
	if filepath.Dir(p) != filepath.Clean(tmp) {
		t.Fatalf("lock path escaped TempDir: %s", p)
	}
	if strings.Contains(filepath.Base(p), "/") {
		t.Fatalf("separator survived sanitization: %s", p)
	}
}

// --on-prompt: Codex has no session until you ask it something, so the first
// prompt is the earliest honest moment to open a pane there. It is off by
// default because on Claude Code a prompt is not the start of anything.
func TestAutopaneOpensOnFirstPromptOnlyWhenAsked(t *testing.T) {
	payload := `{"session_id":"01a0-test","hook_event_name":"UserPromptSubmit","cwd":"/tmp","prompt":"hi"}`

	var on, off bytes.Buffer
	runAutopaneMode(strings.NewReader(payload), []string{"--on-prompt", "--source", "codex"}, os.Getppid(), &on, true)
	runAutopaneMode(strings.NewReader(payload), []string{"--source", "codex"}, os.Getppid(), &off, true)

	if !strings.Contains(on.String(), "(a) event: UserPromptSubmit") {
		t.Errorf("--on-prompt did not accept a first prompt:\n%s", on.String())
	}
	if !strings.Contains(off.String(), "SKIP  (a)") {
		t.Errorf("a prompt opened a pane without --on-prompt:\n%s", off.String())
	}
	// The flag is autopane's own and must never reach the pane's command line.
	if strings.Contains(on.String(), "--on-prompt") && strings.Contains(on.String(), "agentpane --session") {
		t.Errorf("--on-prompt leaked into the pane command:\n%s", on.String())
	}

	// SessionStart still works exactly as before.
	var start bytes.Buffer
	runAutopaneMode(strings.NewReader(
		`{"session_id":"01a0-test","hook_event_name":"SessionStart","source":"startup"}`),
		[]string{"--on-prompt"}, os.Getppid(), &start, true)
	if !strings.Contains(start.String(), "(a) event: SessionStart") {
		t.Errorf("SessionStart stopped being accepted:\n%s", start.String())
	}
}

// literalAt returns the AppleScript string literal that starts at the quote
// following prefix, honouring backslash escapes — i.e. what osascript itself
// would read as one string.
func literalAt(t *testing.T, script, prefix string) string {
	t.Helper()
	i := strings.Index(script, prefix)
	if i < 0 {
		t.Fatalf("script has no %q:\n%s", prefix, script)
	}
	rest := script[i+len(prefix):]
	var out []byte
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
			if j < len(rest) {
				out = append(out, rest[j])
			}
		case '"':
			return string(out)
		default:
			out = append(out, rest[j])
		}
	}
	t.Fatalf("unterminated literal after %q:\n%s", prefix, script)
	return ""
}

// The AppleScript is built by concatenation, and two of the values it embeds
// come from outside the process: the pane id out of $ITERM_SESSION_ID and the
// session id out of the hook payload. Neither can close the string literal it
// sits in and start writing AppleScript of its own.
func TestAutopaneScriptEscapesHostileInput(t *testing.T) {
	hostileID := `a" & (do shell script "touch /tmp/pwned") & "b`
	cmd := paneCommand("/x/agentpane", hostileID, nil)
	script := autopaneScript(`u" & 1 & "`, "Default", cmd)

	// The command is embedded twice — once per branch of the profile
	// fallback — and the payload must be inside a literal both times, and
	// nowhere else.
	literals := strings.Count(script, `command "`)
	if literals != 2 {
		t.Fatalf("expected two command literals, found %d", literals)
	}
	rest := script
	inside := 0
	for i := 0; i < literals; i++ {
		got := literalAt(t, rest, `command "`)
		if !strings.Contains(got, "do shell script") {
			t.Errorf("command literal %d lost the payload — it broke out instead:\n%s", i, got)
		}
		inside += strings.Count(got, "do shell script")
		rest = rest[strings.Index(rest, `command "`)+len(`command "`):]
	}
	if n := strings.Count(script, "do shell script"); n != inside {
		t.Errorf("payload appears %d times but only %d are inside a literal", n, inside)
	}
	if got := literalAt(t, script, `is equal to "`); got != `u" & 1 & "` {
		t.Errorf("the pane id literal reads %q, want the input back verbatim", got)
	}
}

// The whole guard chain, all the way through the act, on a backend that is not
// iTerm2. This is what proves auto-open is not macOS-only: the same fixture
// that drives the AppleScript path drives tmux, with a fake tmux recording the
// argv it was handed.
func TestAutopaneTmuxSplit(t *testing.T) {
	argv := setupAutopaneEnv(t)
	// Leave iTerm2 detectable: tmux must win anyway (a session inside tmux
	// inside iTerm2 lives in a tmux pane).
	t.Setenv("TMUX", "/tmp/tmux-501/default,4242,0")
	t.Setenv("TMUX_PANE", "%9")
	t.Setenv("AGENTPANE_TMUX", strings.TrimSuffix(argv, "osascript-argv.txt")+"fake-osascript")
	t.Setenv("AGENTPANE_COLUMNS", "72")

	in := strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession))
	if code := cmdAutopane(in, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("tmux not invoked on the all-pass path: %v", err)
	}
	got := string(raw)
	exe, _ := os.Executable()
	for _, want := range []string{
		"split-window",
		"-d",                             // focus stays in the session pane
		"-l",                             // ...at a width we chose
		"72",                             // ...which is AGENTPANE_COLUMNS
		"%9",                             // the session's own pane
		exe,                              // the TUI, by absolute path
		"--session", autopaneTestSession, // pinned to this session
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tmux argv missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "iTerm2") {
		t.Fatalf("an AppleScript reached tmux:\n%s", got)
	}
	if _, err := os.Stat(autopaneLockPath(autopaneTestSession)); err != nil {
		t.Fatalf("lockfile not left in place: %v", err)
	}
}

// A terminal agentpane cannot drive is a skip, not a failure, and nothing is
// run at all.
func TestAutopaneUnsupportedTerminal(t *testing.T) {
	argv := setupAutopaneEnv(t)
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("ITERM_SESSION_ID", "")

	var explain bytes.Buffer
	in := strings.NewReader(autopanePayloadJSON("SessionStart", "startup", autopaneTestSession))
	runAutopaneMode(in, nil, os.Getpid(), &explain, false)

	if autopaneInvoked(argv) {
		t.Fatal("something was run in a terminal with no split backend")
	}
	if !strings.Contains(explain.String(), "(c) no terminal to split") {
		t.Fatalf("guard (c) did not report the reason:\n%s", explain.String())
	}
	if _, err := os.Stat(autopaneLockPath(autopaneTestSession)); err == nil {
		t.Fatal("a skipped event took the lock")
	}
}
