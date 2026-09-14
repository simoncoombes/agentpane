package installer

// hooks.go — what an install or uninstall would change in settings.json.
//
// This is the surgery install.sh performs, expressed as a pure function:
// text in, a plan and the proposed new text out, no I/O and no prompting.
// installer_windows.go is the only caller today (Windows has neither bash nor
// python3), but the logic lives here, untagged, so that the part that can
// actually be wrong is tested on every platform CI runs rather than on the
// one that cannot run it.
//
// The rules it implements, all of them inherited from the script:
//
//   - agentpane's entries are recognised by a command that STARTS WITH
//     "agentpane hook"/"agentpane autopane", optionally path-qualified. A
//     foreign hook that merely mentions an agentpane path is never touched.
//   - installing is idempotent, and re-running after a rebuild REBINDS the
//     path in place rather than appending a second entry.
//   - hook groups coexist: agentpane appends its own group and never edits
//     anybody else's.
//   - nothing is written when nothing would change.
//
// PARITY: the pattern strings below are the ones install.sh compiles, and
// cmd/agentpane/inspect.go carries a third copy for `agentpane doctor`.
// TestHookPatternParity asserts this file agrees with the embedded script;
// TestInstallShParity does the same for inspect.go. Edit them together.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// hookEvents are the 12 events the forwarder is installed on.
var hookEvents = []string{
	"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop",
	"SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
	"Notification", "PermissionRequest", "PreCompact", "PostCompact",
}

// The heuristics install.sh uses to warn about OTHER tools, verbatim: these
// are substring searches over a command line and need no dialect mapping.
var (
	visualizerRe   = regexp.MustCompile(`(?i)(?:pane|visual|dashboard|it2|iterm|tmux|wezterm|kitty)`)
	alerterRe      = regexp.MustCompile(`(?i)(?:osascript|notifier|bell|afplay)`)
	alerterSayRe   = regexp.MustCompile(`(?i)(?:(^|[^A-Za-z])say([^A-Za-z]|$))`)
	agentPaneEnvRe = regexp.MustCompile(`^AGENT_PANE_`)
)

// Recognizing agentpane's OWN entries is where this file deliberately parts
// company with install.sh's regex, which is `^(?:\S*/)?agentpane +hook`.
//
// That pattern cannot see a Windows entry at all. It wants "/" separators, it
// wants the basename to be exactly "agentpane" where Windows writes
// "agentpane.exe", and it is built from \S* so it stops at the first space —
// while `"C:\Program Files\agentpane\agentpane.exe" hook` is a command line
// a Windows user can legitimately end up with, quotes and all. A recognizer
// that misses an existing entry does not merely fail to see it: it appends a
// SECOND one on every re-run, and leaves all of them behind on --uninstall.
//
// So the command is parsed rather than matched. Nothing on the unix side
// changes — install.sh remains the only installer there, and this file is
// compiled into the Windows path alone.
func isAgentpaneCmd(c string) bool { return agentpaneVerb(c) != "" }
func isHookCmd(c string) bool      { return agentpaneVerb(c) == "hook" }
func isAutopaneCmd(c string) bool  { return agentpaneVerb(c) == "autopane" }

// agentpaneVerb returns "hook" or "autopane" when cmd invokes an agentpane
// binary with that subcommand, and "" for anything else — including a foreign
// command that merely mentions an agentpane path, which is why the program
// has to be the FIRST token rather than anywhere in the line.
func agentpaneVerb(cmd string) string {
	prog, rest := splitProgram(strings.TrimSpace(cmd))
	if !isAgentpaneProgram(prog) {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "hook", "autopane":
		return fields[0]
	}
	return ""
}

// splitProgram splits a command line into its program and everything after,
// understanding the one quoting rule that matters here: a program path
// wrapped in double quotes, which is how a Windows path containing a space
// has to be written.
func splitProgram(cmd string) (program, rest string) {
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : 1+end], cmd[2+end:]
		}
		return cmd, "" // an unterminated quote is not a command we manage
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i], cmd[i:]
	}
	return cmd, ""
}

