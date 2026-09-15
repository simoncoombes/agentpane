package main

// inspect_test.go — fixture parity with tests/install_test.sh. Every fixture
// below mirrors an install_test.sh case (noted per test); fixtures are built
// inline and install.sh is never executed. TestInstallShParity is the drift
// guard: it reads the embedded installer and asserts the shared pattern
// strings.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/installer"
)

// --- fixture builders -------------------------------------------------------

func settingsJSON(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fixture marshal: %v", err)
	}
	return b
}

// cmdGroup is install_test.sh's group shape: one {"matcher":"","hooks":[...]}.
func cmdGroup(cmd string) map[string]any {
	return map[string]any{"matcher": "", "hooks": []any{
		map[string]any{"type": "command", "command": cmd, "timeout": 5},
	}}
}

func allEventsHooks(cmd string) map[string]any {
	h := map[string]any{}
	for _, ev := range hookEvents {
		h[ev] = []any{cmdGroup(cmd)}
	}
	return h
}

// --- assertion helpers -------------------------------------------------------

func findLine(fs []Finding, level Level, substr string) *Finding {
	for i := range fs {
		if fs[i].Level == level && strings.Contains(fs[i].Line, substr) {
			return &fs[i]
		}
	}
	return nil
}

func countLevel(fs []Finding, level Level) int {
	n := 0
	for _, f := range fs {
		if f.Level == level {
			n++
		}
	}
	return n
}

func dump(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "[%d] %s\n", f.Level, f.Line)
		for _, d := range f.Detail {
			fmt.Fprintf(&b, "      %s\n", d)
		}
	}
	return b.String()
}

func detailContains(f *Finding, substr string) bool {
	if f == nil {
		return false
	}
	for _, d := range f.Detail {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

const fxPath = "/fx/.claude/settings.json"

// --- table: cases mirroring tests/install_test.sh ---------------------------

// TestInspectFresh mirrors install_test.sh case 01: plain settings with no
// hooks — nothing to warn about, entries reported as none.
func TestInspectFresh(t *testing.T) {
	raw := settingsJSON(t, map[string]any{
		"model":       "opus",
		"permissions": map[string]any{"defaultMode": "auto"},
		"env":         map[string]any{"FOO": "bar"},
	})
	fs := InspectSettings(raw, fxPath, "", "")
	if countLevel(fs, LevelWarn) != 0 || countLevel(fs, LevelFail) != 0 {
		t.Fatalf("fresh settings must produce no warn/fail:\n%s", dump(fs))
	}
	if findLine(fs, LevelInfo, "agentpane entries: none") == nil {
		t.Fatalf("missing entries-none info:\n%s", dump(fs))
	}
}

// TestInspectComplete: all 12 events covered (bare command, as the doctor
// snippet prescribes) — a single ✔ coverage line, no warns.
func TestInspectComplete(t *testing.T) {
	raw := settingsJSON(t, map[string]any{"hooks": allEventsHooks("agentpane hook")})
	fs := InspectSettings(raw, fxPath, "", "")
	if findLine(fs, LevelInfo, "present on all 12 events") == nil {
		t.Fatalf("missing complete-coverage info:\n%s", dump(fs))
	}
	if countLevel(fs, LevelWarn) != 0 || countLevel(fs, LevelFail) != 0 {
		t.Fatalf("complete coverage must produce no warn/fail:\n%s", dump(fs))
	}
}

// TestInspectPartial mirrors case 03: 4 events covered, the missing 8 listed
// in event order with the install.sh suggestion.
func TestInspectPartial(t *testing.T) {
	hooks := map[string]any{}
	for _, ev := range []string{"PreToolUse", "Stop", "SessionStart", "Notification"} {
		hooks[ev] = []any{cmdGroup("agentpane hook")}
	}
	raw := settingsJSON(t, map[string]any{"hooks": hooks})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "partial coverage")
	if f == nil {
		t.Fatalf("missing partial-coverage warn:\n%s", dump(fs))
	}
	want := "missing PostToolUse, UserPromptSubmit, SubagentStart, SubagentStop, SessionEnd, PermissionRequest, PreCompact, PostCompact"
	if !strings.Contains(f.Line, want) {
		t.Fatalf("missing-events list wrong:\n%s", f.Line)
	}
	if !detailContains(f, "`agentpane install`") {
		t.Fatalf("partial coverage must suggest `agentpane install`:\n%s", dump(fs))
	}
}

// TestInspectStalePath mirrors case 04: all 12 events point at a binary path
// that no longer exists.
func TestInspectStalePath(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "old", "prefix") // never created
	raw := settingsJSON(t, map[string]any{"hooks": allEventsHooks(gone + "/agentpane hook")})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "stale agentpane path")
	if f == nil {
		t.Fatalf("missing stale-path warn:\n%s", dump(fs))
	}
	if !strings.Contains(f.Line, gone+"/agentpane") || !strings.Contains(f.Line, "PreToolUse") {
		t.Fatalf("stale warn must name the path and events:\n%s", f.Line)
	}
	if !detailContains(f, "`agentpane install`") {
		t.Fatalf("stale warn must carry the installer fix hint:\n%s", dump(fs))
	}
	if findLine(fs, LevelInfo, "present on all 12 events") == nil {
		t.Fatalf("stale entries still count as coverage:\n%s", dump(fs))
	}
}

