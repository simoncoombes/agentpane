package installer

import (
	"strings"
	"testing"
)

// planFor is the common shape: an install against the given existing file.
func planFor(t *testing.T, old string, in planInput) plan {
	t.Helper()
	in.settingsPath = `C:\Users\x\.claude\settings.json`
	in.oldText = old
	in.exists = old != ""
	if in.binary == "" && !in.uninstall {
		in.binary = `C:\Users\x\go\bin\agentpane.exe`
	}
	p, err := computePlan(in)
	if err != nil {
		t.Fatalf("computePlan: %v", err)
	}
	return p
}

// --- recognizing agentpane's own entries -----------------------------------

// This is the test that earns the parser. Every one of these is an entry a
// Windows install would write or read back, and the unix regex
// (^(?:\S*/)?agentpane +hook) sees none of them.
func TestRecognizesWindowsCommands(t *testing.T) {
	hooks := []string{
		`C:\Users\x\go\bin\agentpane.exe hook`,
		`C:\Users\x\go\bin\AgentPane.EXE hook`,
		`"C:\Program Files\agentpane\agentpane.exe" hook`,
		`agentpane.exe hook`,
		`agentpane hook`,
		`/home/x/.local/bin/agentpane hook`,
		`C:\bin\agentpane.exe hook --columns 80`,
	}
	for _, c := range hooks {
		if !isHookCmd(c) {
			t.Errorf("isHookCmd(%q) = false; a missed entry means a duplicate on every re-run", c)
		}
		if !isAgentpaneCmd(c) {
			t.Errorf("isAgentpaneCmd(%q) = false", c)
		}
		if isAutopaneCmd(c) {
			t.Errorf("isAutopaneCmd(%q) = true", c)
		}
	}
	for _, c := range []string{
		`C:\bin\agentpane.exe autopane`,
		`"C:\Program Files\ap\agentpane.exe" autopane --on-prompt`,
	} {
		if !isAutopaneCmd(c) {
			t.Errorf("isAutopaneCmd(%q) = false", c)
		}
		if isHookCmd(c) {
			t.Errorf("isHookCmd(%q) = true", c)
		}
	}
}

// A foreign command that merely mentions agentpane is never rewritten or
// removed — the program has to be the first token.
func TestDoesNotClaimForeignCommands(t *testing.T) {
	foreign := []string{
		`notify.cmd --after C:\bin\agentpane.exe hook`,
		`powershell -c "agentpane hook"`,
		`C:\bin\agentpane-helper.exe hook`,
		`C:\bin\myagentpane.exe hook`,
		`C:\bin\agentpane.exe doctor`,
		`C:\bin\agentpane.exe`,
		``,
		`"C:\unterminated\agentpane.exe hook`,
	}
	for _, c := range foreign {
		if isAgentpaneCmd(c) {
			t.Errorf("isAgentpaneCmd(%q) = true; this command is not ours to touch", c)
		}
	}
}

// --- install ----------------------------------------------------------------

func TestInstallOnEmptyFile(t *testing.T) {
	p := planFor(t, "", planInput{})
	if p.status != statusChanges {
		t.Fatalf("status = %v, want changes", p.status)
	}
	for _, ev := range hookEvents {
		if !strings.Contains(p.proposed, `"`+ev+`"`) {
			t.Errorf("proposed settings has no %s group", ev)
		}
	}
	if strings.Contains(p.proposed, "autopane") {
		t.Error("autopane entry added without --autopane")
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	first := planFor(t, "", planInput{})
	second := planFor(t, first.proposed, planInput{})
	if second.status != statusAlready {
		t.Fatalf("a second install reported %v, want already:\n%s", second.status, strings.Join(second.report, "\n"))
	}
}

func TestAutopaneAddsOneSessionStartGroup(t *testing.T) {
	p := planFor(t, "", planInput{autopane: true})
	if n := strings.Count(p.proposed, "autopane"); n != 1 {
		t.Errorf("autopane appears %d times, want 1:\n%s", n, p.proposed)
	}
	if !strings.Contains(p.proposed, `"startup|resume"`) {
		t.Errorf("the autopane group is not matched to startup|resume:\n%s", p.proposed)
	}
	again := planFor(t, p.proposed, planInput{autopane: true})
	if again.status != statusAlready {
		t.Errorf("re-running --autopane wanted to change something:\n%s", strings.Join(again.report, "\n"))
	}
}

// A rebuilt or moved binary rebinds the existing entries in place. Appending
// instead would leave the old path firing forever.
func TestRebindsMovedBinary(t *testing.T) {
	old := planFor(t, "", planInput{binary: `C:\old\agentpane.exe`}).proposed
	p := planFor(t, old, planInput{binary: `C:\new\agentpane.exe`})
	if p.status != statusChanges {
		t.Fatalf("a moved binary changed nothing")
	}
	if strings.Contains(p.proposed, `C:\\old\\agentpane.exe`) {
		t.Errorf("the old path survived:\n%s", p.proposed)
	}
	if n := strings.Count(p.proposed, `agentpane.exe hook`); n != len(hookEvents) {
		t.Errorf("hook entries = %d, want %d — a rebind must not append", n, len(hookEvents))
	}
}

// User-added arguments survive a rebind.
func TestRebindKeepsArguments(t *testing.T) {
	old := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"C:\\old\\agentpane.exe hook --quiet","timeout":5}]}]}}`
	p := planFor(t, old, planInput{binary: `C:\new\agentpane.exe`})
	if !strings.Contains(p.proposed, "--quiet") {
		t.Errorf("the rebind dropped a user argument:\n%s", p.proposed)
	}
}

// A path with a space is quoted, and the quoted form is recognised next time.
func TestQuotesPathsWithSpaces(t *testing.T) {
	p := planFor(t, "", planInput{binary: `C:\Program Files\agentpane\agentpane.exe`})
	if !strings.Contains(p.proposed, `\"C:\\Program Files`) {
		t.Errorf("a path with a space was written unquoted:\n%s", p.proposed)
	}
	again := planFor(t, p.proposed, planInput{binary: `C:\Program Files\agentpane\agentpane.exe`})
	if again.status != statusAlready {
		t.Errorf("a quoted entry was not recognised on re-run; this would duplicate every time:\n%s",
			strings.Join(again.report, "\n"))
	}
}

