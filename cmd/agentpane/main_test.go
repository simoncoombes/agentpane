package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/statefile"
)

func TestMungeCwd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Users/simoncoombes/Dev", "-Users-simoncoombes-Dev"},
		{"/Users/x/src/api", "-Users-x-src-api"},
		{"/tmp/a.b_c", "-tmp-a-b-c"},
		{"", ""},
		{"/a b/c", "-a-b-c"},
	}
	for _, c := range cases {
		if got := mungeCwd(c.in); got != c.want {
			t.Errorf("mungeCwd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"3f9c1a2b-aaaa", "3f9c"},
		{"ab", "ab"},
		{"", ""},
	}
	for _, c := range cases {
		if got := shortID(c.in); got != c.want {
			t.Errorf("shortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHookSnippet: the doctor snippet must be valid JSON covering every hook
// event with `agentpane hook` at timeout 5.
func TestHookSnippet(t *testing.T) {
	snippet := hookSnippet()
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string  `json:"type"`
				Command string  `json:"command"`
				Timeout float64 `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(snippet), &root); err != nil {
		t.Fatalf("snippet is not valid JSON: %v\n%s", err, snippet)
	}
	if len(root.Hooks) != len(hookEvents) {
		t.Fatalf("snippet covers %d events, want %d", len(root.Hooks), len(hookEvents))
	}
	for _, ev := range hookEvents {
		groups, ok := root.Hooks[ev]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %s missing or malformed in snippet", ev)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "agentpane hook" || h.Timeout != 5 {
			t.Fatalf("event %s: got %+v, want command=agentpane hook timeout=5", ev, h)
		}
	}
}

// TestInstalledHookEvents reads a fabricated settings.json (READ ONLY path
// under a temp HOME).
func TestInstalledHookEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// No file: nothing installed, never an error.
	if got := installedHookEvents(); got != nil {
		t.Fatalf("want nil for missing settings.json, got %v", got)
	}

	settings := `{"hooks":{
		"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"danger-guard.sh"}]},
		              {"hooks":[{"type":"command","command":"agentpane hook","timeout":5}]}],
		"Stop":[{"hooks":[{"type":"command","command":"agentpane hook"}]}]
	}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	got := installedHookEvents()
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %v", got)
	}
	found := map[string]bool{}
	for _, e := range got {
		found[e] = true
	}
	if !found["PreToolUse"] || !found["Stop"] {
		t.Fatalf("want PreToolUse+Stop, got %v", got)
	}

	// A foreign command that merely mentions an agentpane path is not an
	// installed hook (same anchored matcher as install.sh / InspectSettings).
	settings = `{"hooks":{
		"Stop":[{"hooks":[{"type":"command","command":"notify.sh --after /x/agentpane hook --sound pop"}]}],
		"PreToolUse":[{"hooks":[{"type":"command","command":"/x/agentpane hook"}]}]
	}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := installedHookEvents(); len(got) != 1 || got[0] != "PreToolUse" {
		t.Fatalf("foreign mention must not count as installed: got %v, want [PreToolUse]", got)
	}

	// Malformed JSON: nil, no panic (C9).
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := installedHookEvents(); got != nil {
		t.Fatalf("want nil for malformed settings.json, got %v", got)
	}
}

func TestMissingHookEvents(t *testing.T) {
	if got := missingHookEvents(hookEvents); len(got) != 0 {
		t.Fatalf("full set must have no missing events, got %v", got)
	}
	got := missingHookEvents([]string{"PreToolUse"})
	if len(got) != len(hookEvents)-1 {
		t.Fatalf("want %d missing, got %d", len(hookEvents)-1, len(got))
	}
}

// TestCmdHookGarbage: garbage stdin must still exit 0 (SPEC §1.4: never
// block or fail the hook).
func TestCmdHookGarbage(t *testing.T) {
	inputs := []string{"", "not json", `{"session_id":""}`, `{"session_id":"x`, strings.Repeat("A", 1<<20)}
	for i, in := range inputs {
		if code := cmdHook(strings.NewReader(in)); code != 0 {
			t.Errorf("input %d: exit %d, want 0", i, code)
		}
	}
	if code := cmdHook(nil); code != 0 {
		t.Errorf("nil stdin: exit %d, want 0", code)
	}
}

// TestCmdOnelineNoState: with no state file the output is the exact §3.20a
// copy and the exit code 0 — with or without a --session pick.
func TestCmdOnelineNoState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, fl := range []cliFlags{{}, {session: "sess-aaaa"}} {
		var out bytes.Buffer
		if code := cmdOneline(&out, fl); code != 0 {
			t.Fatalf("exit %d, want 0", code)
		}
		if got := strings.TrimSpace(out.String()); got != "no agentpane running" {
			t.Fatalf("session %q: got %q, want %q", fl.session, got, "no agentpane running")
		}
	}
}

// TestCmdOnelinePicksThePinnedPane: with several panes running, --oneline
// --session <id> reports THAT pane, and the unpinned form names the session
// it chose so the status bar is not silently attributing one session's agents
// to another.
func TestCmdOnelinePicksThePinnedPane(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	stateDir := filepath.Join(dir, "agentpane")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(session string, s statefile.State) {
		t.Helper()
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, statefile.FileNameFor(session)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sess-aaaa", statefile.State{Session: "aaaa", Live: 2, Settled: 1, UpdatedAt: now.Add(-5 * time.Second)})
	write("sess-bbbb", statefile.State{Session: "bbbb", Live: 7, Settled: 3, UpdatedAt: now.Add(-time.Second)})

	var pinned bytes.Buffer
	if code := cmdOneline(&pinned, cliFlags{session: "sess-aaaa"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(pinned.String()); got != "●2 ✔1" {
		t.Fatalf("--session aaaa printed %q, want its own row ●2 ✔1", got)
	}

	var newest bytes.Buffer
	if code := cmdOneline(&newest, cliFlags{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(newest.String()); got != "bbbb ●7 ✔3" {
		t.Fatalf("unpinned printed %q, want the newest pane named: bbbb ●7 ✔3", got)
	}
}

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != Version {
		t.Fatalf("got %q, want %q", got, Version)
	}
}

func TestRunUnknownVerb(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "agentpane --demo") {
		t.Fatal("usage text not printed on unknown verb")
	}
}

func TestParseFlags(t *testing.T) {
	var errb bytes.Buffer
	fl, err := parseFlags([]string{
		"--demo", "--demo-speed", "2.5", "--demo-seek", "90",
		"--width", "narrow", "--accent", "6", "--no-color",
		"--no-bell", "--no-badge", "--no-notify", "--no-links",
		"--log", "/tmp/x.log", "--source", "transcript",
		"--session", "3f9c1a2b-aaaa-bbbb",
	}, &errb)
	if err != nil {
		t.Fatalf("parse: %v (%s)", err, errb.String())
	}
	if !fl.demo || fl.demoSpeed != 2.5 || fl.demoSeek != 90 ||
		fl.width != "narrow" || fl.accent != "6" || !fl.noColor ||
		!fl.noBell || !fl.noBadge || !fl.noNotify || !fl.noLinks ||
		fl.logPath != "/tmp/x.log" || fl.source != "transcript" ||
		fl.session != "3f9c1a2b-aaaa-bbbb" {
		t.Fatalf("flags mis-parsed: %+v", fl)
	}

	// Both spellings, and no pin by default.
	for _, args := range [][]string{{"--session", "abc"}, {"-session=abc"}} {
		got, err := parseFlags(args, &errb)
		if err != nil || got.session != "abc" {
			t.Fatalf("parseFlags(%v) = %q, %v", args, got.session, err)
		}
	}
	if got, err := parseFlags(nil, &errb); err != nil || got.session != "" {
		t.Fatalf("bare agentpane must not pin: %q, %v", got.session, err)
	}

	// --render-once always renders the demo scenario, so pairing it with a
	// real session id can only produce a confident answer about a session it
	// never read. Rejected, with the honest alternative in the message.
	errb.Reset()
	if _, err := parseFlags([]string{"--render-once", "--session", "3f9c1a2b"}, &errb); err == nil {
		t.Fatal("--render-once --session must be rejected, not silently render the demo")
	}
	for _, want := range []string{"demo scenario", "3f9c1a2b", "--oneline --session"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("rejection message missing %q: %s", want, errb.String())
		}
	}
	// The CI/screenshot path itself is untouched.
	errb.Reset()
	if got, err := parseFlags([]string{"--render-once", "--cols", "64", "--rows", "40"}, &errb); err != nil ||
		!got.renderOnce || got.cols != 64 || got.rows != 40 {
		t.Fatalf("bare --render-once broke: %+v, %v (%s)", got, err, errb.String())
	}

	if _, err := parseFlags([]string{"--width", "narrow", "stray"}, &errb); err == nil {
		t.Fatal("stray positional argument must fail")
	}
}

// TestEmptySessionIsRejected: `--session ""` is a pin whose value went
// missing, not "no pin". Accepting it silently dropped the pane back onto the
// cwd guess — five live sessions in one directory, a coin flip — and re-keyed
// the run store and state file to whatever was guessed. Every shell wrapper
// that expands an unset variable produces exactly this.
func TestEmptySessionIsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"--session", ""},
		{"--session="},
		{"-session", ""},
		{"--oneline", "--session", ""},
	} {
		var errb bytes.Buffer
		fl, err := parseFlags(args, &errb)
		if err == nil {
			t.Errorf("parseFlags(%q) accepted an empty pin (session=%q)", args, fl.session)
			continue
		}
		if !strings.Contains(errb.String(), "CLAUDE_CODE_SESSION_ID") {
			t.Errorf("parseFlags(%q) message does not name the real variable:\n%s", args, errb.String())
		}
	}

	// An ABSENT --session is still the documented "guess for me" path.
	var errb bytes.Buffer
	if fl, err := parseFlags([]string{"--width", "narrow"}, &errb); err != nil || fl.session != "" {
		t.Fatalf("no --session must stay unpinned: %q, %v", fl.session, err)
	}

	// End to end: exit 2, not a silent unpin.
	var out, stderr bytes.Buffer
	if code := run([]string{"--session", ""}, &out, &stderr); code != 2 {
		t.Fatalf("run(--session \"\") exit %d, want 2", code)
	}
}

// TestOnelineNeverPrintsUsage: a status bar has one row and no error channel.
// §3.20a promises exit 0 always, but parseFlags fails before cmdOneline ever
// runs — an unquoted unset variable turned `--oneline --session $VAR` into
// "flag needs an argument" plus the whole usage block, dumped into the bar.
func TestOnelineNeverPrintsUsage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cases := [][]string{
		{"--oneline", "--session"},     // unquoted + unset: the id ate the flag
		{"--oneline", "--session", ""}, // quoted + unset: an empty pin
		{"--oneline", "--bogus"},
	}
	for _, args := range cases {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 {
			t.Errorf("run(%q) exit %d, want 0 — a status bar must never see a failure", args, code)
		}
		got := strings.TrimRight(out.String(), "\n")
		if strings.Contains(got, "\n") {
			t.Errorf("run(%q) printed %d lines into a one-row status bar:\n%s",
				args, strings.Count(got, "\n")+1, got)
		}
		if strings.Contains(got, "agentpane --demo") || strings.Contains(stderr.String(), "agentpane --demo") {
			t.Errorf("run(%q) dumped the usage block:\nstdout=%q\nstderr=%q", args, got, stderr.String())
		}
		if len([]rune(got)) > 60 {
			t.Errorf("run(%q) printed a %d-char row: %q", args, len([]rune(got)), got)
		}
		if got == "" {
			t.Errorf("run(%q) printed nothing at all", args)
		}
	}

	// The working recipe still prints the real row and exits 0.
	var out, stderr bytes.Buffer
	if code := run([]string{"--oneline", "--session", "3f9c1a2b"}, &out, &stderr); code != 0 {
		t.Fatalf("valid --oneline exit %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "no agentpane running" {
		t.Fatalf("valid --oneline printed %q", got)
	}
}

// TestReadmeStatusBarRecipeIsRunnable: the README's pinned recipe used
// $CLAUDE_SESSION_ID, which Claude Code does not set. The variable that
// exists is $CLAUDE_CODE_SESSION_ID, and it has to be quoted — an unquoted
// unset expansion feeds argv one flag short.
func TestReadmeStatusBarRecipeIsRunnable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	if strings.Contains(readme, "$CLAUDE_SESSION_ID") {
		t.Error("README still uses $CLAUDE_SESSION_ID — Claude Code sets CLAUDE_CODE_SESSION_ID")
	}
	if !strings.Contains(readme, `--session "$CLAUDE_CODE_SESSION_ID"`) {
		t.Error("README's pinned status-bar recipe must pass a QUOTED $CLAUDE_CODE_SESSION_ID")
	}
}

// TestUsageDocumentsSession: PART 6's usage text must carry --session, since
// it is the only documentation a user sees for `agentpane help`.
func TestUsageDocumentsSession(t *testing.T) {
	if !strings.Contains(usageText, "--session") {
		t.Fatalf("usage text does not mention --session:\n%s", usageText)
	}
}

func TestApplyOverrides(t *testing.T) {
	cases := []struct {
		name  string
		fl    cliFlags
		check func(t *testing.T, cfg cfgLike, warns int)
	}{
		{"width valid", cliFlags{width: "narrow"}, func(t *testing.T, c cfgLike, w int) {
			if c.width != "narrow" || w != 0 {
				t.Fatalf("got %q/%d", c.width, w)
			}
		}},
		{"width invalid warns", cliFlags{width: "huge"}, func(t *testing.T, c cfgLike, w int) {
			if c.width != "wide" || w != 1 {
				t.Fatalf("got %q/%d", c.width, w)
			}
		}},
		{"accent invalid warns", cliFlags{accent: "9"}, func(t *testing.T, c cfgLike, w int) {
			if c.accent != "auto" || w != 1 {
				t.Fatalf("got %q/%d", c.accent, w)
			}
		}},
		{"kill switches", cliFlags{noColor: true, noBell: true, noBadge: true, noNotify: true, noLinks: true},
			func(t *testing.T, c cfgLike, w int) {
				if c.color || c.bell || c.badge || c.notify || c.links || w != 0 {
					t.Fatalf("got %+v/%d", c, w)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			cfg := config.Default()
			var warns []config.Warning
			applyOverrides(&cfg, tc.fl, &warns)
			tc.check(t, cfgLike{cfg.Width, cfg.Accent, cfg.Color, cfg.Bell, cfg.Badge, cfg.Notify, cfg.Links}, len(warns))
		})
	}
}

func TestFmtRunDur(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{58 * time.Second, "58s"},
		{4*time.Minute + 12*time.Second, "4m12s"},
		{time.Hour + 4*time.Minute, "1h04m"},
	}
	for _, c := range cases {
		if got := fmtRunDur(c.in); got != c.want {
			t.Errorf("fmtRunDur(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtRunTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "- tok"},
		{999, "999 tok"},
		{611000, "611k tok"},
		{1200000, "1.2M tok"},
	}
	for _, c := range cases {
		if got := fmtRunTokens(c.in); got != c.want {
			t.Errorf("fmtRunTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if p := loadPrefs(); p.Width != "" || p.RightCol != "" {
		t.Fatalf("fresh prefs not empty: %+v", p)
	}
	savePrefs(uiPrefs{Width: "narrow", RightCol: "rate"})
	p := loadPrefs()
	if p.Width != "narrow" || p.RightCol != "rate" {
		t.Fatalf("round trip lost data: %+v", p)
	}
}

// TestRenderOnceFrame: the CI path must print a deterministic frame with the
// §3.1 chrome and an unbroken trunk, without a tty.
func TestRenderOnceFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	code := cmdRenderOnce(&out, cliFlags{cols: 64, rows: 40, demoSeek: 300})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	frame := out.String()
	for _, want := range []string{"AGENTS", "NEEDS YOU", "╰─", "◐ in Bash", "limited telemetry"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	// Deterministic: a second render is byte-identical.
	var out2 bytes.Buffer
	if code := cmdRenderOnce(&out2, cliFlags{cols: 64, rows: 40, demoSeek: 300}); code != 0 {
		t.Fatalf("second render exit %d", code)
	}
	if out.String() != out2.String() {
		t.Fatal("render-once is not deterministic")
	}
}

// TestDoctorRuns: doctor must complete without panicking in a tty-less test
// environment and always emit the accent reasoning, the glyph sample, and
// the honesty lines.
func TestDoctorRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if code := cmdDoctor(&out, io.Discard, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	s := out.String()
	for _, want := range []string{
		"accent:", "live/primary", "agentpane hook",
		"● ⚑ ○ ◇ ◌ ▪▫· ▁▂▃▄▅▆▇ ◐ ↺ ⤷ ╰─╮ ⠹",
		"cannot verify from a script",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, s)
		}
	}
}

// TestRunsEmpty: an empty store lists honestly.
func TestRunsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := cmdRuns(&out, &errb, nil); code != 0 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "no persisted runs" {
		t.Fatalf("got %q", got)
	}
	if code := cmdRuns(&out, &errb, []string{"bogus"}); code != 2 {
		t.Fatalf("bad subcommand: exit %d, want 2", code)
	}
}

// TestTakeSessionFlag: `agentpane runs` keeps listing everything, and
// --session narrows it to one session's history.
func TestTakeSessionFlag(t *testing.T) {
	cases := []struct {
		args        []string
		wantSession string
		wantRest    []string
	}{
		{nil, "", nil},
		{[]string{"--session", "abc"}, "abc", nil},
		{[]string{"--session=abc"}, "abc", nil},
		{[]string{"-session", "abc"}, "abc", nil},
		{[]string{"--session", "abc", "show", "r1"}, "abc", []string{"show", "r1"}},
		{[]string{"show", "r1"}, "", []string{"show", "r1"}},
	}
	for _, c := range cases {
		session, rest, err := takeSessionFlag(c.args)
		if err != nil {
			t.Errorf("takeSessionFlag(%v): %v", c.args, err)
			continue
		}
		if session != c.wantSession {
			t.Errorf("takeSessionFlag(%v) session = %q, want %q", c.args, session, c.wantSession)
		}
		if strings.Join(rest, " ") != strings.Join(c.wantRest, " ") {
			t.Errorf("takeSessionFlag(%v) rest = %v, want %v", c.args, rest, c.wantRest)
		}
	}
	for _, args := range [][]string{{"--session"}, {"--session="}, {"--session", ""}} {
		if _, _, err := takeSessionFlag(args); err == nil {
			t.Errorf("takeSessionFlag(%v) accepted an empty session id", args)
		}
	}
}

// TestRunsSessionScoped: the verb lists the whole store by default and one
// session under --session — the same slice that session's pinned pane sees.
func TestRunsSessionScoped(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runsDir := filepath.Join(state, "agentpane", "runs")
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for i, session := range []string{"sess-aaaa", "sess-bbbb"} {
		st := &runstore.Store{Dir: runsDir, Session: session}
		if err := st.Append(runstore.Run{
			ID:    "run-" + session,
			Start: start.Add(time.Duration(i) * time.Minute),
			End:   start.Add(time.Duration(i)*time.Minute + time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var all, errb bytes.Buffer
	if code := cmdRuns(&all, &errb, nil); code != 0 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
	for _, want := range []string{"run-sess-aaaa", "run-sess-bbbb", "session sess"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("`agentpane runs` output missing %q:\n%s", want, all.String())
		}
	}

	var one bytes.Buffer
	if code := cmdRuns(&one, &errb, []string{"--session", "sess-aaaa"}); code != 0 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
	if !strings.Contains(one.String(), "run-sess-aaaa") || strings.Contains(one.String(), "run-sess-bbbb") {
		t.Fatalf("`runs --session sess-aaaa` leaked another session:\n%s", one.String())
	}
}

// cfgLike keeps the override table terse.
type cfgLike struct {
	width, accent                     string
	color, bell, badge, notify, links bool
}

// TestDoctorSessionAware: doctor used to resolve its attach target and socket
// from the cwd guess with no --session awareness at all, so on a machine where
// every pane is pinned it reported a session the reader is probably not
// looking at, as if it were "the" answer.
func TestDoctorSessionAware(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sessDir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	body := `{"sessionId":"s-guessed","cwd":` + strconv.Quote(cwd) +
		`,"status":"busy","statusUpdatedAt":"2026-08-01T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(sessDir, strconv.Itoa(os.Getpid())+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// No --session: the guess is reported AS a guess, and points at the pin.
	var guess bytes.Buffer
	if code := cmdDoctor(&guess, io.Discard, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(guess.String(), "attach target (no --session)") ||
		!strings.Contains(guess.String(), "pass --session <id>") {
		t.Errorf("unpinned doctor still reports the cwd guess as the answer:\n%s", guess.String())
	}

	// --session <live id>: exact match, no cwd rule.
	var pinned bytes.Buffer
	if code := cmdDoctor(&pinned, io.Discard, []string{"--session", "s-guessed"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(pinned.String(), "--session, exact match") {
		t.Errorf("doctor --session did not resolve exactly:\n%s", pinned.String())
	}

	// --session <absent id>: the honest answer is that a pane pinned to it
	// waits — NOT the cwd guess, which is what the pin exists to refuse.
	var missing bytes.Buffer
	if code := cmdDoctor(&missing, io.Discard, []string{"--session", "s-not-running"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(missing.String(), "never falls back") {
		t.Errorf("doctor --session <absent> did not say the pane waits:\n%s", missing.String())
	}
	if strings.Contains(missing.String(), "s-guessed") || strings.Contains(missing.String(), "s-gu") {
		t.Errorf("doctor --session <absent> leaked the cwd guess:\n%s", missing.String())
	}
	if !strings.Contains(missing.String(), "s-no") {
		t.Errorf("doctor --session <absent> did not check that session's socket:\n%s", missing.String())
	}

	// A bad --session is a usage error, not a silent whole-machine report.
	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "--session"}, &out, &errb); code != 2 {
		t.Fatalf("doctor --session with no id: exit %d, want 2", code)
	}
	if code := run([]string{"doctor", "stray"}, &out, &errb); code != 2 {
		t.Fatalf("doctor with a stray argument: exit %d, want 2", code)
	}
}