// TestInspectDiffersFromRunningBinary: the command's binary exists but is not
// the running one.
func TestInspectDiffersFromRunningBinary(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	other := filepath.Join(dirA, "agentpane")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(dirB, "agentpane")
	// filepath.Join, not dirA+"/agentpane": the finding is compared against a
	// path built with Join, and on Windows the two spellings differ.
	raw := settingsJSON(t, map[string]any{"hooks": allEventsHooks(other + " hook")})
	fs := InspectSettings(raw, fxPath, "", running)
	f := findLine(fs, LevelWarn, "not the running binary")
	if f == nil {
		t.Fatalf("missing differs-from-running warn:\n%s", dump(fs))
	}
	if !strings.Contains(f.Line, other) || !strings.Contains(f.Line, running) {
		t.Fatalf("differs warn must name both paths:\n%s", f.Line)
	}
	// Same path: no warn.
	fs = InspectSettings(raw, fxPath, "", other)
	if findLine(fs, LevelWarn, "not the running binary") != nil {
		t.Fatalf("same binary must not warn:\n%s", dump(fs))
	}
	// Unknown running binary: check skipped.
	fs = InspectSettings(raw, fxPath, "", "")
	if findLine(fs, LevelWarn, "not the running binary") != nil {
		t.Fatalf("empty binaryPath must skip the differs check:\n%s", dump(fs))
	}
}

// TestInspectDuplicate mirrors case 20: a stale old-path group beside a
// current one on Stop — flagged as duplicates, and the dead path as stale.
func TestInspectDuplicate(t *testing.T) {
	hooks := allEventsHooks("agentpane hook")
	// An absolute path that does not exist. Built from TempDir rather than
	// written out, because "/old/dead/agentpane" is not absolute on Windows
	// and the staleness check only looks at paths it can resolve.
	dead := filepath.Join(t.TempDir(), "old", "dead", "agentpane")
	hooks["Stop"] = []any{cmdGroup(dead + " hook"), cmdGroup("agentpane hook")}
	raw := settingsJSON(t, map[string]any{"hooks": hooks})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "duplicate agentpane entries")
	if f == nil || !strings.Contains(f.Line, "Stop (2)") {
		t.Fatalf("missing duplicate warn for Stop:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "stale agentpane path") == nil {
		t.Fatalf("dead path %s must also flag stale:\n%s", dead, dump(fs))
	}
}

