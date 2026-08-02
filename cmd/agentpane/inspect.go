package main

// inspect.go — the read-only "competing settings" scan behind `agentpane
// doctor`. It mirrors the inspection phase of install.sh: the same detection
// patterns, the same warning copy, the same semantics — plus richer checks on
// agentpane's own entries (event coverage, stale binary paths, duplicates,
// ordering) that install.sh fixes rather than reports.
//
// PARITY: install.sh is the reference implementation. TestInstallShParity
// (inspect_test.go) asserts every exported *Pattern const below appears
// VERBATIM in install.sh, so the two implementations cannot drift silently.
// Edit the patterns in both files together.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Level classifies a Finding for the doctor's ✔/⚠/✖ rendering.
type Level int

const (
	LevelInfo Level = iota // ✔ informational — nothing to do
	LevelWarn              // ⚠ worth a look — agentpane still works
	LevelFail              // ✖ the file defeats inspection (doctor still exits 0)
)

// Finding is one line of the competing-settings report plus indented detail.
type Finding struct {
	Level  Level
	Line   string
	Detail []string
}

// Pattern sources, shared with install.sh (see the PARITY note above).
const (
	// AgentpaneCmdPattern recognizes ANY of agentpane's own entries: a
	// command that STARTS WITH "agentpane hook"/"agentpane autopane" or
	// "<any path>/agentpane hook"/".../agentpane autopane", optionally with
	// trailing arguments — never a foreign command that merely mentions an
	// agentpane path (e.g. "notify.sh --after /x/agentpane hook"). This is
	// install.sh's AGENTPANE_CMD — its --uninstall matcher — verbatim in the
	// Python dialect. Go's regexp (RE2) has no lookahead, so each compiled
	// regexp below replaces `(?=\s|$)` with a consuming `(\s|$)` (the same
	// accept/reject set for a prefix match) and captures the path prefix;
	// TestInstallShParity asserts that mapping as well.
	AgentpaneCmdPattern = `^(?:\S*/)?agentpane +(?:hook|autopane)(?=\s|$)`

	// AgentpaneHookCmdPattern recognizes only the forwarder entries
	// ("agentpane hook") — install.sh's AGENTPANE_HOOK_CMD, its install
	// recognizer/rebinder for the 12 events.
	AgentpaneHookCmdPattern = `^(?:\S*/)?agentpane +hook(?=\s|$)`

	// AgentpaneAutopaneCmdPattern recognizes only the auto-open entry
	// ("agentpane autopane" on SessionStart) — install.sh's
	// AGENTPANE_AUTOPANE_CMD, added by --autopane and removed alone by
	// --uninstall --autopane.
	AgentpaneAutopaneCmdPattern = `^(?:\S*/)?agentpane +autopane(?=\s|$)`

	// VisualizerPattern flags competing subagent visualizers on
	// SubagentStart/SubagentStop/PreToolUse (matched case-insensitively).
	VisualizerPattern = `pane|visual|dashboard|it2|iterm|tmux|wezterm|kitty`

	// AlerterPattern flags alerting hooks on Notification/Stop (matched
	// case-insensitively). AlerterSayPattern word-bounds "say" separately so
	// that e.g. "essay.sh" does not match.
	AlerterPattern    = `osascript|notifier|bell|afplay`
	AlerterSayPattern = `(^|[^A-Za-z])say([^A-Za-z]|$)`

	// AgentPaneEnvPattern finds settings-env keys that look like another
	// pane tool's own switches.
	AgentPaneEnvPattern = `^AGENT_PANE_`
)

var (
	agentpaneCmdRe      = regexp.MustCompile(`^(\S*/)?agentpane +(?:hook|autopane)(\s|$)`)
	agentpaneHookCmdRe  = regexp.MustCompile(`^(\S*/)?agentpane +hook(\s|$)`)
	agentpaneAutopaneRe = regexp.MustCompile(`^(\S*/)?agentpane +autopane(\s|$)`)
	visualizerRe        = regexp.MustCompile(`(?i)(?:` + VisualizerPattern + `)`)
	alerterRe           = regexp.MustCompile(`(?i)(?:` + AlerterPattern + `)`)
	alerterSayRe        = regexp.MustCompile(`(?i)(?:` + AlerterSayPattern + `)`)
	agentPaneEnvRe      = regexp.MustCompile(AgentPaneEnvPattern)
)

