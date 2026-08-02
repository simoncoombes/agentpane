// Package transcript implements the TranscriptSource of SPEC.md §1.4: it
// tails a Claude Code session's transcript tree under
// ~/.claude/projects/<munged-cwd>/ — the main <session>.jsonl plus every
// <session>/subagents/**/agent-*.jsonl and its .meta.json sidecar — and maps
// lines onto the frozen event taxonomy per docs/DATA-SOURCES.md §4–§6.
//
// Ordering is by file position, never by timestamp (DATA-SOURCES §4.6); a
// torn final line is buffered by simply not advancing the offset past it, so
// it is re-read whole on a later poll. Offsets() exposes consumed positions
// so a restarted process can resume without re-emitting history.
package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const (
	defaultPollInterval = 500 * time.Millisecond
	eventBufferSize     = 256
)

// Option configures a Source.
type Option func(*Source)

// WithPollInterval overrides the ~500ms poll cadence (tests).
func WithPollInterval(d time.Duration) Option {
	return func(s *Source) {
		if d > 0 {
			s.interval = d
		}
	}
}

// WithClock injects the time source used when a line carries no parseable
// timestamp (tests; logic paths never call time.Now directly).
func WithClock(fn func() time.Time) Option {
	return func(s *Source) {
		if fn != nil {
			s.clock = fn
		}
	}
}

// WithOffsets seeds prior consumed offsets (as returned by Offsets) so a
// restart does not re-emit history (SPEC §1.4). A .jsonl path resumes at the
// stored byte offset; a .meta.json path with a positive offset is treated as
// already announced (parsed for enrichment only, no AgentSpawned).
func WithOffsets(offsets map[string]int64) Option {
	return func(s *Source) {
		for k, v := range offsets {
			s.prior[k] = v
		}
	}
}

type agentInfo struct {
	name string
	typ  string
}

// pendingTool is a tool_use awaiting its tool_result, keyed by tool_use id.
type pendingTool struct {
	tool    string
	target  string
	agentID string
	// Agent-tool spawns carry the child's identity for the eventual
	// toolUseResult.agentId join (DATA-SOURCES §4.3).
	spawnDesc string
	spawnType string
}

type fileState struct {
	path    string
	agentID string
	offset  int64
	// resumed: reading began at a prior nonzero offset, so the running
	// token sum is not a true cumulative count — emit per-message deltas
	// with TokensCum=false rather than inventing a total (C8).
	resumed bool
	tokens  int
	started bool // AgentStarted emitted (always true for main)
}

// Source tails one session's transcript tree and implements event.Source.
type Source struct {
	projectDir string
	sessionID  string
	interval   time.Duration
	clock      func() time.Time

	mu       sync.Mutex
	prior    map[string]int64
	files    map[string]*fileState
	metaSeen map[string]bool
	info     map[string]agentInfo
	pending  map[string]pendingTool
	spawned  map[string]bool
	seq      uint64
	skipped  uint64
	down     bool
	everUp   bool
}

// New returns a Source tailing projectDir/<sessionID>.jsonl and
// projectDir/<sessionID>/subagents/**.
func New(projectDir, sessionID string, opts ...Option) *Source {
	s := &Source{
		projectDir: projectDir,
		sessionID:  sessionID,
		interval:   defaultPollInterval,
		clock:      time.Now,
		prior:      make(map[string]int64),
		files:      make(map[string]*fileState),
		metaSeen:   make(map[string]bool),
		info:       make(map[string]agentInfo),
		pending:    make(map[string]pendingTool),
		spawned:    make(map[string]bool),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Source) Name() string { return "transcript" }

// Skipped reports how many malformed lines have been dropped so far (C9).
func (s *Source) Skipped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// Offsets returns the consumed byte offset per watched file (torn tails
// excluded) plus processed-marker sizes for .meta.json sidecars. Feed the
// map back through WithOffsets to resume without re-emitting history.
func (s *Source) Offsets() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.files)+len(s.metaSeen))
	for p, st := range s.files {
		out[p] = st.offset
	}
	for p := range s.metaSeen {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.Size()
		} else {
			out[p] = 1
		}
	}
	return out
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

