package main

// pane_test.go — the terminal backends, tested as pure functions.
//
// None of tmux, WezTerm or kitty is installed on the machine that runs these,
// and that is the point: splitCommands/focusCommands/tabCommands turn an
// environment into an exact argv, so the argv is what gets asserted. The one
// thing these cannot prove is that the argv is the argv those programs want —
// that is a claim about their CLIs, checked against their docs and marked in
// docs/TERMINALS.md, not something a unit test can settle.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnv is an envLookup over a map: an environment with nothing inherited.
func fakeEnv(kv map[string]string) envLookup {
	return func(k string) string { return kv[k] }
}

func TestDetectBackend(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"bare shell", map[string]string{}, backendNone},
		{"iTerm2", map[string]string{
			"TERM_PROGRAM": "iTerm.app", "ITERM_SESSION_ID": "w0t0p0:UUID"}, backendITerm2},
		{"iTerm2 without a session id", map[string]string{
			"TERM_PROGRAM": "iTerm.app"}, backendNone},
		{"tmux", map[string]string{
			"TMUX": "/tmp/tmux-501/default,123,0", "TMUX_PANE": "%3"}, backendTmux},
		{"wezterm", map[string]string{"WEZTERM_PANE": "0"}, backendWezTerm},
		{"kitty", map[string]string{"KITTY_WINDOW_ID": "1"}, backendKitty},
		{"Terminal.app", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, backendNone},
		{"Ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, backendNone},

		// The multiplexer wins over the terminal hosting it: splitting the
		// iTerm2 pane would divide the whole tmux viewport and put the new
		// pane outside tmux entirely.
		{"tmux inside iTerm2", map[string]string{
			"TMUX": "/tmp/tmux-501/default,123,0", "TMUX_PANE": "%3",
			"TERM_PROGRAM": "iTerm.app", "ITERM_SESSION_ID": "w0t0p0:UUID"}, backendTmux},

		// The override beats detection in both directions.
		{"forced off", map[string]string{
			"AGENTPANE_TERMINAL": "none",
			"TERM_PROGRAM":       "iTerm.app", "ITERM_SESSION_ID": "w0t0p0:UUID"}, backendNone},
		{"forced tmux", map[string]string{
			"AGENTPANE_TERMINAL": "tmux", "TMUX_PANE": "%1"}, backendTmux},
		{"forced, case-insensitive", map[string]string{
			"AGENTPANE_TERMINAL": "WezTerm"}, backendWezTerm},
		// A typo disables rather than falling through to what was being
		// overridden — a silent fall-through would be indistinguishable from
		// the override not working.
		{"typo in the override", map[string]string{
			"AGENTPANE_TERMINAL": "iterm", "TERM_PROGRAM": "iTerm.app",
			"ITERM_SESSION_ID": "w0t0p0:UUID"}, backendNone},
	}
	for _, c := range cases {
		if got := detectBackend(fakeEnv(c.env)); got != c.want {
			t.Errorf("%s: detectBackend = %q, want %q", c.name, got, c.want)
		}
	}
}

// argvOf flattens one attempt for substring assertions.
func argvOf(t *testing.T, chain []paneCmd, i int) string {
	t.Helper()
	if len(chain) <= i {
		t.Fatalf("chain has %d entries, wanted entry %d", len(chain), i)
	}
	return strings.Join(chain[i].argv, " ")
}

