package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/source/registry"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/ui"
)

// --- session/run adapters (ui declares its own local row types) ---

// demoSessionRows adapts fake.DemoSessions for the idle screen (§3.8a).
func demoSessionRows() []ui.SessionRow {
	src := fake.DemoSessions()
	rows := make([]ui.SessionRow, 0, len(src))
	for _, s := range src {
		rows = append(rows, ui.SessionRow{
			ID: s.ID, ShortID: s.ShortID, Dir: s.Dir, Live: s.Live,
			Idle: s.Idle, Attached: s.Attached, Agents: s.Agents, EndedAgo: s.EndedAgo,
		})
	}
	return rows
}

// demoRunRows adapts fake.DemoRunHistory for [ ] browsing (§3.8).
func demoRunRows() []ui.RunRow {
	src := fake.DemoRunHistory()
	rows := make([]ui.RunRow, 0, len(src))
	for _, r := range src {
		row := ui.RunRow{
			ID: r.ID, Start: r.Start, End: r.End, Tokens: r.Tokens,
			Outcome: r.Outcome, Stalls: r.Stalls, Contentions: r.Contentions,
		}
		for _, a := range r.Agents {
			row.Agents = append(row.Agents, ui.RunAgentRow{
				Name: a.Name, Duration: a.Duration, Status: a.Status, Tokens: a.Tokens,
			})
		}
		rows = append(rows, row)
	}
	return rows
}

// sessionRows adapts the live registry for the idle screen. The registry only
// lists live sessions and carries no per-session agent count, so Agents stays
// zero — the idle screen renders plain "live" rather than an invented count
// (C8).
func sessionRows(reg *registry.Source, att *attacher) func() []ui.SessionRow {
	return func() []ui.SessionRow {
		cur := att.current()
		src := reg.Sessions()
		rows := make([]ui.SessionRow, 0, len(src))
		for _, s := range src {
			rows = append(rows, ui.SessionRow{
				ID:       s.SessionID,
				ShortID:  shortID(s.SessionID),
				Dir:      tildeDir(s.CWD),
				Live:     true,
				Idle:     s.Status == "idle",
				Attached: s.SessionID == cur && cur != "",
			})
		}
		return rows
	}
}

// storeRunRows adapts the run store for [ ] browsing. runstore.Run carries no
// total, so tokens are summed over agents.
func storeRunRows(store *runstore.Store) func() []ui.RunRow {
	return func() []ui.RunRow {
		runs, _, err := store.List()
		if err != nil {
			return nil
		}
		rows := make([]ui.RunRow, 0, len(runs))
		for _, r := range runs {
			rows = append(rows, runRowFrom(r))
		}
		return rows
	}
}

func runRowFrom(r runstore.Run) ui.RunRow {
	row := ui.RunRow{
		ID: r.ID, Session: r.Session, Start: r.Start, End: r.End,
		Outcome: r.Outcome, Stalls: r.Stalls, Contentions: r.Contentions,
	}
	for _, a := range r.Agents {
		row.Tokens += a.Tokens
		row.Agents = append(row.Agents, ui.RunAgentRow{
			Name: a.Name, Duration: a.Duration, Status: a.Status, Tokens: a.Tokens,
		})
	}
	return row
}

// --- paths ---

// shortID is the 4-char session id shown in headers and session lists
// (§3.8a).
func shortID(id string) string {
	if len(id) > 4 {
		return id[:4]
	}
	return id
}

// tildeDir shortens a home-prefixed path for the session list.
func tildeDir(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || dir == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		return "~" + dir[len(home):]
	}
	return dir
}

// projectDirFor maps a session cwd onto its transcript directory
// ~/.claude/projects/<munged-cwd> (DATA-SOURCES §4.1: non-alphanumerics
// become '-').
func projectDirFor(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", mungeCwd(cwd))
}

// mungeCwd replicates Claude Code's project-directory munge: every rune
// outside [A-Za-z0-9] becomes '-'.
func mungeCwd(cwd string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, cwd)
}

// settingsPath is ~/.claude/settings.json (a symlink on some machines;
// ReadFile follows it — doctor never writes).
func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// hooksInstalled reports whether any hook in ~/.claude/settings.json invokes
// `agentpane hook`. Read-only, tolerant of malformed JSON (C9).
func hooksInstalled() bool {
	return len(installedHookEvents()) > 0
}

// installedHookEvents lists the hook event names carrying one of agentpane's
// own FORWARDER entries — a command that STARTS WITH "agentpane hook" or
// "<path>/agentpane hook" (isAgentpaneHookCmd, install.sh's
// AGENTPANE_HOOK_CMD) — never a foreign command that merely mentions an
// agentpane path, and never the "agentpane autopane" entry (that one does
// not forward events, so it is not hook coverage).
func installedHookEvents() []string {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return nil
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &root) != nil {
		return nil
	}
	var events []string
	for name, groups := range root.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if isAgentpaneHookCmd(h.Command) {
					events = append(events, name)
				}
			}
		}
	}
	return events
}

// --- ui prefs (w / t persistence, §5.1) ---

// uiPrefs persists the w and t toggles under the state dir — never in the
// user's config.toml, which agentpane treats as read-only.
type uiPrefs struct {
	Width    string `json:"width"`
	RightCol string `json:"right_col"`
}

func prefsPath() string {
	dir, err := statefile.DefaultDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ui.json")
}

func loadPrefs() uiPrefs {
	var p uiPrefs
	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return p
	}
	json.Unmarshal(data, &p) //nolint:errcheck // malformed prefs = defaults (C9)
	return p
}

func savePrefs(p uiPrefs) {
	path := prefsPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0o644) //nolint:errcheck // degrade silently (§5.4)
}

// --- debug log (--log) ---

// debugLog appends one line per merged event plus lifecycle notes. A nil
// file (no --log, or the path failed to open) makes every method a no-op.
type debugLog struct {
	mu sync.Mutex
	f  *os.File
}

func newDebugLog(path string) *debugLog {
	l := &debugLog{}
	if path == "" {
		return l
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return l // C9: a bad --log path degrades to no logging
	}
	l.f = f
	l.printf("agentpane %s started", Version)
	return l
}

func (l *debugLog) printf(format string, args ...any) {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func (l *debugLog) event(ev event.Event) {
	if l == nil || l.f == nil {
		return
	}
	l.printf("seq=%d kind=%s agent=%s tool=%q target=%q detail=%q",
		ev.Seq, ev.Kind, ev.AgentID, ev.Tool, ev.Target, ev.Detail)
}

// tee mirrors an event stream into the log (demo path, which bypasses the
// attacher). Without a log file the input channel passes through untouched.
func (l *debugLog) tee(in <-chan event.Event) <-chan event.Event {
	if l == nil || l.f == nil {
		return in
	}
	out := make(chan event.Event, 64)
	go func() {
		defer close(out)
		for ev := range in {
			l.event(ev)
			out <- ev
		}
	}()
	return out
}

func (l *debugLog) Close() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Close()
}
