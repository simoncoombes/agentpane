// Package registry watches the Claude Code live-session registry
// (~/.claude/sessions/<pid>.json, one file per CLI process — DATA-SOURCES §1,
// §2) for zero-setup session discovery and as the confirmer channel for
// blocked-on-user state. Stale PID files persist after exit, so liveness is
// always kill(pid,0); statusUpdatedAt is event-driven, not a heartbeat, and
// is treated as advisory only (DATA-SOURCES §8 Q6).
package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const (
	defaultPollInterval = time.Second
	eventBufferSize     = 64

	// waitingPermission is the registry's blocked-on-user marker
	// (DATA-SOURCES §2 sessions-registry row).
	waitingPermission = "permission prompt"

	waitingDetail = "registry: waiting on permission"
)

// Session is one live row of the registry.
type Session struct {
	PID        int
	SessionID  string
	CWD        string
	Name       string
	Status     string
	WaitingFor string
	UpdatedAt  time.Time // statusUpdatedAt when parseable, else file mtime; advisory
}

// Option configures a Source.
type Option func(*Source)

// WithPollInterval overrides the 1s poll cadence (tests).
func WithPollInterval(d time.Duration) Option {
	return func(s *Source) {
		if d > 0 {
			s.interval = d
		}
	}
}

// WithClock injects the time source used to stamp emitted events (tests).
func WithClock(fn func() time.Time) Option {
	return func(s *Source) {
		if fn != nil {
			s.clock = fn
		}
	}
}

// WithLiveness injects the pid-liveness probe (tests). The default is
// kill(pid,0).
func WithLiveness(fn func(pid int) bool) Option {
	return func(s *Source) {
		if fn != nil {
			s.alive = fn
		}
	}
}

// WithSessionFilter restricts the EVENT stream to one session id. The registry
// is session-agnostic by design — it is the discovery channel, and Sessions /
// AttachTarget keep reporting every live row regardless — but a pane that
// knows which session it belongs to has no use for a sibling's rows, and
// forwarding them puts another session's text one bug away from that pane.
// Filtered here they never enter the process at all.
//
// Transport lifecycle events (SourceConnected/SourceDisconnected) carry no
// session id and are genuinely global, so they always pass. A row with no
// session id cannot be attributed and is dropped under a filter rather than
// guessed at (C8). The empty filter is "emit everything", which is what the
// idle registry-only watch needs.
func WithSessionFilter(sessionID string) Option {
	return func(s *Source) { s.session = sessionID }
}

// DefaultDir returns ~/.claude/sessions.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

type snapshot struct {
	status     string
	waitingFor string
}

// Source polls a sessions directory and implements event.Source. Sessions
// and AttachTarget read the directory on demand, so they work before Events
// is started.
type Source struct {
	dir      string
	interval time.Duration
	clock    func() time.Time
	alive    func(pid int) bool
	session  string // "" = emit every session's rows (see WithSessionFilter)

	mu   sync.Mutex
	last map[int]snapshot
	seq  uint64
	down bool
}

// New returns a Source watching dir (injectable; see DefaultDir).
func New(dir string, opts ...Option) *Source {
	s := &Source{
		dir:      dir,
		interval: defaultPollInterval,
		clock:    time.Now,
		alive:    pidAlive,
		last:     make(map[int]snapshot),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Source) Name() string { return "registry" }

// Sessions returns the live sessions (stale-pid files skipped), newest
// first by UpdatedAt.
func (s *Source) Sessions() []Session {
	rows, _ := s.read()
	return rows
}

// AttachTarget returns the newest live session whose cwd matches
// (SPEC §3.8a attach rule).
func (s *Source) AttachTarget(cwd string) (Session, bool) {
	cwd = filepath.Clean(cwd)
	var best Session
	found := false
	for _, row := range s.Sessions() {
		if filepath.Clean(row.CWD) != cwd {
			continue
		}
		if !found || row.UpdatedAt.After(best.UpdatedAt) ||
			(row.UpdatedAt.Equal(best.UpdatedAt) && row.PID > best.PID) {
			best = row
			found = true
		}
	}
	return best, found
}

func (s *Source) Events(ctx context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event, eventBufferSize)
	go s.loop(ctx, ch)
	return ch, nil
}