// Somebody else's hooks are reported and left exactly as they were.
func TestLeavesForeignHooksAlone(t *testing.T) {
	old := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"C:\\tools\\notify.exe","timeout":5}]}]}}`
	p := planFor(t, old, planInput{})
	if !strings.Contains(p.proposed, `notify.exe`) {
		t.Errorf("a foreign hook was removed:\n%s", p.proposed)
	}
	if !reportHas(p, "coexisting hooks left untouched") {
		t.Errorf("coexisting hooks were not reported:\n%s", strings.Join(p.report, "\n"))
	}
}

func TestWarnsAboutCompetingVisualizerAndAlerter(t *testing.T) {
	old := `{"hooks":{"SubagentStart":[{"matcher":"","hooks":[{"type":"command","command":"C:\\t\\tmux-pane.exe","timeout":5}]}],` +
		`"Notification":[{"matcher":"","hooks":[{"type":"command","command":"C:\\t\\bell.exe","timeout":5}]}]}}`
	p := planFor(t, old, planInput{})
	if !reportHas(p, "competing subagent visualizer") {
		t.Errorf("no visualizer warning:\n%s", strings.Join(p.report, "\n"))
	}
	if !reportHas(p, "existing alerting hook") {
		t.Errorf("no alerting warning:\n%s", strings.Join(p.report, "\n"))
	}
}

// --- refusals ---------------------------------------------------------------

func TestRefusesFilesItDoesNotUnderstand(t *testing.T) {
	cases := []struct{ name, text string }{
		{"malformed JSON", `{"hooks":`},
		{"not an object", `["a","b"]`},
		{"hooks is not an object", `{"hooks":[]}`},
		{"an event is not a list", `{"hooks":{"Stop":{}}}`},
		{"duplicate keys", `{"a":1,"a":2}`},
	}
	for _, c := range cases {
		_, err := computePlan(planInput{
			settingsPath: "s.json", oldText: c.text, exists: true,
			binary: `C:\bin\agentpane.exe`,
		})
		if err == nil {
			t.Errorf("%s: accepted, want a refusal", c.name)
		}
	}
}

// --- uninstall --------------------------------------------------------------

func TestUninstallRemovesEverythingAgentpane(t *testing.T) {
	installed := planFor(t, "", planInput{autopane: true}).proposed
	p := planFor(t, installed, planInput{uninstall: true})
	if p.status != statusChanges {
		t.Fatalf("nothing to uninstall from a fresh install")
	}
	if strings.Contains(p.proposed, "agentpane") {
		t.Errorf("an agentpane entry survived uninstall:\n%s", p.proposed)
	}
}

// --uninstall --autopane removes only the auto-open entry.
func TestUninstallAutopaneOnly(t *testing.T) {
	installed := planFor(t, "", planInput{autopane: true}).proposed
	p := planFor(t, installed, planInput{uninstall: true, autopane: true})
	if strings.Contains(p.proposed, "autopane") {
		t.Errorf("the autopane entry survived:\n%s", p.proposed)
	}
	if n := strings.Count(p.proposed, " hook"); n != len(hookEvents) {
		t.Errorf("hook entries = %d, want all %d kept:\n%s", n, len(hookEvents), p.proposed)
	}
}

func TestUninstallKeepsForeignHooks(t *testing.T) {
	old := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"C:\\t\\notify.exe","timeout":5}]},` +
		`{"matcher":"","hooks":[{"type":"command","command":"C:\\bin\\agentpane.exe hook","timeout":5}]}]}}`
	p := planFor(t, old, planInput{uninstall: true})
	if !strings.Contains(p.proposed, "notify.exe") {
		t.Errorf("uninstall removed a foreign hook:\n%s", p.proposed)
	}
	if strings.Contains(p.proposed, "agentpane") {
		t.Errorf("agentpane entry survived:\n%s", p.proposed)
	}
}