func TestSplitCommands(t *testing.T) {
	const cmdline = "/bin/sh -c '/usr/local/bin/agentpane --session abc'"

	t.Run("tmux sizes the pane and keeps focus", func(t *testing.T) {
		chain := splitCommands(backendTmux, fakeEnv(map[string]string{"TMUX_PANE": "%7"}), cmdline, 64)
		got := argvOf(t, chain, 0)
		for _, want := range []string{
			"tmux split-window",
			"-h",        // left/right, tmux's naming for iTerm2's "vertically"
			"-d",        // focus stays on the session pane
			"-l 64",     // the pane is born the right width, no resize dance
			"-t %7",     // the session's own pane, not "the current one"
			"--session", // the pin travels in cmdline
		} {
			if !strings.Contains(got, want) {
				t.Errorf("tmux split missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("tmux falls back for versions without -l", func(t *testing.T) {
		chain := splitCommands(backendTmux, fakeEnv(map[string]string{"TMUX_PANE": "%7"}), cmdline, 64)
		if len(chain) != 2 {
			t.Fatalf("tmux chain has %d entries, want 2 (sized, then unsized)", len(chain))
		}
		if got := argvOf(t, chain, 1); strings.Contains(got, "-l") {
			t.Errorf("the fallback still passes -l, so pre-3.1 tmux still rejects it:\n%s", got)
		}
	})

	t.Run("tmux with no pane id cannot split", func(t *testing.T) {
		if chain := splitCommands(backendTmux, fakeEnv(nil), cmdline, 64); chain != nil {
			t.Errorf("split without TMUX_PANE returned %v", chain)
		}
	})

	t.Run("wezterm hands focus back", func(t *testing.T) {
		chain := splitCommands(backendWezTerm, fakeEnv(map[string]string{"WEZTERM_PANE": "3"}), cmdline, 64)
		got := argvOf(t, chain, 0)
		for _, want := range []string{"wezterm cli split-pane", "--pane-id 3", "--right", "--cells 64"} {
			if !strings.Contains(got, want) {
				t.Errorf("wezterm split missing %q:\n%s", want, got)
			}
		}
		if len(chain[0].after) != 1 {
			t.Fatalf("wezterm split has %d follow-ups, want 1 (focus back)", len(chain[0].after))
		}
		if back := strings.Join(chain[0].after[0], " "); !strings.Contains(back, "activate-pane --pane-id 3") {
			t.Errorf("wezterm does not return focus to pane 3: %s", back)
		}
	})

	t.Run("kitty tries kitten then kitty", func(t *testing.T) {
		chain := splitCommands(backendKitty, fakeEnv(map[string]string{"KITTY_WINDOW_ID": "1"}), cmdline, 64)
		if len(chain) != 2 {
			t.Fatalf("kitty chain has %d entries, want 2 (kitten, then kitty)", len(chain))
		}
		if chain[0].argv[0] != "kitten" || chain[1].argv[0] != "kitty" {
			t.Errorf("kitty chain order = %q, %q", chain[0].argv[0], chain[1].argv[0])
		}
		if got := argvOf(t, chain, 0); !strings.Contains(got, "--dont-take-focus") {
			t.Errorf("kitty split takes focus:\n%s", got)
		}
	})

	t.Run("iterm2 strips the position prefix", func(t *testing.T) {
		chain := splitCommands(backendITerm2, fakeEnv(map[string]string{
			"ITERM_SESSION_ID": "w0t2p0:ABCD-1234"}), cmdline, 64)
		got := argvOf(t, chain, 0)
		if !strings.Contains(got, `"ABCD-1234"`) {
			t.Errorf("AppleScript does not target the stripped uuid:\n%s", got)
		}
		if strings.Contains(got, "w0t2p0") {
			t.Errorf("AppleScript kept the position prefix:\n%s", got)
		}
	})

	t.Run("none splits nothing", func(t *testing.T) {
		if chain := splitCommands(backendNone, fakeEnv(nil), cmdline, 64); chain != nil {
			t.Errorf("backendNone returned %v", chain)
		}
	})
}

// The focus jump aims LEFT on every backend: agentpane is split to the right
// of the session by construction, and the TUI never saw the id of the pane it
// was split from.
func TestFocusCommandsAimLeft(t *testing.T) {
	cases := map[string][]string{
		backendTmux:    {"{left-of}", "select-pane -l"},
		backendWezTerm: {"Left"},
		backendKitty:   {"neighbor:left"},
		backendITerm2:  {"first session of current tab"},
	}
	for backend, wants := range cases {
		chain := focusCommands(backend, fakeEnv(nil))
		if len(chain) == 0 {
			t.Errorf("%s: no focus command", backend)
			continue
		}
		var all []string
		for _, c := range chain {
			all = append(all, strings.Join(c.argv, " "))
		}
		joined := strings.Join(all, "\n")
		for _, want := range wants {
			if !strings.Contains(joined, want) {
				t.Errorf("%s focus missing %q:\n%s", backend, want, joined)
			}
		}
	}
	if chain := focusCommands(backendNone, fakeEnv(nil)); chain != nil {
		t.Errorf("backendNone has a focus command: %v", chain)
	}
}

func TestTabCommands(t *testing.T) {
	const cmdline = "/bin/sh -c 'vim /src/main.go'"
	for _, backend := range paneBackends {
		chain := tabCommands(backend, fakeEnv(nil), cmdline)
		if len(chain) == 0 {
			t.Errorf("%s: no way to open a tab", backend)
			continue
		}
		if got := argvOf(t, chain, 0); !strings.Contains(got, "vim /src/main.go") {
			t.Errorf("%s tab command loses the editor command:\n%s", backend, got)
		}
	}
	if chain := tabCommands(backendNone, fakeEnv(nil), cmdline); chain != nil {
		t.Errorf("backendNone has a tab command: %v", chain)
	}
}

// Every backend's program name is overridable, which is both the test seam and
// the way to point at a wrapper or a non-PATH install.
func TestPaneProgramOverride(t *testing.T) {
	env := fakeEnv(map[string]string{
		"TMUX_PANE":       "%1",
		"AGENTPANE_TMUX":  "/opt/fake/tmux",
		"KITTY_WINDOW_ID": "1",
		"AGENTPANE_KITTY": "/opt/fake/kitty",
	})
	if got := splitCommands(backendTmux, env, "cmd", 64)[0].argv[0]; got != "/opt/fake/tmux" {
		t.Errorf("AGENTPANE_TMUX ignored: %q", got)
	}
	chain := splitCommands(backendKitty, env, "cmd", 64)
	if got := chain[0].argv[0]; got != "kitten" {
		t.Errorf("kitten should be untouched by AGENTPANE_KITTY: %q", got)
	}
	if got := chain[1].argv[0]; got != "/opt/fake/kitty" {
		t.Errorf("AGENTPANE_KITTY ignored: %q", got)
	}
}

// An exhausted chain reports failure, and a later alternative is reached when
// an earlier one is missing or fails — which is what makes the kitten/kitty
// pair and the tmux focus fallback work.
func TestRunPaneChainFallsThrough(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ran")
	good := filepath.Join(tmp, "good")
	if err := os.WriteFile(good, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if runPaneChain(nil, time.Second) {
		t.Error("an empty chain reported success")
	}
	if runPaneChain([]paneCmd{{argv: []string{filepath.Join(tmp, "nope")}}}, time.Second) {
		t.Error("a missing binary reported success")
	}
	chain := []paneCmd{
		{argv: []string{filepath.Join(tmp, "nope")}},        // not installed
		{argv: []string{"/usr/bin/false"}},                  // installed, fails
		{argv: []string{good, marker}},                      // works
		{argv: []string{good, filepath.Join(tmp, "extra")}}, // must not run
	}
	if !runPaneChain(chain, 5*time.Second) {
		t.Fatal("the chain did not reach the working alternative")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the working alternative did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "extra")); err == nil {
		t.Error("the chain kept going after an alternative succeeded")
	}
}

// A follow-up runs after its command succeeds, and its own failure does not
// fail the attempt — a WezTerm split whose focus hand-back fails still left
// you with the pane you asked for.
func TestRunPaneChainFollowUps(t *testing.T) {
	tmp := t.TempDir()
	good := filepath.Join(tmp, "good")
	if err := os.WriteFile(good, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(tmp, "after")
	chain := []paneCmd{{
		argv:  []string{good, filepath.Join(tmp, "main")},
		after: [][]string{{good, after}, {"/usr/bin/false"}},
	}}
	if !runPaneChain(chain, 5*time.Second) {
		t.Fatal("a failing follow-up failed the whole attempt")
	}
	if _, err := os.Stat(after); err != nil {
		t.Errorf("the follow-up did not run: %v", err)
	}
}

// backendReason has to name what was actually looked at, or a skipped
// auto-open is undiagnosable.
func TestBackendReason(t *testing.T) {
	got := backendReason(fakeEnv(map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}))
	for _, want := range []string{"Apple_Terminal", "xterm-256color", "TMUX"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason missing %q: %s", want, got)
		}
	}
	got = backendReason(fakeEnv(map[string]string{"AGENTPANE_TERMINAL": "iterm"}))
	if !strings.Contains(got, "iterm") || !strings.Contains(got, "wezterm") {
		t.Errorf("an override typo must quote it and list the valid names: %s", got)
	}
}