// quoteIfNeeded wraps a program path in double quotes when it contains a
// space, which is what makes `C:\Program Files\...\agentpane.exe hook` a
// command rather than an attempt to run `C:\Program`. splitProgram reads the
// result back, so an entry written this way is still recognised on the next
// run and still removed by --uninstall.
func quoteIfNeeded(path string) string {
	if path == "" || !strings.ContainsAny(path, " \t") {
		return path
	}
	if strings.HasPrefix(path, `"`) {
		return path
	}
	return `"` + path + `"`
}

// windowsExeExts are the extensions a Windows entry may carry.
var windowsExeExts = []string{".exe", ".bat", ".cmd", ".com"}

// isAgentpaneProgram reports whether a program path names agentpane, under
// either separator and with or without a Windows executable extension.
//
// Case is folded only for a path that LOOKS like a Windows one — a
// backslash, a drive letter, or an executable extension. Windows genuinely
// does not distinguish AgentPane.EXE from agentpane.exe, and missing an entry
// over its case would append a second one on every re-run; but /opt/AgentPane
// on a case-sensitive filesystem is a different file from /opt/agentpane, and
// claiming it would be wrong. Deciding on the SHAPE rather than on GOOS keeps
// this a pure function, so one test covers both readings on any machine.
func isAgentpaneProgram(prog string) bool {
	base := prog
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	lower := strings.ToLower(base)

	windowsish := strings.Contains(prog, `\`) || hasDriveLetter(prog)
	for _, ext := range windowsExeExts {
		if strings.HasSuffix(lower, ext) {
			base, windowsish = base[:len(base)-len(ext)], true
			break
		}
	}
	if windowsish {
		return strings.EqualFold(base, "agentpane")
	}
	return base == "agentpane"
}

// hasDriveLetter reports whether a path starts with a Windows drive, as in
// C:\ or c:/.
func hasDriveLetter(p string) bool {
	if len(p) < 3 || p[1] != ':' {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z' && (p[2] == '\\' || p[2] == '/')
}

// planStatus is what the caller does next.
type planStatus int

const (
	statusAlready planStatus = iota // already installed with this binary
	statusNothing                   // nothing to uninstall
	statusChanges                   // proposed holds new content
)

// planInput is everything the plan needs to know about the world. Taking it
// as data rather than reading the filesystem is what makes the rules above
// testable without one.
type planInput struct {
	settingsPath  string
	oldText       string
	exists        bool
	symlinkTarget string // "" unless settingsPath is a symlink
	binary        string
	uninstall     bool
	autopane      bool
}

// plan is the report plus the proposed file content.
type plan struct {
	report   []string // the [warn]/[info]/[plan] lines, in the order to print
	proposed string   // the new file content; empty unless status is statusChanges
	status   planStatus
}

func (p *plan) out(prefix, format string, args ...any) {
	p.report = append(p.report, fmt.Sprintf("["+prefix+"] "+format, args...))
}

// hookEntry is one command hook, located well enough to edit or delete.
type hookEntry struct {
	event   string
	group   int
	index   int
	command string
}

// entriesOf walks an event's groups and yields its command hooks. Anything
// malformed is skipped rather than rejected: a settings file may legitimately
// contain shapes this installer does not manage.
func entriesOf(hooks *jvalue, event string) []hookEntry {
	var out []hookEntry
	groups := hooks.get(event)
	if !groups.isArray() {
		return nil
	}
	for gi, g := range groups.items {
		if !g.isObject() {
			continue
		}
		hl := g.get("hooks")
		if !hl.isArray() {
			continue
		}
		for hi, h := range hl.items {
			if !h.isObject() || h.get("type").str() != "command" {
				continue
			}
			out = append(out, hookEntry{event, gi, hi, h.get("command").str()})
		}
	}
	return out
}

// newGroup builds the group agentpane appends: its own, never shared.
func newGroup(matcher, command string) *jvalue {
	hook := newObject()
	hook.set("type", newString("command"))
	hook.set("command", newString(command))
	hook.set("timeout", newNumber(5))

	list := newArray()
	list.items = append(list.items, hook)

	g := newObject()
	g.set("matcher", newString(matcher))
	g.set("hooks", list)
	return g
}

// computePlan is the whole decision. It never touches the filesystem.
func computePlan(in planInput) (plan, error) {
	var p plan

	data := newObject()
	if in.exists {
		parsed, err := parseJSON([]byte(in.oldText))
		if err != nil {
			return p, fmt.Errorf("%s is not valid JSON (%v)\nrefusing to touch it; fix the file by hand and rerun", in.settingsPath, err)
		}
		if !parsed.isObject() {
			return p, fmt.Errorf("%s does not contain a JSON object at the top level; refusing to touch it", in.settingsPath)
		}
		data = parsed
		if h := data.get("hooks"); h != nil && !h.isObject() {
			return p, fmt.Errorf("the \"hooks\" key in %s is not an object; refusing to touch it", in.settingsPath)
		}
		if h := data.get("hooks"); h.isObject() {
			for _, ev := range hookEvents {
				if e := h.get(ev); e != nil && !e.isArray() {
					return p, fmt.Errorf("hooks.%s in %s is not a list; refusing to touch it", ev, in.settingsPath)
				}
			}
		}
	}

	if in.symlinkTarget != "" {
		p.out("warn", "%s is a symlink -> %s", in.settingsPath, in.symlinkTarget)
		p.out("warn", "these settings look managed by another system (a dotfiles repo or fleet-style installer)")
		p.out("warn", "this edit will land in %s; that system's installer may overwrite it,", in.symlinkTarget)
		p.out("warn", "or the change may show up as an uncommitted diff in its repo -- consider committing it there too")
	}

	hooks := data.get("hooks")
	if !hooks.isObject() {
		hooks = newObject()
	}

	if in.uninstall {
		return planUninstall(&p, data, in)
	}
	return planInstall(&p, data, hooks, in)
}

func planInstall(p *plan, data, hooks *jvalue, in planInput) (plan, error) {
	// ---- warning: competing subagent visualizers ----
	var apEnv []string
	if env := data.get("env"); env.isObject() {
		for _, k := range env.keys {
			if agentPaneEnvRe.MatchString(k) {
				apEnv = append(apEnv, k)
			}
		}
		sort.Strings(apEnv)
	}
	for _, ev := range []string{"SubagentStart", "SubagentStop", "PreToolUse"} {
		for _, e := range entriesOf(hooks, ev) {
			if isAgentpaneCmd(e.command) || !visualizerRe.MatchString(e.command) {
				continue
			}
			p.out("warn", "competing subagent visualizer on %s: %s", ev, e.command)
			p.out("warn", "running two subagent UIs doubles panes and notifications; consider disabling one")
			if len(apEnv) > 0 {
				p.out("warn", "env vars %s in this settings file suggest that tool has its own on/off switch there",
					strings.Join(apEnv, ", "))
			}
		}
	}

	// ---- warning: duplicate alerting ----
	for _, ev := range []string{"Notification", "Stop"} {
		for _, e := range entriesOf(hooks, ev) {
			if isAgentpaneCmd(e.command) {
				continue
			}
			if !alerterRe.MatchString(e.command) && !alerterSayRe.MatchString(e.command) {
				continue
			}
			p.out("warn", "existing alerting hook on %s: %s", ev, e.command)
			p.out("warn", "agentpane also rings/badges on permission prompts, so expect double alerts;")
			p.out("warn", "see agentpane --no-bell / --no-badge / --no-notify or the bell/badge/notify config keys")
		}
	}

	if data.get("statusLine") != nil {
		p.out("info", "a statusLine is configured; agentpane --oneline can replace or double a status line")
	}

	// ---- info: coexisting hooks on the 12 events ----
	var coexist []hookEntry
	for _, ev := range hookEvents {
		for _, e := range entriesOf(hooks, ev) {
			if !isAgentpaneCmd(e.command) {
				coexist = append(coexist, e)
			}
		}
	}
	if len(coexist) > 0 {
		p.out("info", "coexisting hooks left untouched (hook groups coexist by design; agentpane is observe-only and appended last):")
		for _, e := range coexist {
			p.out("info", "  %s: %s", e.event, e.command)
		}
	}

	prog := quoteIfNeeded(in.binary)
	expected := prog + " hook"
	expectedAuto := prog + " autopane"

	// rebind rewrites only the program path, preserving the verb and any
	// arguments the user added after it. A rebuilt or moved binary is the
	// normal reason this runs, and somebody's extra `--columns 80` must
	// survive it.
	rebind := func(cmd, target string) string {
		_, rest := splitProgram(strings.TrimSpace(cmd))
		return target + strings.TrimRight(rest, " \t")
	}

	type update struct {
		e   hookEntry
		cmd string
	}
	var toAdd []string
	var toUpdate []update
	var toRemove []hookEntry

	for _, ev := range hookEvents {
		var seen []string
		covered := false
		for _, e := range entriesOf(hooks, ev) {
			if !isHookCmd(e.command) {
				continue
			}
			covered = true
			nc := rebind(e.command, quoteIfNeeded(in.binary))
			if slicesContains(seen, nc) {
				// A stale old-path group beside a current one would rebind
				// to an exact duplicate; drop it rather than leave it.
				toRemove = append(toRemove, e)
				continue
			}
			seen = append(seen, nc)
			if nc != e.command {
				toUpdate = append(toUpdate, update{e, nc})
			}
		}
		if !covered {
			toAdd = append(toAdd, ev)
		}
	}

	// The autopane entry is rebound on every run so a rebuilt binary never
	// leaves a stale auto-open behind; --autopane only controls whether a
	// MISSING one is added.
	autoAdd := false
	autoCovered := false
	var autoSeen []string
	for _, e := range entriesOf(hooks, "SessionStart") {
		if !isAutopaneCmd(e.command) {
			continue
		}
		autoCovered = true
		nc := rebind(e.command, quoteIfNeeded(in.binary))
		if slicesContains(autoSeen, nc) {
			toRemove = append(toRemove, e)
			continue
		}
		autoSeen = append(autoSeen, nc)
		if nc != e.command {
			toUpdate = append(toUpdate, update{e, nc})
		}
	}
	if in.autopane && !autoCovered {
		autoAdd = true
	}

	if len(toAdd) == 0 && len(toUpdate) == 0 && len(toRemove) == 0 && !autoAdd {
		if in.autopane {
			p.out("info", "agentpane hooks (all 12 events) and the autopane entry already installed with this binary; nothing to do")
		} else {
			p.out("info", "agentpane hooks already installed for all 12 events with this binary; nothing to do")
		}
		p.status = statusAlready
		return *p, nil
	}

	if !in.exists {
		p.out("plan", "create %s containing only the hooks block", in.settingsPath)
	}

	if !data.get("hooks").isObject() {
		data.set("hooks", hooks)
	}
	dh := data.get("hooks")

	for _, u := range toUpdate {
		dh.get(u.e.event).items[u.e.group].get("hooks").items[u.e.index].set("command", newString(u.cmd))
		p.out("plan", "update agentpane binary path on %s: \"%s\" -> \"%s\"", u.e.event, u.e.command, u.cmd)
	}

	// Removals run after the in-place updates (which rely on the original
	// indices) and in descending order per event so earlier indices stay
	// valid while deleting.
	sort.Slice(toRemove, func(i, j int) bool {
		a, b := toRemove[i], toRemove[j]
		if a.event != b.event {
			return a.event > b.event
		}
		if a.group != b.group {
			return a.group > b.group
		}
		return a.index > b.index
	})
	touched := map[string]bool{}
	for _, e := range toRemove {
		list := dh.get(e.event).items[e.group].get("hooks")
		list.items = append(list.items[:e.index], list.items[e.index+1:]...)
		p.out("plan", "remove duplicate agentpane hook from %s (command: %s)", e.event, e.command)
		touched[e.event] = true
	}
	for _, ev := range sortedKeys(touched) {
		pruneEmptyGroups(dh.get(ev))
	}

	for _, ev := range toAdd {
		arr := dh.get(ev)
		if !arr.isArray() {
			arr = newArray()
			dh.set(ev, arr)
		}
		arr.items = append(arr.items, newGroup("", expected))
		p.out("plan", `append to %s: {"matcher": "", "hooks": [{"type": "command", "command": "%s", "timeout": 5}]}`, ev, expected)
	}

	if autoAdd {
		arr := dh.get("SessionStart")
		if !arr.isArray() {
			arr = newArray()
			dh.set("SessionStart", arr)
		}
		arr.items = append(arr.items, newGroup("startup|resume", expectedAuto))
		p.out("plan", `append to SessionStart: {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "%s", "timeout": 5}]}`, expectedAuto)
	}

	p.proposed = data.render(detectIndent(in.oldText))
	p.status = statusChanges
	return *p, nil
}

func planUninstall(p *plan, data *jvalue, in planInput) (plan, error) {
	if !in.exists {
		p.out("info", "%s does not exist; nothing to uninstall", in.settingsPath)
		p.status = statusNothing
		return *p, nil
	}

	// Plain --uninstall removes every agentpane entry; --uninstall --autopane
	// removes only the auto-open one.
	matches := isAgentpaneCmd
	if in.autopane {
		matches = isAutopaneCmd
	}

	removed := 0
	dh := data.get("hooks")
	if dh.isObject() {
		for _, ev := range append([]string{}, dh.keys...) {
			groups := dh.get(ev)
			if !groups.isArray() {
				continue
			}
			var kept []*jvalue
			changed := false
			for _, g := range groups.items {
				if g.isObject() && g.get("hooks").isArray() {
					hl := g.get("hooks")
					var keptHooks []*jvalue
					for _, h := range hl.items {
						if h.isObject() && h.get("type").str() == "command" && matches(h.get("command").str()) {
							removed++
							changed = true
							p.out("plan", "remove agentpane hook from %s (command: %s)", ev, h.get("command").str())
							continue
						}
						keptHooks = append(keptHooks, h)
					}
					if len(keptHooks) != len(hl.items) {
						if len(keptHooks) == 0 && onlyMatcherAndHooks(g) {
							continue // drop the emptied group entirely
						}
						hl.items = keptHooks
					}
				}
				kept = append(kept, g)
			}
			if changed {
				if len(kept) > 0 {
					groups.items = kept
				} else {
					dh.del(ev)
					p.out("plan", "remove emptied event %s (the hooks key itself is kept)", ev)
				}
			}
		}
	}

	if removed == 0 {
		p.out("info", "no agentpane hook entries found in %s; nothing to do", in.settingsPath)
		p.status = statusNothing
		return *p, nil
	}

	p.proposed = data.render(detectIndent(in.oldText))
	p.status = statusChanges
	return *p, nil
}

// pruneEmptyGroups drops groups this installer emptied, but only the plain
// {"matcher":..,"hooks":[]} shape — a group carrying any other key is
// somebody else's and is left alone even when agentpane emptied its list.
func pruneEmptyGroups(groups *jvalue) {
	if !groups.isArray() {
		return
	}
	var kept []*jvalue
	for _, g := range groups.items {
		if g.isObject() && g.get("hooks").isArray() && len(g.get("hooks").items) == 0 && onlyMatcherAndHooks(g) {
			continue
		}
		kept = append(kept, g)
	}
	groups.items = kept
}

func onlyMatcherAndHooks(g *jvalue) bool {
	for _, k := range g.keys {
		if k != "matcher" && k != "hooks" {
			return false
		}
	}
	return true
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- the recognizer, for callers outside this package -----------------------
//
// `agentpane doctor` has to see exactly the entries the installer manages: a
// doctor that says "not installed" about a working Windows install is worse
// than no doctor at all. These are the same predicates the plan uses, so the
// two cannot drift.

// IsAgentpaneCmd reports whether cmd is any of agentpane's own hook entries.
func IsAgentpaneCmd(cmd string) bool { return isAgentpaneCmd(cmd) }

// IsHookCmd reports whether cmd is a forwarder entry ("agentpane hook").
func IsHookCmd(cmd string) bool { return isHookCmd(cmd) }

// IsAutopaneCmd reports whether cmd is the auto-open entry.
func IsAutopaneCmd(cmd string) bool { return isAutopaneCmd(cmd) }

// CmdProgram returns the agentpane program path a hook entry names, unquoted,
// or "" when cmd is not one of ours. A bare "agentpane hook" returns
// "agentpane": PATH-resolved, with no path to check.
func CmdProgram(cmd string) string {
	if agentpaneVerb(cmd) == "" {
		return ""
	}
	prog, _ := splitProgram(strings.TrimSpace(cmd))
	return prog
}