// visualizerEvents and alertEvents are the event subsets install.sh scans.
var (
	visualizerEvents = []string{"SubagentStart", "SubagentStop", "PreToolUse"}
	alertEvents      = []string{"Notification", "Stop"}
)

// isAgentpaneCmd reports whether cmd is ANY of agentpane's own entries —
// forwarder or autopane (install.sh's is_agentpane, the uninstall matcher).
func isAgentpaneCmd(cmd string) bool {
	return agentpaneCmdRe.MatchString(strings.TrimSpace(cmd))
}

// isAgentpaneHookCmd reports whether cmd is a forwarder entry, "agentpane
// hook" (install.sh's is_hook) — the shape the 12-event coverage checks care
// about.
func isAgentpaneHookCmd(cmd string) bool {
	return agentpaneHookCmdRe.MatchString(strings.TrimSpace(cmd))
}

// isAutopaneCmd reports whether cmd is the auto-open entry, "agentpane
// autopane" (install.sh's is_autopane).
func isAutopaneCmd(cmd string) bool {
	return agentpaneAutopaneRe.MatchString(strings.TrimSpace(cmd))
}

// hookEntry is one type=command hook, in file order within its event.
type hookEntry struct {
	group, index int
	cmd          string
}

// commandEntries flattens an event's command hooks in file order, skipping
// malformed shapes without error (install.sh's entries()).
func commandEntries(hooks map[string]any, event string) []hookEntry {
	groups, _ := hooks[event].([]any)
	var out []hookEntry
	for gi, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hl, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for hi, h := range hl {
			hm, ok := h.(map[string]any)
			if !ok || hm["type"] != "command" {
				continue
			}
			cmd, _ := hm["command"].(string)
			out = append(out, hookEntry{gi, hi, cmd})
		}
	}
	return out
}