// TestInspectCompetingVisualizer mirrors case 06: it2-pane-hook.sh on
// SubagentStart/SubagentStop plus an AGENT_PANE_FOO env var.
func TestInspectCompetingVisualizer(t *testing.T) {
	raw := settingsJSON(t, map[string]any{
		"env": map[string]any{"AGENT_PANE_FOO": "1", "OTHER": "x"},
		"hooks": map[string]any{
			"SubagentStart": []any{cmdGroup("/Users/x/.claude/it2-pane-hook.sh start")},
			"SubagentStop":  []any{cmdGroup("/Users/x/.claude/it2-pane-hook.sh stop")},
		},
	})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "competing subagent visualizer on SubagentStart: /Users/x/.claude/it2-pane-hook.sh start")
	if f == nil {
		t.Fatalf("missing exact SubagentStart visualizer warn:\n%s", dump(fs))
	}
	if !detailContains(f, "doubles panes") {
		t.Fatalf("visualizer warn must carry the doubles-panes copy:\n%s", dump(fs))
	}
	if !detailContains(f, "AGENT_PANE_FOO") {
		t.Fatalf("visualizer warn must name the AGENT_PANE_ env vars:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "competing subagent visualizer on SubagentStop: /Users/x/.claude/it2-pane-hook.sh stop") == nil {
		t.Fatalf("missing SubagentStop visualizer warn:\n%s", dump(fs))
	}
	// agentpane's own entries never count as competing visualizers, despite
	// "pane" in the name.
	raw = settingsJSON(t, map[string]any{"hooks": map[string]any{
		"SubagentStart": []any{cmdGroup("agentpane hook")},
	}})
	fs = InspectSettings(raw, fxPath, "", "")
	if findLine(fs, LevelWarn, "competing subagent visualizer") != nil {
		t.Fatalf("own entry flagged as visualizer:\n%s", dump(fs))
	}
}

// TestInspectPaneEnv: AGENT_PANE_* and generic *_PANE_* keys get the
// standalone on/off-switch warning; unrelated keys do not.
func TestInspectPaneEnv(t *testing.T) {
	raw := settingsJSON(t, map[string]any{
		"env": map[string]any{"AGENT_PANE_OFF": "0", "MY_PANE_THING": "1", "UNRELATED": "x"},
	})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "on/off switch")
	if f == nil {
		t.Fatalf("missing pane-env warn:\n%s", dump(fs))
	}
	if !strings.Contains(f.Line, "AGENT_PANE_OFF") || !strings.Contains(f.Line, "MY_PANE_THING") {
		t.Fatalf("pane-env warn must list both key classes:\n%s", f.Line)
	}
	if strings.Contains(f.Line, "UNRELATED") {
		t.Fatalf("unrelated env key leaked into the warn:\n%s", f.Line)
	}
	raw = settingsJSON(t, map[string]any{"env": map[string]any{"UNRELATED": "x"}})
	if fs := InspectSettings(raw, fxPath, "", ""); findLine(fs, LevelWarn, "on/off switch") != nil {
		t.Fatalf("no pane keys must mean no env warn:\n%s", dump(fs))
	}
}

// TestInspectAlerting mirrors case 07: osascript on Notification warns with
// the --no-bell pointer; "say" is word-bounded so essay-writer.sh is clean.
func TestInspectAlerting(t *testing.T) {
	raw := settingsJSON(t, map[string]any{"hooks": map[string]any{
		"Notification": []any{cmdGroup(`osascript -e 'display notification "claude"'`)},
		"Stop":         []any{cmdGroup("essay-writer.sh --polish"), cmdGroup("say done")},
	}})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, "existing alerting hook on Notification: osascript")
	if f == nil {
		t.Fatalf("missing Notification alerting warn:\n%s", dump(fs))
	}
	if !detailContains(f, "double alerts") || !detailContains(f, "--no-bell") ||
		!detailContains(f, "--no-badge") || !detailContains(f, "--no-notify") {
		t.Fatalf("alerting warn must carry double-alerts copy and the kill switches:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "existing alerting hook on Stop: say done") == nil {
		t.Fatalf("word-bounded 'say' must match 'say done':\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "essay-writer.sh") != nil {
		t.Fatalf("'essay-writer.sh' must not match the say pattern:\n%s", dump(fs))
	}
}

// TestInspectStatusLine mirrors case 08.
func TestInspectStatusLine(t *testing.T) {
	raw := settingsJSON(t, map[string]any{
		"statusLine": map[string]any{"type": "command", "command": "my-status.sh"},
	})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelInfo, "statusLine")
	if f == nil || !strings.Contains(f.Line, "--oneline") {
		t.Fatalf("missing statusLine/--oneline info:\n%s", dump(fs))
	}
}

