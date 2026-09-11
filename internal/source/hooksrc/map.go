package hooksrc

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// Field names below are EXACTLY as captured in docs/DATA-SOURCES.md §3.3:
// 'prompt' not 'user_prompt', 'tool_response' not 'tool_output'. Where docs
// prose and captures conflict, captures win.
type hookPayload struct {
	SessionID           string          `json:"session_id"`
	HookEventName       string          `json:"hook_event_name"`
	AgentID             string          `json:"agent_id"`
	AgentType           string          `json:"agent_type"`
	AgentName           string          `json:"agent_name"` // optional on SubagentStart
	Prompt              string          `json:"prompt"`
	ToolName            string          `json:"tool_name"`
	ToolInput           json.RawMessage `json:"tool_input"`
	ToolResponse        json.RawMessage `json:"tool_response"`
	DurationMS          *int64          `json:"duration_ms"`
	LastAssistantMsg    string          `json:"last_assistant_message"`
	AgentTranscriptPath string          `json:"agent_transcript_path"`
	TeammateName        string          `json:"teammate_name"`
	NotificationType    string          `json:"notification_type"`
	ToolUseID           string          `json:"tool_use_id"`
	Message             string          `json:"message"`
	Suggestions         []suggestion    `json:"permission_suggestions"`
	Source              string          `json:"source"`
	Reason              string          `json:"reason"`
	Trigger             string          `json:"trigger"`
}

type suggestion struct {
	Type string `json:"type"`
}

type toolInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	Pattern      string `json:"pattern"`
	URL          string `json:"url"`
	Path         string `json:"path"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

var (
	errBadJSON        = errors.New("hooksrc: undecodable payload")
	errUnattributable = errors.New("hooksrc: payload missing session_id or hook_event_name")
)

const (
	// maxPendingSpawns bounds the per-session unclaimed-spawn queue. An Agent
	// call that is denied — or whose child never corroborates — leaves an
	// entry behind for good, so the queue is capped and the oldest entries
	// fall off rather than growing without bound in a long session.
	maxPendingSpawns = 64
	// maxPendingStarts bounds the per-session set of ids that have started but
	// not yet corroborated. That set is exactly the phantom set — Claude Code
	// announced ~40 of them in one 5-subagent session — so it is capped too.
	maxPendingStarts = 256
	// maxPendingSessions bounds how many sessions one mapper tracks. A Source
	// serves exactly one session's socket, so this only guards against a
	// confused or hostile forwarder (C9).
	maxPendingSessions = 16
	// correlationTTL is how long an unclaimed spawn, or a start still waiting
	// to corroborate, stays eligible. Live captures put a real child's first
	// tool call within seconds of its SubagentStart; an entry this old is a
	// denied Agent call, a phantom, or an agent the transcript will name
	// exactly. An expired entry is DROPPED, never handed to whoever happens to
	// corroborate next.
	correlationTTL = 5 * time.Minute
)

// pendingSpawn is one Agent-tool call the parent has issued that no agent has
// yet been corroborated as being.
type pendingSpawn struct {
	desc string
	typ  string
	// toolUseID is the spawning call's tool_use_id. SubagentStart does NOT
	// carry it (DATA-SOURCES §3.3), so it cannot key a strict join from hooks
	// alone; it is kept because the transcript sidecar's `toolUseId` can, and
	// because the debug ring shows it.
	toolUseID string
	// seq orders the spawns of a session; at is when it was remembered.
	seq uint64
	at  time.Time
}

// startState is one SubagentStart still waiting for the agent to prove it is
// real. watermark is the session's spawn count at the moment it started: a
// spawn issued AFTER an agent started cannot be the call that started it, so
// only spawns at or below the watermark are ever candidates for it.
type startState struct {
	typ       string
	watermark uint64
	at        time.Time
}

// sessionState is one session's correlation state.
type sessionState struct {
	spawnSeq uint64
	pending  []pendingSpawn
	starts   map[string]*startState
	// claimed is a tombstone set: agent ids whose one claim has already been
	// resolved. corroborate() removes the id from starts, so without this a
	// second SubagentStart for the same id would look new, re-register with a
	// fresh watermark, and claim a SECOND description — one belonging to a
	// later, unrelated agent. Duplicate starts are real: hook connections are
	// served concurrently, and a hook configured in both global and project
	// settings fires twice.
	claimed map[string]bool
}

// Mapper maps forwarded hook payloads onto the frozen event taxonomy. It is
// stateful — the spawn→start correlation below has to remember spawns whose
// child has not corroborated yet — and safe for concurrent use, because hook
// connections are served in parallel.
//
// # Spawn→start correlation: why it exists, and when it declines
//
// The parent's PreToolUse for tool_name=="Agent" carries `description` and
// `subagent_type` but NO `agent_id`: the call belongs to the parent. An
// AgentSpawned built from it is therefore keyed to "main" — it names nobody,
// and it puts a phantom spawn on main's row. The child's identity arrives
// later, on SubagentStart, which carries `agent_id` and `agent_type` but no
// description and no `tool_use_id` (DATA-SOURCES §3.3), so a strict id join is
// impossible from hooks alone.
//
// # The claim rule
//
// A bare SubagentStart NEVER claims. Claude Code announces subagents that
// never do anything: session 6dbbfaa6 emitted SubagentStart/Stop for ~40
// entities that ran no tool, spent no tokens and wrote no transcript,
// interleaved in time with 5 real Agent calls. A rule that let the starter
// claim on arrival therefore handed a real child's description to whichever
// phantom happened to start first — the pane then named the phantom and left
// the agent doing the work unnamed.
//
// So the claim is DEFERRED until the starting id corroborates that it is real:
//
//	(1) its own first tool call or permission ask (a hook payload carrying
//	    that agent_id: PreToolUse, PostToolUse, PermissionRequest), or
//	(2) a SubagentStop carrying an agent_transcript_path — a transcript
//	    sidecar exists for that id, which phantoms never have.
//
// When an id corroborates, exactly one claim is attempted for it, against the
// spawns still unclaimed at that moment:
//
//	(a) a start whose `agent_type` is EMPTY never claims — an empty type says
//	    nothing about which call this is, and the observed phantoms carry
//	    exactly that;
//	(b) candidates are the unclaimed spawns issued at or before the start's
//	    watermark whose `subagent_type` is compatible with the start's
//	    `agent_type` (an empty type on the spawn cannot exclude, two different
//	    non-empty types do);
//	(c) exactly one candidate: it is claimed and removed;
//	(d) several candidates that all carry the same description: any choice
//	    yields the same string, so the oldest is claimed and the rest stay;
//	(e) several candidates that differ: genuinely ambiguous — one of them IS
//	    this agent and nothing says which. The whole candidate set is burned
//	    so no LATER start can inherit a crossed name either, and this agent
//	    gets no name;
//	(f) no candidates: no name.
//
// A spawn or a start that goes correlationTTL without resolving is dropped,
// not reassigned, and both sets are capped (C9).
//
// No name beats a wrong name (C8). An agent left unnamed keeps its
// `agent_type`, which the UI renders as an honest placeholder, and the
// transcript source independently supplies the correct id+description pair as
// soon as the sidecar lands (its `toolUseId` join is exact) — which is why
// deferring costs nothing whenever a transcript exists.
type Mapper struct {
	mu sync.Mutex
	// clock stamps correlation entries for expiry. Injectable for tests.
	clock func() time.Time
	sess  map[string]*sessionState
}

// NewMapper returns a Mapper with empty correlation state.
func NewMapper() *Mapper {
	return &Mapper{clock: time.Now, sess: make(map[string]*sessionState)}
}

// session returns a session's correlation state, creating it on demand. The
// caller holds m.mu.
func (m *Mapper) session(id string) *sessionState {
	if s := m.sess[id]; s != nil {
		return s
	}
	if len(m.sess) >= maxPendingSessions {
		m.sess = make(map[string]*sessionState)
	}
	s := &sessionState{starts: make(map[string]*startState)}
	m.sess[id] = s
	return s
}

// remember records a spawn the parent issued, awaiting a child that
// corroborates being it.
func (m *Mapper) remember(session string, sp pendingSpawn) {
	if sp.desc == "" {
		// Nothing to claim: a description-less Agent call can never name
		// anybody, and keeping it would only manufacture ambiguity.
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	s := m.session(session)
	s.expire(now)
	s.spawnSeq++
	sp.seq = s.spawnSeq
	sp.at = now
	s.pending = append(s.pending, sp)
	if len(s.pending) > maxPendingSpawns {
		s.pending = append([]pendingSpawn(nil), s.pending[len(s.pending)-maxPendingSpawns:]...)
	}
}

// startSeen records a SubagentStart whose agent has not corroborated yet. It
// claims nothing — that is the whole point (see Mapper).
func (m *Mapper) startSeen(session, agentID, agentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	s := m.session(session)
	s.expire(now)
	if _, dup := s.starts[agentID]; dup {
		return // an id starts once; a repeat carries no new information
	}
	if s.claimed[agentID] {
		return // its one claim is already spent (see sessionState.claimed)
	}
	s.starts[agentID] = &startState{typ: agentType, watermark: s.spawnSeq, at: now}
	s.trimStarts()
}

// corroborate resolves the deferred claim for an agent that has just proved it
// is real. It fires at most once per agent id and returns "" whenever the
// answer is not certain.
func (m *Mapper) corroborate(session, agentID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sess[session]
	if s == nil {
		return ""
	}
	s.expire(m.clock())
	st := s.starts[agentID]
	if st == nil {
		// Either it never started under this mapper's watch, or it has already
		// had its one claim. Nothing may be claimed twice.
		return ""
	}
	delete(s.starts, agentID)
	if s.claimed == nil {
		s.claimed = map[string]bool{}
	}
	s.claimed[agentID] = true
	return s.claim(st)
}

// claim applies rules (a)–(f) documented on Mapper.
func (s *sessionState) claim(st *startState) string {
	if st.typ == "" {
		return ""
	}
	cand := make([]int, 0, len(s.pending))
	for i, sp := range s.pending {
		if sp.seq > st.watermark {
			continue // issued after this agent started: not the call that made it
		}
		if sp.typ != "" && sp.typ != st.typ {
			continue
		}
		cand = append(cand, i)
	}
	if len(cand) == 0 {
		return ""
	}
	if len(cand) > 1 && !sameDesc(s.pending, cand) {
		// Ambiguous. Burn the whole candidate set: this agent IS one of them,
		// and leaving the rest would let a later start pick up a name that
		// belongs to this one.
		s.pending = removeAt(s.pending, cand)
		return ""
	}
	desc := s.pending[cand[0]].desc
	s.pending = removeAt(s.pending, cand[:1])
	return desc
}

// expire drops correlation entries older than correlationTTL. An expired spawn
// is discarded rather than left to be mis-assigned, and an expired start can
// never claim afterwards.
func (s *sessionState) expire(now time.Time) {
	if len(s.pending) > 0 {
		keep := s.pending[:0]
		for _, sp := range s.pending {
			if now.Sub(sp.at) < correlationTTL {
				keep = append(keep, sp)
			}
		}
		s.pending = keep
	}
	for id, st := range s.starts {
		if now.Sub(st.at) >= correlationTTL {
			delete(s.starts, id)
		}
	}
}

// trimStarts enforces maxPendingStarts, dropping the oldest waiting starts.
func (s *sessionState) trimStarts() {
	for len(s.starts) > maxPendingStarts {
		oldest, oldestAt := "", time.Time{}
		for id, st := range s.starts {
			if oldest == "" || st.at.Before(oldestAt) {
				oldest, oldestAt = id, st.at
			}
		}
		delete(s.starts, oldest)
	}
}

// corroborates reports whether a payload is evidence that the agent it carries
// is a real one, rather than an id the platform merely announced: its own tool
// call, its own permission ask, or a stop naming a transcript sidecar.
func corroborates(p hookPayload) bool {
	switch p.HookEventName {
	case "PreToolUse", "PostToolUse", "PermissionRequest":
		return true
	case "SubagentStop":
		return p.AgentTranscriptPath != ""
	}
	return false
}

// sameDesc reports whether every candidate carries the same description, in
// which case claiming one of them cannot produce a wrong name.
func sameDesc(q []pendingSpawn, cand []int) bool {
	for _, i := range cand[1:] {
		if q[i].desc != q[cand[0]].desc {
			return false
		}
	}
	return true
}

// removeAt returns q without the (ascending) indices in drop.
func removeAt(q []pendingSpawn, drop []int) []pendingSpawn {
	if len(drop) == 0 {
		return q
	}
	out := q[:0]
	next := 0
	for i, sp := range q {
		if next < len(drop) && drop[next] == i {
			next++
			continue
		}
		out = append(out, sp)
	}
	return out
}

// Map maps one forwarded hook payload to zero or more events per
// DATA-SOURCES §6. Seq and At are assigned by the emitter. Unknown but
// well-formed hook events (Stop, TaskCompleted, ...) map to nothing without
// error. Raw always carries the original line for the debug ring.
func (m *Mapper) Map(line []byte) ([]event.Event, error) {
	var p hookPayload
	if err := json.Unmarshal(line, &p); err != nil {
		return nil, errBadJSON
	}
	if p.SessionID == "" || p.HookEventName == "" {
		return nil, errUnattributable
	}

	raw := json.RawMessage(append([]byte(nil), line...))
	base := event.Event{
		SessionID: p.SessionID,
		AgentID:   event.MainAgentID,
		Raw:       raw,
	}
	// Presence of agent_id is the subagent discriminator (DATA-SOURCES
	// §3.3 PreToolUse); its value keys the tree.
	if p.AgentID != "" {
		base.AgentID = p.AgentID
		base.AgentType = p.AgentType
	}

	var in toolInput
	if len(p.ToolInput) > 0 {
		// Best-effort: a non-object tool_input just leaves fields empty.
		json.Unmarshal(p.ToolInput, &in)
	}

	// The deferred claim resolves here, before the payload's own events, so a
	// row is named by the same batch that first shows it working.
	evs := m.correlate(p, base)
	return append(evs, m.mapPayload(p, base, in)...), nil
}

// correlate drives the spawn→start correlation state for one payload and
// returns the naming event a corroborated claim produced, if any (see Mapper).
func (m *Mapper) correlate(p hookPayload, base event.Event) []event.Event {
	if p.AgentID == "" {
		return nil
	}
	if p.HookEventName == "SubagentStart" {
		if p.AgentName == "" {
			// Remembered, not claimed: this id has proved nothing yet.
			m.startSeen(p.SessionID, p.AgentID, p.AgentType)
		}
		return nil
	}
	if !corroborates(p) {
		return nil
	}
	desc := m.corroborate(p.SessionID, p.AgentID)
	if desc == "" {
		return nil
	}
	// The claim IS a spawn record now: a parent Agent call with this
	// description, an id that started after it, and evidence that id is real.
	ev := base
	ev.Kind = event.AgentSpawned
	ev.AgentName = desc
	return []event.Event{ev}
}

// mapPayload is the payload→event switch itself, free of correlation state
// except for remembering the parent's Agent calls.
func (m *Mapper) mapPayload(p hookPayload, base event.Event, in toolInput) []event.Event {
	switch p.HookEventName {
	case "SessionStart":
		ev := base
		ev.Kind = event.SessionStart
		ev.Detail = p.Source
		return []event.Event{ev}

	case "SessionEnd":
		ev := base
		ev.Kind = event.SessionEnd
		ev.Detail = p.Reason
		return []event.Event{ev}

	case "UserPromptSubmit":
		ev := base
		ev.Kind = event.UserPromptSubmitted
		ev.Detail = p.Prompt
		return []event.Event{ev}

	case "PreToolUse":
		start := base
		start.Kind = event.ToolStart
		start.Tool = p.ToolName
		start.Target = targetFrom(in)
		start.Says = in.Description
		evs := []event.Event{start}

		if p.ToolName == "Agent" && p.AgentID == "" {
			// The spawning call belongs to the PARENT — no agent_id, so
			// nothing in this payload identifies the child. Remember the
			// description for the SubagentStart that follows instead of
			// emitting an AgentSpawned keyed to main: that event named nobody
			// (the child never saw it) and credited main with a spawn it
			// cannot be attributed to. Depth is always 1 (SPEC §2.1), so a
			// payload that DOES carry agent_id is not a parent spawn and is
			// left as the plain ToolStart above.
			m.remember(p.SessionID, pendingSpawn{
				desc:      in.Description,
				typ:       in.SubagentType,
				toolUseID: p.ToolUseID,
			})
		}
		if p.ToolName == "Read" {
			read := base
			read.Kind = event.FileRead
			read.Tool = p.ToolName
			read.Target = in.FilePath
			evs = append(evs, read)
		}
		return evs

	case "PostToolUse":
		end := base
		end.Kind = event.ToolEnd
		end.Tool = p.ToolName
		end.Target = targetFrom(in)
		end.Says = in.Description
		if p.DurationMS != nil {
			end.Duration = time.Duration(*p.DurationMS) * time.Millisecond
		}
		evs := []event.Event{end}

		switch p.ToolName {
		case "Edit", "Write", "MultiEdit":
			edited := base
			edited.Kind = event.FileEdited
			edited.Tool = p.ToolName
			edited.Target = in.FilePath
			edited.LinesAdd, edited.LinesDel = patchLines(p.ToolResponse)
			evs = append(evs, edited)
		}
		return evs

	case "SubagentStart":
		// The only payload carrying the child's id — and, on its own, no
		// evidence that the id belongs to anything real. It never claims a
		// description; correlate defers that until the id corroborates.
		ev := base
		ev.Kind = event.AgentStarted
		ev.AgentName = p.AgentName // set only for named agents
		return []event.Event{ev}

	case "SubagentStop":
		// agent_transcript_path rides along in Raw.
		ev := base
		ev.Kind = event.AgentReturned
		ev.Detail = p.LastAssistantMsg
		return []event.Event{ev}

	case "Notification":
		// Delayed, unattributable confirmer (no tool_name/agent_id even
		// for subagent prompts) — never the primary blocked signal.
		ev := base
		ev.Kind = event.NotificationRaised
		ev.Detail = p.Message
		ev.Target = p.NotificationType
		return []event.Event{ev}

	case "PermissionRequest":
		ev := base
		ev.Kind = event.PermissionRequested
		ev.Tool = p.ToolName
		ev.Target = targetFrom(in)
		ev.Ask = &event.AskPayload{
			Kind:    askKind(p.ToolName),
			Command: askCommand(in),
			Options: suggestionTypes(p.Suggestions),
		}
		return []event.Event{ev}

	case "TeammateIdle":
		// The only hook signal teammates emit (DATA-SOURCES §3.3). A name
		// is required for a stable identity; without one, inventing an id
		// would violate C8 — drop instead.
		name := p.TeammateName
		if name == "" {
			name = p.AgentName
		}
		if name == "" {
			return nil
		}
		ev := base
		ev.Kind = event.AgentSpawned
		ev.AgentID = "teammate:" + name
		ev.AgentName = name
		ev.AgentType = "teammate"
		ev.Detail = "idle"
		return []event.Event{ev}

	case "PreCompact":
		ev := base
		ev.Kind = event.CompactStarted
		ev.Detail = p.Trigger
		return []event.Event{ev}

	case "PostCompact":
		ev := base
		ev.Kind = event.CompactFinished
		ev.Detail = p.Trigger
		return []event.Event{ev}
	}

	// Well-formed but unmapped (Stop, TaskCompleted, ...).
	return nil
}

// targetFrom picks the most specific target from tool_input, matching the
// Event.Target contract: file path, command, or pattern.
func targetFrom(in toolInput) string {
	for _, v := range []string{in.Command, in.FilePath, in.Pattern, in.URL, in.Path, in.Description} {
		if v != "" {
			return v
		}
	}
	return ""
}

func askKind(toolName string) string {
	switch toolName {
	case "Bash":
		return "command"
	case "Edit", "Write", "MultiEdit":
		return "file_write"
	}
	if strings.HasPrefix(toolName, "mcp__") {
		return "mcp"
	}
	return "other"
}

func askCommand(in toolInput) string {
	if in.Command != "" {
		return in.Command
	}
	return in.FilePath
}

func suggestionTypes(sugs []suggestion) []string {
	if len(sugs) == 0 {
		return nil
	}
	types := make([]string, 0, len(sugs))
	for _, s := range sugs {
		if s.Type != "" {
			types = append(types, s.Type)
		}
	}
	if len(types) == 0 {
		return nil
	}
	return types
}

// patchLines derives LinesAdd/LinesDel from a structuredPatch in the
// tool_response, when present. Hook captures don't guarantee the field
// (transcript toolUseResult is the richer channel), so 0,0 is the honest
// fallback rather than an invented count.
func patchLines(resp json.RawMessage) (add, del int) {
	if len(resp) == 0 {
		return 0, 0
	}
	var r struct {
		StructuredPatch []struct {
			Lines []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(resp, &r) != nil {
		return 0, 0
	}
	for _, hunk := range r.StructuredPatch {
		for _, l := range hunk.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				add++
			case strings.HasPrefix(l, "-"):
				del++
			}
		}
	}
	return add, del
}