func TestUninstallOnAMissingFile(t *testing.T) {
	p, err := computePlan(planInput{settingsPath: "s.json", uninstall: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.status != statusNothing {
		t.Errorf("status = %v, want nothing", p.status)
	}
}

// The rest of the user's settings survive untouched and unreordered.
func TestPreservesUnrelatedSettings(t *testing.T) {
	old := `{
  "theme": "dark",
  "zzz": 1,
  "aaa": 2,
  "permissions": {"defaultMode": "bypassPermissions"}
}
`
	p := planFor(t, old, planInput{})
	for _, want := range []string{`"theme": "dark"`, `"zzz": 1`, `"aaa": 2`, `"bypassPermissions"`} {
		if !strings.Contains(p.proposed, want) {
			t.Errorf("lost %s:\n%s", want, p.proposed)
		}
	}
	if strings.Index(p.proposed, `"zzz"`) > strings.Index(p.proposed, `"aaa"`) {
		t.Errorf("keys were reordered:\n%s", p.proposed)
	}
	// The hooks block is appended, so it lands last rather than in the middle.
	if strings.Index(p.proposed, `"hooks"`) < strings.Index(p.proposed, `"permissions"`) {
		t.Errorf("the hooks block was not appended at the end:\n%s", p.proposed)
	}
}

func reportHas(p plan, substr string) bool {
	for _, l := range p.report {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// TestHookPatternParity is the drift guard between this file and install.sh.
//
// The warning heuristics must be VERBATIM: they decide what a user is told
// about their other tools, and two installers disagreeing about that on the
// same machine would be a bug nobody could explain. The agentpane-command
// recognizer is deliberately NOT shared — see the comment above
// isAgentpaneCmd — so what is asserted for it is the opposite: that
// install.sh still carries the unix-only regex, i.e. that nobody has
// "unified" the two without reading why they differ.
func TestHookPatternParity(t *testing.T) {
	sh := Script()

	verbatim := map[string]string{
		"VisualizerPattern":  `pane|visual|dashboard|it2|iterm|tmux|wezterm|kitty`,
		"AlerterPattern":     `osascript|notifier|bell|afplay`,
		"AlerterSayPattern":  `(^|[^A-Za-z])say([^A-Za-z]|$)`,
		"AgentPaneEnvPrefix": `^AGENT_PANE_`,
	}
	for name, pat := range verbatim {
		if !strings.Contains(sh, pat) {
			t.Errorf("%s has drifted: %q is not in install.sh", name, pat)
		}
		if !strings.Contains(patternSourceOf(t, name), pat) {
			t.Errorf("%s has drifted from hooks.go: %q", name, pat)
		}
	}

	// The 12 events, in the same order, because the two installers manage the
	// same set and a missing one would be silently unhooked.
	for _, ev := range hookEvents {
		if !strings.Contains(sh, `"`+ev+`"`) {
			t.Errorf("install.sh does not manage %s but hooks.go does", ev)
		}
	}

	if !strings.Contains(sh, `^(?:\S*/)?agentpane +hook(?=\s|$)`) {
		t.Error("install.sh's own recognizer has changed; hooks.go parses instead of matching " +
			"on purpose (Windows paths), so check whether that reasoning still holds before syncing them")
	}
}

// patternSourceOf returns the regexp source hooks.go compiled for a pattern,
// so the assertion above compares what the code USES rather than a second
// copy of the string written in the test.
func patternSourceOf(t *testing.T, name string) string {
	t.Helper()
	switch name {
	case "VisualizerPattern":
		return visualizerRe.String()
	case "AlerterPattern":
		return alerterRe.String()
	case "AlerterSayPattern":
		return alerterSayRe.String()
	case "AgentPaneEnvPrefix":
		return agentPaneEnvRe.String()
	}
	t.Fatalf("unknown pattern %q", name)
	return ""
}