func (s *Source) loop(ctx context.Context, ch chan<- event.Event) {
	defer close(ch)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		for _, ev := range s.scan() {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scan diffs the registry against the previous pass and emits taxonomy
// events on status transitions only: idle → SessionIdle, waiting on a
// permission prompt → NotificationRaised (confirmer channel, DATA-SOURCES
// §6). Event.At is the ingest clock — statusUpdatedAt is advisory.
func (s *Source) scan() []event.Event {
	rows, err := s.read()

	s.mu.Lock()
	defer s.mu.Unlock()

	var evs []event.Event
	if err != nil {
		if !s.down {
			s.down = true
			evs = append(evs, s.newEvent(event.SourceDisconnected, "", func(e *event.Event) {
				e.Detail = err.Error()
			}))
		}
		return evs
	}
	if s.down {
		s.down = false
		evs = append(evs, s.newEvent(event.SourceConnected, "", nil))
	}

	seen := make(map[int]bool, len(rows))
	for _, row := range rows {
		seen[row.PID] = true
		cur := snapshot{status: row.Status, waitingFor: row.WaitingFor}
		if prev, ok := s.last[row.PID]; ok && prev == cur {
			continue
		}
		s.last[row.PID] = cur

		// Diff bookkeeping above stays whole-registry (so a filtered row still
		// only ever fires on a transition); emission is what the filter gates.
		if !s.watches(row.SessionID) {
			continue
		}
		switch {
		case cur.status == "idle":
			evs = append(evs, s.newEvent(event.SessionIdle, row.SessionID, nil))
		case cur.status == "waiting" && cur.waitingFor == waitingPermission:
			evs = append(evs, s.newEvent(event.NotificationRaised, row.SessionID, func(e *event.Event) {
				e.Detail = waitingDetail
			}))
		}
	}
	for pid := range s.last {
		if !seen[pid] {
			delete(s.last, pid)
		}
	}
	return evs
}

// watches reports whether a row's session id passes this source's filter.
func (s *Source) watches(sessionID string) bool {
	return s.session == "" || sessionID == s.session
}

func (s *Source) newEvent(kind event.Kind, sessionID string, mut func(*event.Event)) event.Event {
	s.seq++
	ev := event.Event{
		Seq:       s.seq,
		At:        s.clock(),
		SessionID: sessionID,
		AgentID:   event.MainAgentID,
		Kind:      kind,
	}
	if mut != nil {
		mut(&ev)
	}
	return ev
}

// read parses every <pid>.json in the directory, skipping stale pids and
// malformed files (C9). A missing directory is the normal nothing-running
// state, not an error.
func (s *Source) read() ([]Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rows []Session
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		pidStr, ok := trimJSON(name)
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || !s.alive(pid) {
			continue
		}
		row, ok := s.parseFile(filepath.Join(s.dir, name), pid)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
		}
		return rows[i].PID > rows[j].PID
	})
	return rows, nil
}

func trimJSON(name string) (string, bool) {
	const ext = ".json"
	if len(name) <= len(ext) || name[len(name)-len(ext):] != ext {
		return "", false
	}
	return name[:len(name)-len(ext)], true
}

func (s *Source) parseFile(path string, pid int) (Session, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Session{}, false
	}
	var f struct {
		SessionID       string          `json:"sessionId"`
		CWD             string          `json:"cwd"`
		Name            string          `json:"name"`
		Status          string          `json:"status"`
		WaitingFor      string          `json:"waitingFor"`
		StatusUpdatedAt json.RawMessage `json:"statusUpdatedAt"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return Session{}, false
	}
	updated := time.Time{}
	if fi, err := os.Stat(path); err == nil {
		updated = fi.ModTime()
	}
	if t, ok := parseWhen(f.StatusUpdatedAt); ok {
		updated = t
	}
	return Session{
		PID:        pid,
		SessionID:  f.SessionID,
		CWD:        f.CWD,
		Name:       f.Name,
		Status:     f.Status,
		WaitingFor: f.WaitingFor,
		UpdatedAt:  updated,
	}, true
}

// parseWhen accepts the unverified statusUpdatedAt wire shapes: RFC3339
// string, epoch milliseconds, or epoch seconds.
func parseWhen(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		switch {
		case n > 1e12: // epoch ms
			return time.UnixMilli(int64(n)), true
		case n > 1e9: // epoch s
			return time.Unix(int64(n), 0), true
		}
	}
	return time.Time{}, false
}