// TestInspectNonList mirrors case 16: a non-list event value is a fail line,
// never a panic, and never counts as coverage.
func TestInspectNonList(t *testing.T) {
	raw := []byte(`{"hooks": {"Stop": "my-precious-hook.sh"}}`)
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelFail, "hooks.Stop")
	if f == nil || !strings.Contains(f.Line, "not a list") {
		t.Fatalf("missing non-list fail:\n%s", dump(fs))
	}
}

// TestInspectMalformed mirrors case 09: invalid JSON is one fail line.
func TestInspectMalformed(t *testing.T) {
	for _, raw := range []string{"{ this is not json", "", "[1, 2]", `{"hooks": 3}`} {
		fs := InspectSettings([]byte(raw), fxPath, "", "")
		if countLevel(fs, LevelFail) != 1 {
			t.Fatalf("input %q: want exactly one fail line:\n%s", raw, dump(fs))
		}
	}
	if fs := InspectSettings([]byte("{ nope"), fxPath, "", ""); !strings.Contains(fs[0].Line, "not valid JSON") {
		t.Fatalf("malformed JSON fail line wrong:\n%s", dump(fs))
	}
}

// TestInspectSymlink mirrors case 05: resolvedPath set means the
// managed-elsewhere warning, with the install.sh copy.
func TestInspectSymlink(t *testing.T) {
	raw := settingsJSON(t, map[string]any{"model": "opus"})
	fs := InspectSettings(raw, fxPath, "/fleet/settings.json", "")
	f := findLine(fs, LevelWarn, "is a symlink -> /fleet/settings.json")
	if f == nil {
		t.Fatalf("missing symlink warn:\n%s", dump(fs))
	}
	if !detailContains(f, "managed by another system") || !detailContains(f, "overwrite") {
		t.Fatalf("symlink warn copy incomplete:\n%s", dump(fs))
	}
	if fs := InspectSettings(raw, fxPath, "", ""); findLine(fs, LevelWarn, "symlink") != nil {
		t.Fatalf("no resolvedPath must mean no symlink warn:\n%s", dump(fs))
	}
}

// TestInspectForeignMention mirrors case 17: a foreign command that merely
// mentions an agentpane path is not an agentpane entry, and its path is never
// checked for staleness.
func TestInspectForeignMention(t *testing.T) {
	raw := settingsJSON(t, map[string]any{"hooks": map[string]any{
		"Stop": []any{cmdGroup("notify.sh --after /Users/x/go/bin/agentpane hook --sound pop")},
	}})
	fs := InspectSettings(raw, fxPath, "", "")
	if findLine(fs, LevelInfo, "agentpane entries: none") == nil {
		t.Fatalf("foreign mention counted as an agentpane entry:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "stale agentpane path") != nil {
		t.Fatalf("foreign mention path stat-checked:\n%s", dump(fs))
	}
}

// TestInspectNotLast: a guard command after agentpane's entry on the same
// event is an info line, not a warning.
func TestInspectNotLast(t *testing.T) {
	hooks := allEventsHooks("agentpane hook")
	hooks["PreToolUse"] = []any{
		cmdGroup("agentpane hook"),
		map[string]any{"matcher": "Bash", "hooks": []any{
			map[string]any{"type": "command", "command": "danger-guard.sh", "timeout": 5},
		}},
	}
	raw := settingsJSON(t, map[string]any{"hooks": hooks})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelInfo, "not last on PreToolUse")
	if f == nil || !strings.Contains(f.Line, "danger-guard.sh") {
		t.Fatalf("missing not-last info naming the guard:\n%s", dump(fs))
	}
	if !detailContains(f, "observe-only") {
		t.Fatalf("not-last info must explain order does not affect agentpane:\n%s", dump(fs))
	}
	if countLevel(fs, LevelWarn) != 0 {
		t.Fatalf("ordering alone must not warn:\n%s", dump(fs))
	}
}

