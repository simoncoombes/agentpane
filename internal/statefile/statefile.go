// Package statefile implements §3.20a: the running TUI writes a small JSON
// state file on every state change (debounced to at most one write per
// second), and the short-lived `agentpane --oneline` process reads it back
// and renders one status row. The file is the only IPC between the two —
// --oneline never opens the socket and never reads the transcript.
package statefile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileName is the state file's name for a writer with no session id.
const FileName = "current.json"

const (
	filePrefix = "current-"
	fileSuffix = ".json"
)

// FileNameFor is the state file one session's TUI writes: current-<id>.json.
// Panes are pinned to a session (`agentpane --session <id>`), so N panes are
// N distinct writers; a single current.json would make them fight over one
// file and --oneline would flip between unrelated sessions second to second.
func FileNameFor(session string) string {
	if session == "" {
		return FileName
	}
	return filePrefix + sanitizeSession(session) + fileSuffix
}

// isStateFile reports whether name is one of this package's own files. The
// ".current-*.tmp" temporaries are excluded by both fixes.
func isStateFile(name string) bool {
	return name == FileName ||
		(strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix))
}

// sanitizeSession keeps a session id safe as a filename fragment.
func sanitizeSession(session string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, session)
}

// Staleness is how old UpdatedAt may be before the file counts as
// not-running (§3.20a: 15s).
const Staleness = 15 * time.Second

// DefaultInterval is the minimum spacing between writes (§3.20a: ≤1/s).
const DefaultInterval = time.Second