// scan runs one poll pass: discover files, consume complete new lines in
// file-position order, and return the mapped events. Transport-state events
// (SourceConnected / SourceDisconnected) are appended on state change.
func (s *Source) scan() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	var evs []event.Event
	var firstErr error

	metas, agents := s.discover()
	for _, mp := range metas {
		evs = append(evs, s.processMeta(mp)...)
	}

	mainPath := filepath.Join(s.projectDir, s.sessionID+".jsonl")
	more, err := s.readFile(mainPath, event.MainAgentID)
	evs = append(evs, more...)
	if err != nil {
		firstErr = err
	}
	for _, ap := range agents {
		more, err := s.readFile(ap, agentIDFromPath(ap))
		evs = append(evs, more...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// A file poller's transport is the filesystem: missing files are the
	// normal not-started-yet state, so only real read errors count as down.
	switch {
	case firstErr != nil:
		if !s.down {
			s.down = true
			evs = append(evs, s.newEvent(event.SourceDisconnected, s.clock(), event.MainAgentID, "", nil, func(e *event.Event) {
				e.Detail = firstErr.Error()
			}))
		}
	case s.down || !s.everUp:
		s.down = false
		s.everUp = true
		evs = append(evs, s.newEvent(event.SourceConnected, s.clock(), event.MainAgentID, "", nil, nil))
	}
	return evs
}

// discover returns the sidecar and agent-jsonl paths currently on disk,
// sorted, covering both plain subagents and workflow agents
// (DATA-SOURCES §4.1 layout).
func (s *Source) discover() (metas, agents []string) {
	sub := filepath.Join(s.projectDir, s.sessionID, "subagents")
	for _, pat := range []string{
		filepath.Join(sub, "agent-*.meta.json"),
		filepath.Join(sub, "workflows", "*", "agent-*.meta.json"),
	} {
		if m, err := filepath.Glob(pat); err == nil {
			metas = append(metas, m...)
		}
	}
	for _, pat := range []string{
		filepath.Join(sub, "agent-*.jsonl"),
		filepath.Join(sub, "workflows", "*", "agent-*.jsonl"),
	} {
		if m, err := filepath.Glob(pat); err == nil {
			agents = append(agents, m...)
		}
	}
	sort.Strings(metas)
	sort.Strings(agents)
	return metas, agents
}

func agentIDFromPath(p string) string {
	base := filepath.Base(p)
	base = strings.TrimSuffix(base, ".meta.json")
	base = strings.TrimSuffix(base, ".jsonl")
	return strings.TrimPrefix(base, "agent-")
}

// processMeta parses an agent-<id>.meta.json sidecar once: it is the
// agentId↔toolUseId join and the display-name source (DATA-SOURCES §4.3).
// Workflow-agent sidecars lack a description — the name stays empty rather
// than being invented (C8).
func (s *Source) processMeta(path string) []event.Event {
	if s.metaSeen[path] {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // transient; retry next pass
	}
	s.metaSeen[path] = true

	var meta struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
		ToolUseID   string `json:"toolUseId"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		s.skipped++
		return nil
	}
	agentID := agentIDFromPath(path)
	if agentID == "" {
		s.skipped++
		return nil
	}
	if _, ok := s.info[agentID]; !ok {
		s.info[agentID] = agentInfo{name: meta.Description, typ: meta.AgentType}
	}
	if s.prior[path] > 0 || s.spawned[agentID] {
		s.spawned[agentID] = true
		return nil
	}
	s.spawned[agentID] = true
	at := s.clock()
	if fi, err := os.Stat(path); err == nil {
		at = fi.ModTime()
	}
	return []event.Event{s.newEvent(event.AgentSpawned, at, agentID, s.sessionID, raw, nil)}
}

// readFile consumes complete new lines from path. The offset only advances
// past lines terminated by '\n'; a torn tail is re-read next pass
// (DATA-SOURCES §4.6).
func (s *Source) readFile(path, agentID string) ([]event.Event, error) {
	st, ok := s.files[path]
	if !ok {
		st = &fileState{path: path, agentID: agentID, started: agentID == event.MainAgentID}
		if off, prior := s.prior[path]; prior && off > 0 {
			st.offset = off
			st.resumed = true
			st.started = true
		}
		s.files[path] = st
	}

	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if fi.Size() < st.offset {
		// Truncated or replaced: resync from the top rather than guess.
		st.offset = 0
		st.tokens = 0
		st.resumed = false
	}
	if fi.Size() == st.offset {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var evs []event.Event
	pos := 0
	for {
		nl := bytes.IndexByte(data[pos:], '\n')
		if nl < 0 {
			break // torn tail: leave for next pass
		}
		line := data[pos : pos+nl]
		pos += nl + 1
		st.offset += int64(nl + 1)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if agentID != event.MainAgentID && !st.started {
			st.started = true
			evs = append(evs, s.newEvent(event.AgentStarted, s.lineTime(line), agentID, s.sessionID, line, nil))
			s.spawned[agentID] = true
		}
		evs = append(evs, s.lineEvents(st, line)...)
	}
	return evs, nil
}