// TestIsAgentpaneCmd pins the accept/reject sets of the three recognizers
// shared with install.sh: combined (uninstall matcher), hook-only, and
// autopane-only.
func TestIsAgentpaneCmd(t *testing.T) {
	cases := []struct {
		cmd                     string
		any, hookOnly, autoOnly bool
	}{
		{"agentpane hook", true, true, false},
		{"/Users/x/go/bin/agentpane hook", true, true, false},
		{"agentpane  hook", true, true, false},
		{"agentpane hook --socket /tmp/ap.sock", true, true, false},
		{"  agentpane hook  ", true, true, false},
		{"agentpane autopane", true, false, true},
		{"/Users/x/go/bin/agentpane autopane", true, false, true},
		{"agentpane  autopane", true, false, true},
		{"agentpane autopane --width narrow", true, false, true},
		{"agentpane hooky", false, false, false},
		{"agentpane autopaney", false, false, false},
		{"notify.sh --after /x/agentpane hook", false, false, false},
		{"notify.sh --after /x/agentpane autopane", false, false, false},
		{"my-agentpane hook", false, false, false}, // basename is not "agentpane"
		{"agentpane version", false, false, false},
		{"", false, false, false},
	}
	for _, c := range cases {
		if got := isAgentpaneCmd(c.cmd); got != c.any {
			t.Errorf("isAgentpaneCmd(%q) = %v, want %v", c.cmd, got, c.any)
		}
		if got := isAgentpaneHookCmd(c.cmd); got != c.hookOnly {
			t.Errorf("isAgentpaneHookCmd(%q) = %v, want %v", c.cmd, got, c.hookOnly)
		}
		if got := isAutopaneCmd(c.cmd); got != c.autoOnly {
			t.Errorf("isAutopaneCmd(%q) = %v, want %v", c.cmd, got, c.autoOnly)
		}
	}
}

// TestInspectAutopane: the autopane line reports installed (with the exact
// command) or not installed (with the --autopane pointer); the entry never
// pollutes the 12-event coverage or duplicate checks, and a foreign command
// mentioning an agentpane autopane path does not count.
func TestInspectAutopane(t *testing.T) {
	// Installed: hook coverage complete + autopane group on SessionStart.
	hooks := allEventsHooks("agentpane hook")
	hooks["SessionStart"] = []any{
		cmdGroup("agentpane hook"),
		map[string]any{"matcher": "startup|resume", "hooks": []any{
			map[string]any{"type": "command", "command": "/x/agentpane autopane", "timeout": 5},
		}},
	}
	raw := settingsJSON(t, map[string]any{"hooks": hooks})
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelInfo, "autopane: auto-open on session start installed")
	if f == nil || !strings.Contains(f.Line, "/x/agentpane autopane") {
		t.Fatalf("missing installed autopane info with the command:\n%s", dump(fs))
	}
	if findLine(fs, LevelInfo, "present on all 12 events") == nil {
		t.Fatalf("hook coverage lost with autopane present:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "duplicate agentpane entries") != nil {
		t.Fatalf("autopane entry must not count as a duplicate hook entry:\n%s", dump(fs))
	}
	if findLine(fs, LevelInfo, "not last on SessionStart") != nil {
		t.Fatalf("autopane after the forwarder is an own entry, not a foreign follower:\n%s", dump(fs))
	}

	// Not installed.
	fs = InspectSettings(settingsJSON(t, map[string]any{"hooks": allEventsHooks("agentpane hook")}), fxPath, "", "")
	f = findLine(fs, LevelInfo, "autopane: auto-open on session start: not installed")
	if f == nil || !strings.Contains(f.Line, "`agentpane install --autopane`") {
		t.Fatalf("missing not-installed autopane info:\n%s", dump(fs))
	}

	// Foreign mention is not an autopane entry.
	fs = InspectSettings(settingsJSON(t, map[string]any{"hooks": map[string]any{
		"SessionStart": []any{cmdGroup("notify.sh --after /x/agentpane autopane")},
	}}), fxPath, "", "")
	if findLine(fs, LevelInfo, "autopane: auto-open on session start installed") != nil {
		t.Fatalf("foreign mention counted as autopane installed:\n%s", dump(fs))
	}
}