// InspectSettings runs the competing-settings scan over raw settings.json
// content and returns the report. It is read-only and never panics on any
// input (C9). The caller does the filesystem work for the symlink check:
// resolvedPath is the readlink/realpath target when settingsPath is a
// symlink, "" otherwise. binaryPath is the running agentpane binary ("" to
// skip the differs-from-running-binary check). The only filesystem access
// here is an os.Stat existence probe on binary paths found inside commands.
func InspectSettings(raw []byte, settingsPath, resolvedPath string, binaryPath string) []Finding {
	var findings []Finding
	add := func(level Level, line string, detail ...string) {
		findings = append(findings, Finding{Level: level, Line: line, Detail: detail})
	}

	// (a) symlink: settings managed by another system. Formatting only — the
	// lstat/readlink happened in the caller.
	if resolvedPath != "" {
		add(LevelWarn,
			fmt.Sprintf("%s is a symlink -> %s", settingsPath, resolvedPath),
			"these settings look managed by another system (a dotfiles repo or fleet-style installer)",
			fmt.Sprintf("an install.sh edit will land in %s; that system's installer may overwrite it,", resolvedPath),
			"or the change may show up as an uncommitted diff in its repo -- consider committing it there too")
	}

	// (g) parse. Any structural problem is a fail line, never a panic.
	var top any
	if err := json.Unmarshal(raw, &top); err != nil {
		add(LevelFail, fmt.Sprintf("%s is not valid JSON (%v) — install.sh refuses to touch it; fix the file by hand", settingsPath, err))
		return findings
	}
	data, ok := top.(map[string]any)
	if !ok {
		add(LevelFail, fmt.Sprintf("%s does not contain a JSON object at the top level — install.sh refuses to touch it", settingsPath))
		return findings
	}

	// (g2) duplicate keys. encoding/json silently keeps only the last value,
	// so install.sh refuses such a file outright ("not valid JSON (duplicate
	// key ...)"). Doctor deviates deliberately: last-wins matches Claude
	// Code's own parse, so the scan below stays accurate — but the file must
	// not get an unqualified clean bill.
	if key := firstDuplicateKey(raw); key != "" {
		add(LevelWarn, fmt.Sprintf("duplicate key %q in %s — this scan (like Claude Code) reads the last value only", key, settingsPath),
			"install.sh refuses to touch a file with duplicate keys; fix the duplicate by hand")
	}
	hooks := map[string]any{}
	if hooksAny, has := data["hooks"]; has {
		hm, isObj := hooksAny.(map[string]any)
		if !isObj {
			add(LevelFail, fmt.Sprintf("the \"hooks\" key in %s is not an object — install.sh refuses to touch it", settingsPath))
		} else {
			hooks = hm
			for _, ev := range hookEvents {
				if v, present := hm[ev]; present {
					if _, isList := v.([]any); !isList {
						add(LevelFail, fmt.Sprintf("hooks.%s in %s is not a list — install.sh refuses to touch it", ev, settingsPath))
					}
				}
			}
		}
	}

	// Env keys that look like another pane tool's switches. apEnv (the
	// AGENT_PANE_ prefix) is what install.sh names inside its visualizer
	// warning; paneEnv is the generic *_PANE_* superset doctor adds.
	envBlock, _ := data["env"].(map[string]any)
	var apEnv, paneEnv []string
	for k := range envBlock {
		switch {
		case agentPaneEnvRe.MatchString(k):
			apEnv = append(apEnv, k)
		case strings.Contains(k, "_PANE_"):
			paneEnv = append(paneEnv, k)
		}
	}
	sort.Strings(apEnv)
	sort.Strings(paneEnv)

	// (b) competing subagent visualizers.
	for _, ev := range visualizerEvents {
		for _, e := range commandEntries(hooks, ev) {
			if isAgentpaneCmd(e.cmd) || !visualizerRe.MatchString(e.cmd) {
				continue
			}
			detail := []string{"running two subagent UIs doubles panes and notifications; consider disabling one"}
			if len(apEnv) > 0 {
				detail = append(detail, fmt.Sprintf("env vars %s in this settings file suggest that tool has its own on/off switch there", strings.Join(apEnv, ", ")))
			}
			add(LevelWarn, fmt.Sprintf("competing subagent visualizer on %s: %s", ev, e.cmd), detail...)
		}
	}

	// (c) pane-tool env keys, standalone.
	if len(apEnv)+len(paneEnv) > 0 {
		add(LevelWarn,
			fmt.Sprintf("env: %s set in the settings env block — that tool may have its own on/off switch there", strings.Join(append(append([]string{}, apEnv...), paneEnv...), ", ")))
	}

	// (d) duplicate alerting.
	for _, ev := range alertEvents {
		for _, e := range commandEntries(hooks, ev) {
			if isAgentpaneCmd(e.cmd) {
				continue
			}
			if alerterRe.MatchString(e.cmd) || alerterSayRe.MatchString(e.cmd) {
				add(LevelWarn, fmt.Sprintf("existing alerting hook on %s: %s", ev, e.cmd),
					"agentpane also rings/badges on permission prompts, so expect double alerts;",
					"see agentpane --no-bell / --no-badge / --no-notify or the bell/badge/notify config keys")
			}
		}
	}

	// (e) status line.
	if _, has := data["statusLine"]; has {
		add(LevelInfo, "a statusLine is configured; agentpane --oneline can replace or double a status line")
	}

	// (f) agentpane's own entries — richer than install.sh, which fixes these
	// silently instead of reporting them.
	var covered, missing []string
	staleEvents := map[string][]string{}   // command binary path -> events (path gone)
	differsEvents := map[string][]string{} // command binary path -> events (path != running binary)
	var dupes []string
	type orderHit struct{ ev, cmd string }
	var notLast []orderHit

	for _, ev := range hookEvents {
		es := commandEntries(hooks, ev)
		// Coverage/duplicate/stale checks look at FORWARDER entries only:
		// the autopane entry coexisting on SessionStart is neither coverage
		// for that event nor a duplicate of the forwarder.
		var apIdx []int
		for i, e := range es {
			if isAgentpaneHookCmd(e.cmd) {
				apIdx = append(apIdx, i)
			}
		}
		if len(apIdx) == 0 {
			missing = append(missing, ev)
			continue
		}
		covered = append(covered, ev)
		if len(apIdx) > 1 {
			dupes = append(dupes, fmt.Sprintf("%s (%d)", ev, len(apIdx)))
		}
		for _, i := range apIdx {
			m := agentpaneHookCmdRe.FindStringSubmatch(strings.TrimSpace(es[i].cmd))
			if m == nil || m[1] == "" {
				continue // bare "agentpane hook": PATH-resolved, as the doctor snippet prescribes
			}
			binPath := expandTilde(m[1] + "agentpane")
			if !filepath.IsAbs(binPath) {
				continue // relative prefix: existence depends on the hook's cwd, not checkable
			}
			switch {
			case !fileExists(binPath):
				staleEvents[binPath] = append(staleEvents[binPath], ev)
			case binaryPath != "" && !samePath(binPath, binaryPath):
				differsEvents[binPath] = append(differsEvents[binPath], ev)
			}
		}
		// Ordering: an exit-2-style guard after agentpane's (last) entry.
		for i := apIdx[len(apIdx)-1] + 1; i < len(es); i++ {
			if !isAgentpaneCmd(es[i].cmd) {
				notLast = append(notLast, orderHit{ev, es[i].cmd})
				break
			}
		}
	}

	switch {
	case len(covered) == 0:
		add(LevelInfo, fmt.Sprintf("agentpane entries: none in %s — the hooks line above prints the snippet to paste", settingsPath))
	case len(missing) == 0:
		add(LevelInfo, fmt.Sprintf("agentpane entries: present on all %d events", len(hookEvents)))
	default:
		add(LevelWarn, fmt.Sprintf("agentpane entries: partial coverage — missing %s", strings.Join(missing, ", ")),
			"run ./install.sh to append the missing events (existing hook groups are left untouched)")
	}

	for _, p := range sortedKeys(staleEvents) {
		add(LevelWarn, fmt.Sprintf("stale agentpane path on %s: %s no longer exists", strings.Join(staleEvents[p], ", "), p),
			"hooks run with an unpredictable PATH, so a dead absolute path means those events are silently lost",
			"run ./install.sh to rewrite the binary path in place (trailing arguments are preserved)")
	}
	for _, p := range sortedKeys(differsEvents) {
		add(LevelWarn, fmt.Sprintf("agentpane path on %s is %s — not the running binary (%s)", strings.Join(differsEvents[p], ", "), p, binaryPath),
			"hook events go to that other binary; run ./install.sh to rewrite the path in place if this one should receive them")
	}
	if len(dupes) > 0 {
		add(LevelWarn, fmt.Sprintf("duplicate agentpane entries on %s", strings.Join(dupes, ", ")),
			"each duplicate forwards the same event twice; ./install.sh prunes exact duplicates on re-run")
	}
	for _, h := range notLast {
		add(LevelInfo, fmt.Sprintf("agentpane is not last on %s: %q runs after it", h.ev, h.cmd),
			"order does not affect agentpane (observe-only, exits 0), but an exit-2-style guard placed after it can still veto the call agentpane already drew")
	}

	// (h) autopane — the auto-open-on-session-start entry lives on
	// SessionStart only (install.sh --autopane).
	var autopaneCmds []string
	for _, e := range commandEntries(hooks, "SessionStart") {
		if isAutopaneCmd(e.cmd) {
			autopaneCmds = append(autopaneCmds, e.cmd)
		}
	}
	if len(autopaneCmds) > 0 {
		add(LevelInfo, fmt.Sprintf("autopane: auto-open on session start installed (command: %s)", autopaneCmds[0]))
	} else {
		add(LevelInfo, "autopane: auto-open on session start: not installed — ./install.sh --autopane")
	}

	return findings
}