// State is the §3.20a payload:
// {session, updated_at, live, asks, stalls, in_call, settled,
// oldest_ask_slug, oldest_ask_age}. OldestAskAge marshals as Go's default
// time.Duration integer nanoseconds; only agentpane reads the file back.
type State struct {
	Session       string        `json:"session"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Live          int           `json:"live"`
	Asks          int           `json:"asks"`
	Stalls        int           `json:"stalls"`
	InCall        int           `json:"in_call"`
	Settled       int           `json:"settled"`
	OldestAskSlug string        `json:"oldest_ask_slug"`
	OldestAskAge  time.Duration `json:"oldest_ask_age"`
}

// DefaultDir returns ~/.local/state/agentpane (honouring $XDG_STATE_HOME),
// the directory §3.20a names for current.json.
func DefaultDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "agentpane"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agentpane"), nil
}

// Writer debounces State updates into atomic writes of
// <Dir>/current-<Session>.json. The zero value plus a Dir is ready to use.
// Update never blocks a render and never returns an error: a failed write
// leaves the previous file in place and --oneline degrades to staleness
// (§3.20a).
type Writer struct {
	Dir string

	// Session keys the file this writer owns, so concurrent panes never
	// overwrite each other. Set it before the first Update; use SetSession to
	// change it on a live writer (the trailing timer runs on its own
	// goroutine and reads it under the lock).
	Session string

	// Interval overrides DefaultInterval; ≤0 means DefaultInterval.
	Interval time.Duration
	// Now overrides time.Now, for deterministic tests.
	Now func() time.Time
	// AfterFunc overrides time.AfterFunc, for deterministic tests.
	AfterFunc func(d time.Duration, f func()) *time.Timer

	mu        sync.Mutex
	lastWrite time.Time
	pending   *State
	timer     *time.Timer
	swept     bool
}

// SetSession re-keys the file this writer owns (an unpinned pane switching
// sessions, §3.8a). The previous session's file is left alone to go stale on
// its own: 15 seconds later it reads as "no agentpane running", which by then
// is the truth.
func (w *Writer) SetSession(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Session = session
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return DefaultInterval
}

// Update records s as the latest state. If at least one interval has passed
// since the last write it is written immediately; otherwise it is kept as
// pending and flushed by a trailing timer, so the newest state always lands
// on disk within one interval. UpdatedAt is stamped at write time.
func (w *Writer) Update(s State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if now.Sub(w.lastWrite) >= w.interval() {
		w.writeLocked(s, now)
		return
	}
	w.pending = &s
	if w.timer == nil {
		after := w.AfterFunc
		if after == nil {
			after = time.AfterFunc
		}
		w.timer = after(w.interval()-now.Sub(w.lastWrite), w.flushPending)
	}
}

// Flush writes any pending state immediately. Call on shutdown so the last
// state (typically all-settled) is not lost to the debounce.
func (w *Writer) Flush() {
	w.flushPending()
}

func (w *Writer) flushPending() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == nil {
		return
	}
	s := *w.pending
	w.writeLocked(s, w.now())
}

// writeLocked performs the atomic tmp+rename write. Errors are swallowed:
// nothing here may break a render (§5.4).
func (w *Writer) writeLocked(s State, at time.Time) {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.pending = nil
	w.lastWrite = at
	s.UpdatedAt = at

	if w.Dir == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return
	}
	if !w.swept {
		w.swept = true
		w.sweepAbandoned(at)
	}
	tmp, err := os.CreateTemp(w.Dir, ".current-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name) //nolint:errcheck
		return
	}
	if err := os.Rename(name, filepath.Join(w.Dir, FileNameFor(w.Session))); err != nil {
		os.Remove(name) //nolint:errcheck
	}
}

// AbandonedAfter is how old a state file must be before a starting writer
// removes it. Per-session keying means one file per session id ever pinned,
// so without a sweep the state directory would grow without bound. The
// threshold is far beyond Staleness: anything this old already reads as
// "no agentpane running", and a pane that is somehow still alive simply
// rewrites its file on the next state change.
const AbandonedAfter = time.Hour

// sweepAbandoned removes long-dead state files. Called once per Writer, on
// its first write, with w.mu held. Every error is ignored (§5.4).
func (w *Writer) sweepAbandoned(now time.Time) {
	mine := FileNameFor(w.Session)
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == mine || !isStateFile(e.Name()) {
			continue
		}
		path := filepath.Join(w.Dir, e.Name())
		s, ok := readState(path)
		if !ok {
			continue // unparseable: not this writer's to judge
		}
		if now.Sub(s.UpdatedAt) < AbandonedAfter {
			continue
		}
		os.Remove(path) //nolint:errcheck // hygiene only, never load-bearing
	}
}

// Read loads <dir>/current.json — the unkeyed file. ok is false when the file
// is missing, unparseable, or its UpdatedAt is more than Staleness older than
// now — all of which mean "no agentpane running" (§3.20a). It never panics on
// malformed input (C9); exit-0 semantics belong to the caller.
func Read(dir string, now time.Time) (State, bool) {
	return ReadSession(dir, "", now)
}

// ReadSession loads exactly one session's state file, with Read's semantics.
// This is `--oneline --session <id>`: an explicit, unambiguous answer about
// one pane, with no fallback to whichever other pane happens to be freshest.
func ReadSession(dir, session string, now time.Time) (State, bool) {
	s, ok := readState(filepath.Join(dir, FileNameFor(session)))
	if !ok || s.UpdatedAt.IsZero() || now.Sub(s.UpdatedAt) > Staleness {
		return State{}, false
	}
	return s, true
}

// ReadLatest scans dir for every session's state file and returns the
// freshest fresh one, plus how many are fresh at all. live > 1 means several
// panes are running and the row must name its session for the status bar to
// mean anything (RenderOneline does). Nothing fresh yields ok false and
// live 0: "no agentpane running".
func ReadLatest(dir string, now time.Time) (state State, ok bool, live int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return State{}, false, 0
	}
	for _, e := range entries {
		if e.IsDir() || !isStateFile(e.Name()) {
			continue
		}
		s, fresh := readState(filepath.Join(dir, e.Name()))
		if !fresh || s.UpdatedAt.IsZero() || now.Sub(s.UpdatedAt) > Staleness {
			continue
		}
		live++
		if !ok || s.UpdatedAt.After(state.UpdatedAt) {
			state, ok = s, true
		}
	}
	return state, ok, live
}

// readState parses one state file without judging its freshness.
func readState(path string) (State, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, false
	}
	return s, true
}

// RenderOneline renders the §3.20 level-2 status row, e.g.
//
//	⚑1 ●4 ◐1 ✔2 4m02s fix-ts2345
//
// Zero-valued optional segments are omitted: ⚑ appears only with a live ask
// (and carries the oldest ask's age and slug), ○ only with stalls, ◐ only
// with open calls. ● and ✔ always render so an idle row still reads as a
// status. When ok is false it returns "no agentpane running"; the exit-0
// contract is the caller's (§3.20a).
//
// live is how many panes are running (ReadLatest's count). With more than one
// the row is prefixed with the session's short id, because "●4" alone would
// silently attribute one session's agents to whichever session the reader had
// in mind.
func RenderOneline(s State, ok bool, live int) string {
	if !ok {
		return "no agentpane running"
	}
	var parts []string
	if live > 1 && s.Session != "" {
		parts = append(parts, s.Session)
	}
	if s.Asks > 0 {
		parts = append(parts, fmt.Sprintf("⚑%d", s.Asks))
	}
	parts = append(parts, fmt.Sprintf("●%d", s.Live))
	if s.Stalls > 0 {
		parts = append(parts, fmt.Sprintf("○%d", s.Stalls))
	}
	if s.InCall > 0 {
		parts = append(parts, fmt.Sprintf("◐%d", s.InCall))
	}
	parts = append(parts, fmt.Sprintf("✔%d", s.Settled))
	if s.Asks > 0 && s.OldestAskSlug != "" {
		parts = append(parts, formatAge(s.OldestAskAge), s.OldestAskSlug)
	}
	return strings.Join(parts, " ")
}

// formatAge renders a wait duration in the §3.20 style: "42s", "4m02s",
// "1h04m". Negative durations clamp to zero.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
}