// TestDoctorCompetingSection: the section renders inside cmdDoctor against a
// fixture HOME (never the real ~/.claude) and doctor still exits 0.
func TestDoctorCompetingSection(t *testing.T) {
	home := t.TempDir()
	setFakeHome(t, home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := settingsJSON(t, map[string]any{
		"env": map[string]any{"AGENT_PANE_FOO": "1"},
		"hooks": map[string]any{
			"SubagentStart": []any{cmdGroup("it2-pane-hook.sh start")},
		},
	})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDoctor(&out, io.Discard, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	s := out.String()
	for _, want := range []string{
		"competing settings",
		"competing subagent visualizer on SubagentStart: it2-pane-hook.sh start",
		"AGENT_PANE_FOO",
		"agentpane entries: none",
		"autopane: auto-open on session start: not installed — `agentpane install --autopane`",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, s)
		}
	}

	// Absent settings: the section reports nothing to scan, and the hooks
	// check above keeps printing the snippet (asserted in TestDoctorRuns).
	if err := os.Remove(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdDoctor(&out, io.Discard, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(out.String(), "nothing to scan") {
		t.Fatalf("absent settings must report nothing to scan:\n%s", out.String())
	}
}

// TestInspectDuplicateKeys: encoding/json parses a duplicated key last-wins
// where the installer refuses the file — the scan proceeds on the last value
// (matching Claude Code's own parse) but flags the duplicate, and the earlier
// shadowed value never fires a finding.
func TestInspectDuplicateKeys(t *testing.T) {
	raw := []byte(`{
  "hooks": {"SubagentStart": [{"matcher": "", "hooks": [{"type": "command", "command": "it2-pane-hook.sh start", "timeout": 5}]}]},
  "hooks": {}
}`)
	fs := InspectSettings(raw, fxPath, "", "")
	f := findLine(fs, LevelWarn, `duplicate key "hooks"`)
	if f == nil {
		t.Fatalf("missing duplicate-key warn:\n%s", dump(fs))
	}
	if !strings.Contains(f.Line, "last value") || !detailContains(f, "the installer refuses") {
		t.Fatalf("duplicate-key copy incomplete:\n%s", dump(fs))
	}
	if findLine(fs, LevelWarn, "competing subagent visualizer") != nil {
		t.Fatalf("shadowed first hooks block must not fire findings (last-wins):\n%s", dump(fs))
	}

	// Nested duplicates are found too.
	fs = InspectSettings([]byte(`{"env": {"AGENT": "1", "AGENT": "2"}}`), fxPath, "", "")
	if findLine(fs, LevelWarn, `duplicate key "AGENT"`) == nil {
		t.Fatalf("missing nested duplicate-key warn:\n%s", dump(fs))
	}

	// No duplicates: no warn.
	fs = InspectSettings(settingsJSON(t, map[string]any{"model": "opus"}), fxPath, "", "")
	if findLine(fs, LevelWarn, "duplicate key") != nil {
		t.Fatalf("clean file flagged for duplicate keys:\n%s", dump(fs))
	}
}

// TestDoctorDanglingSymlink: a settings symlink whose target is missing is a
// broken link, not an absent file — doctor must mirror install.sh's error
// ("fix the link first") instead of advising a fresh file. Fixture HOME only.
func TestDoctorDanglingSymlink(t *testing.T) {
	home := t.TempDir()
	setFakeHome(t, home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "fleet", "settings.json") // never created
	if err := os.Symlink(target, filepath.Join(dir, "settings.json")); err != nil {
		// Creating one is a privilege on Windows (Developer Mode or admin),
		// so an unprivileged run has nothing to test rather than a failure.
		t.Skipf("cannot create a symlink here: %v", err)
	}
	var out bytes.Buffer
	if code := cmdDoctor(&out, io.Discard, nil); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	s := out.String()
	if !strings.Contains(s, "symlink to a missing target") || !strings.Contains(s, target) || !strings.Contains(s, "fix the link first") {
		t.Fatalf("missing dangling-symlink fail line:\n%s", s)
	}
	if strings.Contains(s, "nothing to scan") {
		t.Fatalf("dangling symlink reported as an absent file:\n%s", s)
	}
}

// --- drift guard -------------------------------------------------------------

// TestInstallShParity is the drift guard between inspect.go and install.sh:
// every shared pattern string must appear VERBATIM in install.sh (they live in
// python re.compile(r"...") literals, so the raw text matches byte-for-byte).
// If this test fails, the two implementations have diverged — update both
// files together. Referenced by comments in inspect.go and install.sh.
//
// One deliberate dialect mapping is asserted instead of verbatim reuse:
// AgentpaneCmdPattern uses a Python lookahead `(?=\s|$)` that Go's RE2 cannot
// compile, so the Go form consumes `(\s|$)` and turns the leading `(?:` into
// a capturing `(` for path extraction. Prefix-match semantics are identical.
func TestInstallShParity(t *testing.T) {
	// The EMBEDDED script, not a file on disk: what ships in the binary is
	// what has to agree with doctor, and reading it this way cannot be
	// defeated by the script moving.
	sh := installer.Script()

	verbatim := map[string]string{
		"AgentpaneCmdPattern (uninstall matcher)":             AgentpaneCmdPattern,
		"AgentpaneHookCmdPattern (install recognizer)":        AgentpaneHookCmdPattern,
		"AgentpaneAutopaneCmdPattern (--autopane recognizer)": AgentpaneAutopaneCmdPattern,
		"VisualizerPattern":                                   VisualizerPattern,
		"AlerterPattern":                                      AlerterPattern,
		"AlerterSayPattern":                                   AlerterSayPattern,
		"AgentPaneEnvPattern":                                 AgentPaneEnvPattern,
	}
	for name, pat := range verbatim {
		if !strings.Contains(sh, pat) {
			t.Errorf("%s %q not found verbatim in install.sh — the implementations have drifted", name, pat)
		}
	}

	// The documented Python→Go mapping: first `(?:` becomes a capturing `(`
	// (path extraction), the lookahead `(?=\s|$)` becomes a consuming
	// `(\s|$)` (same accept/reject set for a prefix match).
	pyToGo := func(pat string) string {
		g := strings.Replace(pat, "(?:", "(", 1)
		return strings.Replace(g, `(?=\s|$)`, `(\s|$)`, 1)
	}
	for _, m := range []struct {
		name string
		re   string
		pat  string
	}{
		{"agentpaneCmdRe", agentpaneCmdRe.String(), AgentpaneCmdPattern},
		{"agentpaneHookCmdRe", agentpaneHookCmdRe.String(), AgentpaneHookCmdPattern},
		{"agentpaneAutopaneRe", agentpaneAutopaneRe.String(), AgentpaneAutopaneCmdPattern},
	} {
		if m.re != pyToGo(m.pat) {
			t.Errorf("%s %q is not the documented mapping of its pattern (want %q)", m.name, m.re, pyToGo(m.pat))
		}
	}

	// Both files must keep pointing future editors at this test.
	if !strings.Contains(sh, "TestInstallShParity") {
		t.Error("install.sh no longer mentions TestInstallShParity — restore the parity comment next to its regex block")
	}
}