// firstDuplicateKey walks raw's token stream and returns the first key that
// appears twice within one object ("" when none). Only called after
// json.Unmarshal accepted raw, so the stream is well-formed; any residual
// token error just ends the walk with no finding.
func firstDuplicateKey(raw []byte) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() string
	walk = func() string {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		delim, isDelim := tok.(json.Delim)
		if !isDelim {
			return "" // scalar
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return ""
				}
				key, _ := kt.(string)
				if seen[key] {
					return key
				}
				seen[key] = true
				if dup := walk(); dup != "" {
					return dup
				}
			}
			dec.Token() //nolint:errcheck // consume '}' — already validated
		case '[':
			for dec.More() {
				if dup := walk(); dup != "" {
					return dup
				}
			}
			dec.Token() //nolint:errcheck // consume ']' — already validated
		}
		return ""
	}
	return walk()
}

// expandTilde expands a leading ~/ so hand-written hook commands like
// "~/go/bin/agentpane hook" stat correctly.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// fileExists is a read-only stat probe.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// samePath compares a command's binary path against the running binary,
// tolerating a symlink on the command side (e.g. ~/go/bin/agentpane linked at
// the resolved executable).
func samePath(cmdPath, runningPath string) bool {
	if filepath.Clean(cmdPath) == filepath.Clean(runningPath) {
		return true
	}
	if r, err := filepath.EvalSymlinks(cmdPath); err == nil && filepath.Clean(r) == filepath.Clean(runningPath) {
		return true
	}
	return false
}

// sortedKeys keeps map-grouped findings deterministic.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
