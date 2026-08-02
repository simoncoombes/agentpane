package state

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const (
	callRing     = 20
	dedupeWindow = 8192
	// teammateType marks a named agent (§3.15): terminal status, near-zero
	// telemetry, never in the alert band.
	teammateType = "teammate"
)

var (
	// watchRe catches the §2.5 canonical stuck case; the bare -w flag is
	// matched as a whole token separately.
	watchRe = regexp.MustCompile(`--watch\b|\bnodemon\b|\btail\s+-f\b`)
	// verifyRe is the §2.2 inferred-verify heuristic over Bash commands.
	verifyRe = regexp.MustCompile(`(?i)\b(tests?|lint|type-?check|vet|build)\b`)
)

// Machine consumes source events and derives everything the platform cannot
// emit. It is not safe for concurrent use: the UI goroutine owns it.
type Machine struct {
	cfg     Config
	allowRe *regexp.Regexp

	// now is the latest time seen by Apply/Tick; Snapshot aligns spark
	// buckets and active-run durations to it. Never time.Now().
	now time.Time

	sessionID    string
	sessionState SessionState
	connected    bool

	main mainState

	agents     map[string]*agentState
	order      []string // spawn order, subagents only
	spawnCount int

	asks     []*askState // oldest first
	askByKey map[string]*askState
	// ghosts maps a folded retry agent's id to its ask key so a row is
	// never created for it, even if its spawn events arrive later (§3.7.7).
	ghosts map[string]string

	contentions map[string]*contentionState

	run     *runState
	lastRun *Run
	runSeq  int

	dropped    int
	seen       map[dupKey]struct{}
	seenOld    map[dupKey]struct{}
	derivedSeq uint64
	rev        uint64
}

type dupKey struct {
	agent string
	kind  event.Kind
	seq   uint64
}

type mainState struct {
	title string
	// titleSeq is the ingest Seq of the line `title` came from. Titles are
	// rewritten as a session evolves and the mux's Seq is the only total
	// order agentpane has (ai-title lines carry no timestamp), so a lower
	// Seq arriving late must not overwrite a newer title.
	titleSeq    uint64
	todo        string
	activity    string
	tokens      int
	lastEventAt time.Time
	editedFiles map[string]struct{}
	rev         uint64
}

// nameExact on agentState records whether the current name came from an exact
// id↔description join (transcript sidecar) rather than a hooks correlation.
type agentState struct {
	nameExact bool

	id       string
	name     string
	typ      string
	teammate bool
	// folded: a retry ghost absorbed into an ask band entry; never
	// rendered, never counted toward run settlement (§3.7.7).
	folded bool
	// spawned: an AgentSpawned was observed for this agent, i.e. a real spawn
	// record exists (a transcript sidecar, the Agent call's agentId join, or a
	// hook claim the agent corroborated).
	// worked: at least one event carrying actual activity was observed.
	// earned: the §1.5 row-eligibility decision, latched — once true it never
	// goes back, so a row the user has watched cannot vanish (see
	// Machine.quiet).
	spawned bool
	worked  bool
	earned  bool

	status     Status
	station    int
	retries    int
	verified   bool
	decayed    bool
	spawnIndex int

	spawnedAt   time.Time
	lastEventAt time.Time
	doneAt      time.Time

	tokens        int
	linesAdd      int
	linesDel      int
	currentTool   string
	currentTarget string
	activity      string
	openFile      string
	lastError     string
	lastMessage   string

	open       *OpenCall
	calls      []Call
	totalCalls int

	sparkSec [60]int64
	spark    [60]SparkBucket

	editedFiles map[string]struct{}
	failedCmds  map[string]struct{}
	ask         *event.AskPayload
	rev         uint64
}

type askState struct {
	key         string
	agentID     string
	agentName   string
	tool        string
	command     string
	kind        string
	raisedAt    time.Time
	dupes       int
	ghostIDs    map[string]struct{}
	resolving   bool
	resolvingAt time.Time
}

type contentionState struct {
	path       string
	agentIDs   []string
	detectedAt time.Time
}

type runState struct {
	id          string
	startedAt   time.Time
	prompt      string
	members     []string
	tokens      int
	stalls      int
	contentions int
}

// New builds a machine. Zero durations in cfg fall back to spec defaults;
// WatchNeverExempt is honoured verbatim (start from DefaultConfig for the
// spec default of true).
func New(cfg Config) *Machine {
	cfg = cfg.normalized()
	return &Machine{
		cfg:         cfg,
		allowRe:     compileAllow(cfg.LongRunningAllow),
		connected:   true,
		main:        mainState{editedFiles: map[string]struct{}{}},
		agents:      map[string]*agentState{},
		askByKey:    map[string]*askState{},
		ghosts:      map[string]string{},
		contentions: map[string]*contentionState{},
		seen:        make(map[dupKey]struct{}, 1024),
	}
}

// Reset clears every piece of per-session world state and pins the machine
// to sessionID ("" adopts the first SessionStart seen, as before). A session
// switch (§3.8a) calls it so session-A agents, asks, and runs can never leak
// into session B; pinning the id up front also lets Apply drop cross-session
// registry noise before the session's own SessionStart arrives. Config
// survives; the dedupe window is cleared because each attach renumbers Seq.
func (m *Machine) Reset(sessionID string) {
	m.sessionID = sessionID
	m.sessionState = SessionActive
	m.connected = true
	m.main = mainState{editedFiles: map[string]struct{}{}}
	m.agents = map[string]*agentState{}
	m.order = nil
	m.spawnCount = 0
	m.asks = nil
	m.askByKey = map[string]*askState{}
	m.ghosts = map[string]string{}
	m.contentions = map[string]*contentionState{}
	m.run = nil
	m.lastRun = nil
	m.runSeq = 0
	m.dropped = 0
	m.seen = make(map[dupKey]struct{}, 1024)
	m.seenOld = nil
	m.bumpRev()
}

func compileAllow(tokens []string) *regexp.Regexp {
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(t))
	}
	if len(parts) == 0 {
		return nil
	}
	re, err := regexp.Compile(`\b(?:` + strings.Join(parts, "|") + `)\b`)
	if err != nil {
		return nil // C9: never panic on user input
	}
	return re
}

// Accepts reports whether ev belongs to the session this machine is pinned
// to. It is the SINGLE authority on that question: Apply drops what it
// rejects, and every other per-event consumer in the process (the inspector
// log above all) must ask it first, or a sibling session's row lands in a
// pinned pane's log while the world correctly ignores it.
//
// Cross-session events must not mutate this world: the registry reports every
// concurrent session (SessionIdle, NotificationRaised), and a session switch
// can leave stale events of the previous session in flight. Events without a
// session id (transport lifecycle, hooks quirks) are genuinely global and
// pass, as does everything while the machine is unpinned.
func (m *Machine) Accepts(ev event.Event) bool {
	return ev.SessionID == "" || m.sessionID == "" || ev.SessionID == m.sessionID
}

// Apply feeds one source event into the machine and returns any derived
// events it produced. Malformed, derived-from-source, cross-session, and
// unresolvable events are counted and dropped, never panicked on (C9).
// Duplicate events keyed on (AgentID, Kind, Seq) are silent no-ops (§1.4).
func (m *Machine) Apply(ev event.Event, now time.Time) []event.Event {
	m.advance(now)
	if ev.Kind == event.KindInvalid || ev.Kind.Derived() {
		m.dropped++
		return nil
	}
	if !m.Accepts(ev) {
		return nil
	}
	if !m.firstTime(ev) {
		return nil
	}
	at := ev.At
	if at.IsZero() {
		at = now
	}

	var out []event.Event
	switch ev.Kind {
	case event.SourceConnected:
		m.connected = true
		m.bumpRev()
	case event.SourceDisconnected:
		m.connected = false
		m.bumpRev()
	case event.SessionStart:
		if ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		m.sessionState = SessionActive
		m.bumpRev()
	case event.SessionEnd:
		m.sessionState = SessionEnded
		m.bumpRev()
		out = append(out, m.endRun(now)...)
	case event.SessionIdle:
		if m.sessionState != SessionEnded {
			m.sessionState = SessionIdle
			m.bumpRev()
		}
		out = append(out, m.checkRunEnd(now)...)
	case event.UserPromptSubmitted:
		m.sessionState = SessionActive
		if m.sessionID == "" && ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		m.bumpRev()
		if m.run == nil {
			m.runSeq++
			m.run = &runState{
				id:        runID(m.sessionID, m.runSeq),
				startedAt: at,
				prompt:    ev.Detail,
			}
			out = append(out, m.derived(event.RunStarted, "", now, "", "", ev.Detail))
		}
		// A new run means main's earlier edits no longer contend.
		out = append(out, m.resetMainEdits(now)...)
	case event.SessionTitled:
		// Session metadata, not main's work: it sets the row label and
		// bumps the global rev so the UI repaints, but it never touches
		// main.lastEventAt (the liveness clock) or main.rev (the §3.19
		// away-digest change mark) — a retitle is not progress you missed.
		// Titles are rewritten per turn, so the newest Seq wins and a
		// late-arriving older line is ignored.
		if ev.Detail != "" && ev.Seq >= m.main.titleSeq && ev.Detail != m.main.title {
			m.main.title = ev.Detail
			m.main.titleSeq = ev.Seq
			m.bumpRev()
		} else if ev.Detail != "" && ev.Seq >= m.main.titleSeq {
			m.main.titleSeq = ev.Seq // same text: idempotent, no repaint
		}
	case event.CompactStarted:
		if m.sessionState != SessionEnded {
			m.sessionState = SessionCompacting
			m.bumpRev()
		}
	case event.CompactFinished:
		if m.sessionState == SessionCompacting {
			m.sessionState = SessionActive
			m.bumpRev()
		}
	default:
		out = m.applyAgentEvent(ev, now, at)
	}
	return out
}

// Tick runs the timer-driven derivations: stall detection (§2.5), decay
// (§3.5), unknown-after-silence (§2.4), and the ask resolution grace
// (§3.7.8). Call it on the UI's clock tick with an injected now.
func (m *Machine) Tick(now time.Time) []event.Event {
	m.advance(now)
	var out []event.Event
	for _, id := range m.order {
		a := m.agents[id]
		if a.folded || a.teammate {
			continue
		}
		if m.quiet(a) {
			// An agent that has shown nothing at all cannot stall, decay or go
			// unknown: silence is all it has ever produced, so a derived alert
			// about it would be an alert about nothing — and a StallDetected
			// would put an unnamed phantom in the NEEDS YOU band and count a
			// stall against the run (§1.5, Machine.quiet).
			continue
		}
		silence := now.Sub(a.lastEventAt)
		switch a.status {
		case StatusQueued, StatusRun, StatusStuck, StatusAsk:
			if silence >= m.cfg.UnknownAfter {
				a.status = StatusUnknown
				a.rev = m.bumpRev()
				continue
			}
		}
		if a.status == StatusRun && silence >= m.stallThreshold(a) {
			a.status = StatusStuck
			a.earned = true // a stall needs the user: never suppressed, ever again
			a.rev = m.bumpRev()
			if m.run != nil {
				m.run.stalls++
			}
			detail := fmt.Sprintf("no events for %s", silence.Round(time.Second))
			tool, target := "", ""
			if a.open != nil {
				tool, target = a.open.Tool, a.open.Target
				detail = fmt.Sprintf("open %s call quiet for %s", a.open.Tool, silence.Round(time.Second))
			}
			out = append(out, m.derived(event.StallDetected, id, now, tool, target, detail))
		}
		if a.status == StatusDone && !a.decayed && !a.doneAt.IsZero() && now.Sub(a.doneAt) >= m.cfg.DecayAfter {
			a.decayed = true
			a.rev = m.bumpRev()
			out = append(out, m.derived(event.AgentDecayed, id, now, "", "", "settled "+now.Sub(a.doneAt).Round(time.Second).String()+" ago"))
		}
	}
	var expired []*askState
	for _, ask := range m.asks {
		if ask.resolving && now.Sub(ask.resolvingAt) >= m.cfg.ResolveGrace {
			expired = append(expired, ask)
		}
	}
	for _, ask := range expired {
		m.removeAsk(ask)
	}
	out = append(out, m.checkRunEnd(now)...)
	return out
}

// Snapshot returns a copy-safe view of the world, agents already in §3.6
// render order.
func (m *Machine) Snapshot() World {
	w := World{
		Session: Session{
			ID:      m.sessionID,
			ShortID: shortID(m.sessionID),
			State:   m.sessionState,
		},
		Main: Main{
			Title:       m.main.title,
			Todo:        m.main.todo,
			Activity:    m.main.activity,
			Tokens:      m.main.tokens,
			LastEventAt: m.main.lastEventAt,
			Rev:         m.main.rev,
		},
		Connected:     m.connected,
		DroppedEvents: m.dropped,
		Rev:           m.rev,
	}
	agents := make([]Agent, 0, len(m.order))
	for _, id := range m.order {
		a := m.agents[id]
		if a.folded {
			continue
		}
		if m.quiet(a) {
			w.Quiet = append(w.Quiet, QuietAgent{ID: a.id, Type: a.typ, Status: a.status})
			continue
		}
		agents = append(agents, m.snapshotAgent(a))
	}
	// Spawn-order iteration + stable sort = §3.6 ordering: only a status
	// (attention) change can reorder.
	sort.SliceStable(agents, func(i, j int) bool {
		return agents[i].Status.Rank() < agents[j].Status.Rank()
	})
	w.Agents = agents

	if len(m.asks) > 0 {
		w.Asks = make([]Ask, 0, len(m.asks))
		for _, a := range m.asks {
			w.Asks = append(w.Asks, Ask{
				Key:          a.key,
				AgentID:      a.agentID,
				Description:  a.agentName,
				Tool:         a.tool,
				Command:      a.command,
				Kind:         a.kind,
				RaisedAt:     a.raisedAt,
				DupeCount:    a.dupes,
				FoldedGhosts: len(a.ghostIDs),
				Resolving:    a.resolving,
			})
		}
	}
	if len(m.contentions) > 0 {
		w.Contentions = make([]Contention, 0, len(m.contentions))
		for _, c := range m.contentions {
			ids := append([]string(nil), c.agentIDs...)
			names := make([]string, len(ids))
			for i, id := range ids {
				names[i] = m.agentDisplayName(id)
			}
			w.Contentions = append(w.Contentions, Contention{
				Path:       c.path,
				AgentIDs:   ids,
				AgentNames: names,
				DetectedAt: c.detectedAt,
			})
		}
		sort.Slice(w.Contentions, func(i, j int) bool {
			if !w.Contentions[i].DetectedAt.Equal(w.Contentions[j].DetectedAt) {
				return w.Contentions[i].DetectedAt.After(w.Contentions[j].DetectedAt)
			}
			return w.Contentions[i].Path < w.Contentions[j].Path
		})
	}
	if m.run != nil {
		r := m.buildRun(m.now, time.Time{})
		w.Run = &r
	}
	if m.lastRun != nil {
		r := *m.lastRun
		r.Agents = append([]RunAgent(nil), m.lastRun.Agents...)
		w.LastRun = &r
	}
	return w
}

// quiet reports whether an agent has not earned a tree row: the platform said
// it exists and nothing else. A row is earned by one of, and only one of,
//
//	(i)   a name or description, from any source;
//	(ii)  a spawn record — an AgentSpawned, which only ever comes from a real
//	      one (the transcript's agent-<id>.meta.json sidecar, the Agent call's
//	      toolUseResult.agentId join, or a hook claim the agent corroborated);
//	(iii) one observed unit of work — a tool call, a file, a token, an error;
//	(iv)  something that needs the user: a pending permission ask, or a stall.
//
// Once earned the row is LATCHED for the rest of the run (agentState.earned):
// a row the user has watched must never disappear underneath them, however
// quiet the agent later looks.
//
// The rule exists because Claude Code announces subagents that never do
// anything: a session with 5 real subagents also emitted SubagentStart/Stop
// for ~40 entities that ran no tool, wrote no transcript, and settled
// instantly. Those arrive as AgentStarted + AgentReturned and nothing else —
// no sidecar, so no AgentSpawned, which is what (ii) discriminates on.
// Suppression is display-only: the agents stay in the model and are reported
// as World.Quiet (§1.5 — honest degradation, never a silent drop).
//
// Two things are deliberately NOT on the list:
//
//   - a final message. SubagentStop always carries last_assistant_message, so
//     counting it let every long-lived phantom through: a real session with 5
//     named subagents also rendered 4 unnamed rows that ran ~90s, touched no
//     tool, spent no tokens, and wrote no transcript anywhere on disk.
//   - merely being alive. An earlier rule gave any unsettled agent a row once
//     it outlived a grace period, which rendered exactly those long-lived
//     phantoms for ~90s and then DELETED the row the moment they settled.
//     Nothing may appear on the strength of existing alone; an agent that is
//     genuinely working produces (iii) within its first turn, and until then a
//     row that says nothing is worth less than the count on the quiet line.
//
// Teammates are never suppressed: they are a declared low-telemetry class
// (§3.15) and they always have a name.
func (m *Machine) quiet(a *agentState) bool {
	if a.earned {
		return false
	}
	if earnsRow(a) {
		a.earned = true
		return false
	}
	return true
}

// earnsRow is the unlatched half of the rule documented on Machine.quiet:
// whether this agent, right now, is showing one of the things that earns a
// row. Callers latch the answer into agentState.earned; nothing ever clears it.
func earnsRow(a *agentState) bool {
	if a.teammate || a.name != "" || a.spawned || a.worked ||
		a.tokens > 0 || a.totalCalls > 0 || a.lastError != "" || a.ask != nil {
		return true
	}
	switch a.status {
	case StatusAsk, StatusStuck:
		return true
	}
	return false
}

// --- agent events ---

func (m *Machine) applyAgentEvent(ev event.Event, now, at time.Time) []event.Event {
	if ev.AgentID == "" {
		// An agent-scoped kind without an agent reference cannot be
		// resolved: count it, never guess (C8).
		m.dropped++
		return nil
	}
	if ev.AgentID == event.MainAgentID {
		return m.applyMainEvent(ev, now, at)
	}

	var out []event.Event
	a := m.agents[ev.AgentID]
	if a == nil {
		a = m.createAgent(ev, at)
	}
	// Names have provenance. A hooks name is CORRELATED: the description and
	// the agent id arrive on different payloads (the spawning PreToolUse has
	// no agent_id; SubagentStart has no description), so hooksrc joins them
	// heuristically and can be wrong when the platform interleaves phantom
	// starts. The transcript's sidecar is an EXACT join — agentId and
	// description in one file — so it must be able to correct a correlated
	// name. First-name-wins would freeze a mis-attribution for the whole run,
	// which is the one failure a monitor must not have (C8).
	if ev.AgentName != "" && (a.name == "" || (!a.nameExact && ev.NameExact)) {
		a.name = ev.AgentName
		a.nameExact = ev.NameExact
	}
	if a.typ == "" && ev.AgentType != "" {
		a.typ = ev.AgentType
	}
	if ev.AgentType == teammateType && !a.teammate {
		a.teammate = true
		a.status = StatusTeammate
	}
	// §1.5 row eligibility (Machine.quiet): a spawn record and observed
	// activity are the two things that separate a real agent from one the
	// platform merely announced.
	if ev.Kind == event.AgentSpawned {
		a.spawned = true
	}
	if isActivity(ev.Kind) || ev.Kind == event.PermissionRequested || ev.Kind == event.ErrorRaised {
		a.worked = true
	}
	if at.After(a.lastEventAt) {
		a.lastEventAt = at
	}
	a.rev = m.bumpRev()

	// Any agent event clears stuck (§2.4) and revives unknown; activity
	// from an asking agent implies the grant happened (§3.7.8).
	if !a.teammate && a.status != StatusDone {
		switch {
		case a.status == StatusStuck:
			a.status = StatusRun
			out = append(out, m.derived(event.StallCleared, a.id, now, "", "", ""))
		case a.status == StatusUnknown:
			a.status = StatusRun
		case a.status == StatusAsk && isActivity(ev.Kind):
			a.status = StatusRun
		}
	}

	if ev.Kind != event.PermissionRequested {
		m.resolveAsksFor(a.id, ev.Kind, at)
	}
	if d := accrueTokens(&a.tokens, ev); d > 0 {
		a.bucket(at).Tokens += d
		if m.run != nil {
			m.run.tokens += d
		}
	}

	switch ev.Kind {
	case event.AgentSpawned:
		if a.spawnedAt.IsZero() || at.Before(a.spawnedAt) {
			a.spawnedAt = at
		}
	case event.AgentStarted:
		if a.status == StatusQueued {
			a.status = StatusRun
		}
	case event.AgentReturned, event.AgentMerged:
		out = append(out, m.agentReturned(a, ev, now, at)...)
	case event.ToolStart:
		out = append(out, m.toolStart(a, ev, now, at)...)
	case event.ToolEnd:
		out = append(out, m.toolEnd(a, ev, now, at)...)
	case event.ToolOutput:
		// Background output growth is real liveness (§2.6a): it keeps
		// the call-cadence sparkline alive.
		a.bucket(at).Calls++
	case event.FileRead:
		a.station = max(a.station, 1)
		if ev.Target != "" {
			a.openFile = ev.Target
		}
		setActivity(&a.activity, ev)
	case event.FileEdited:
		out = append(out, m.fileEdited(a, ev, now, at)...)
	case event.TokensUpdated:
		// Accounted above for every event kind.
	case event.ThinkingEnded:
		// Labels past activity only; must never change a stall
		// threshold (§2.5).
		if ev.Detail != "" {
			a.activity = ev.Detail
		}
	case event.PermissionRequested:
		out = append(out, m.raiseAsk(a.id, a.name, a, ev, now, at)...)
	case event.PermissionGranted:
		if a.status == StatusAsk {
			a.status = StatusRun
		}
	case event.PermissionDenied:
		if a.status == StatusAsk {
			a.status = StatusRun
		}
		a.ask = nil
	case event.NotificationRaised:
		if ev.Detail != "" {
			a.activity = ev.Detail
		}
	case event.ErrorRaised:
		if ev.Err != "" {
			a.lastError = ev.Err
		} else if ev.Detail != "" {
			a.lastError = ev.Detail
		}
	}
	// Latch the row-eligibility decision on the write path, after this event's
	// own effects, so a row earned only transiently — an ask that is then
	// granted — stays earned even if no snapshot was taken while it showed
	// (Machine.quiet).
	if !a.earned && earnsRow(a) {
		a.earned = true
	}
	return out
}

func (m *Machine) createAgent(ev event.Event, at time.Time) *agentState {
	m.spawnCount++
	a := &agentState{
		id:          ev.AgentID,
		name:        ev.AgentName,
		typ:         ev.AgentType,
		spawnIndex:  m.spawnCount,
		spawnedAt:   at,
		lastEventAt: at,
		editedFiles: map[string]struct{}{},
		failedCmds:  map[string]struct{}{},
	}
	switch ev.Kind {
	case event.AgentSpawned:
		a.status = StatusQueued
	case event.AgentReturned, event.AgentMerged:
		// Out-of-order tolerance: the return may arrive first.
		a.status = StatusDone
		a.doneAt = at
		a.station = 3
	default:
		// Any other activity means it exists and is working.
		a.status = StatusRun
	}
	if ev.AgentType == teammateType {
		a.teammate = true
		a.status = StatusTeammate
	}
	if _, ghost := m.ghosts[ev.AgentID]; ghost {
		a.folded = true
	}
	m.agents[ev.AgentID] = a
	m.order = append(m.order, ev.AgentID)
	if m.run != nil {
		m.run.members = append(m.run.members, ev.AgentID)
	}
	return a
}

func (m *Machine) agentReturned(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	if a.teammate {
		return nil // teammate is terminal (§2.4)
	}
	if a.status != StatusDone {
		a.status = StatusDone
		a.doneAt = at
	}
	a.station = max(a.station, 3)
	if ev.Detail != "" {
		a.lastMessage = ev.Detail
	}
	if ev.Err != "" {
		a.lastError = ev.Err
	}
	if a.open != nil {
		out = append(out, m.derived(event.CallClosed, a.id, now, a.open.Tool, a.open.Target, ""))
		a.open = nil
	}
	for _, p := range sortedKeys(a.editedFiles) {
		out = append(out, m.recomputeContention(p, now)...)
	}
	out = append(out, m.checkRunEnd(now)...)
	return out
}

func (m *Machine) toolStart(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	if a.status == StatusQueued {
		a.status = StatusRun
	}
	a.station = max(a.station, 1)
	a.currentTool, a.currentTarget = ev.Tool, ev.Target
	setActivity(&a.activity, ev)
	a.open = &OpenCall{Tool: ev.Tool, Target: ev.Target, Since: at}
	a.bucket(at).Calls++
	out = append(out, m.derived(event.CallOpened, a.id, now, ev.Tool, ev.Target, ""))
	if _, failed := a.failedCmds[cmdKey(ev.Tool, ev.Target)]; failed {
		a.retries++
		out = append(out, m.derived(event.RetryInferred, a.id, now, ev.Tool, ev.Target, fmt.Sprintf("retry %d (inferred)", a.retries)))
	}
	return out
}

func (m *Machine) toolEnd(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	a.station = max(a.station, 1)
	tool, target := ev.Tool, ev.Target
	if a.open != nil && (ev.Tool == "" || ev.Tool == a.open.Tool) {
		if tool == "" {
			tool = a.open.Tool
		}
		if target == "" {
			target = a.open.Target
		}
		a.open = nil
		out = append(out, m.derived(event.CallClosed, a.id, now, tool, target, ""))
	}
	failed := ev.Err != "" || (ev.ExitCode != nil && *ev.ExitCode != 0)
	a.appendCall(Call{Tool: tool, Target: target, Err: failed, Meta: callMeta(ev), At: at})
	key := cmdKey(tool, target)
	if failed {
		a.failedCmds[key] = struct{}{}
		if ev.Err != "" {
			a.lastError = ev.Err
		} else {
			a.lastError = fmt.Sprintf("exit %d", *ev.ExitCode)
		}
	} else {
		delete(a.failedCmds, key)
	}
	if !failed && tool == "Bash" && !a.verified && verifyRe.MatchString(target) {
		// §2.2/§3.14 inferred verify: only a check that completed
		// successfully counts — §2.10 grants the marker to run-auth-tests'
		// passing suite, never to fix-ts2345's failing typechecks.
		a.verified = true
		out = append(out, m.derived(event.VerifyObserved, a.id, now, tool, target, "verify pattern matched (inferred)"))
	}
	a.currentTool, a.currentTarget = tool, target
	setActivity(&a.activity, ev)
	a.bucket(at).Calls++
	return out
}

func (m *Machine) fileEdited(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	a.station = max(a.station, 2)
	a.linesAdd += ev.LinesAdd
	a.linesDel += ev.LinesDel
	setActivity(&a.activity, ev)
	if ev.Target != "" {
		a.openFile = ev.Target
		if _, seen := a.editedFiles[ev.Target]; !seen {
			a.editedFiles[ev.Target] = struct{}{}
			out = append(out, m.recomputeContention(ev.Target, now)...)
		}
	}
	a.mergeEditCall(ev, at)
	return out
}

// --- main agent ---

func (m *Machine) applyMainEvent(ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	if at.After(m.main.lastEventAt) {
		m.main.lastEventAt = at
	}
	m.main.rev = m.bumpRev()
	if ev.Kind != event.PermissionRequested {
		m.resolveAsksFor(event.MainAgentID, ev.Kind, at)
	}
	if d := accrueTokens(&m.main.tokens, ev); d > 0 && m.run != nil {
		m.run.tokens += d
	}
	switch ev.Kind {
	case event.ToolStart, event.ToolEnd:
		if ev.Tool == "TodoWrite" {
			// Only the ToolStart twin carries the todo text (the
			// transcript's channel, DATA-SOURCES §5); the hooks ToolEnd
			// puts duration_ms in Detail ("142ms") and must touch neither
			// the todo nor the activity line.
			if ev.Kind == event.ToolStart && ev.Detail != "" {
				m.main.todo = ev.Detail
			}
		} else {
			setActivity(&m.main.activity, ev)
		}
	case event.FileRead:
		setActivity(&m.main.activity, ev)
	case event.FileEdited:
		setActivity(&m.main.activity, ev)
		if ev.Target != "" {
			if _, seen := m.main.editedFiles[ev.Target]; !seen {
				m.main.editedFiles[ev.Target] = struct{}{}
				out = append(out, m.recomputeContention(ev.Target, now)...)
			}
		}
	case event.ThinkingEnded, event.NotificationRaised:
		if ev.Detail != "" {
			m.main.activity = ev.Detail
		}
	case event.PermissionRequested:
		out = append(out, m.raiseAsk(event.MainAgentID, event.MainAgentID, nil, ev, now, at)...)
	}
	return out
}

// --- asks (§3.7) ---

func (m *Machine) raiseAsk(agentID, agentName string, a *agentState, ev event.Event, now, at time.Time) []event.Event {
	if a != nil {
		if a.teammate {
			return nil // teammates never appear in the band (§3.15)
		}
		if a.status == StatusDone {
			return nil // stale out-of-order request
		}
	}
	tool := ev.Tool
	cmd := ev.Target
	kind := ""
	var payload *event.AskPayload
	if ev.Ask != nil {
		if ev.Ask.Command != "" {
			cmd = ev.Ask.Command
		}
		kind = ev.Ask.Kind
		cp := *ev.Ask
		cp.Options = append([]string(nil), ev.Ask.Options...)
		payload = &cp
	}
	if ev.Detail != "" {
		// The event's one-line summary ("asked to clear the TS cache")
		// feeds the §3.1 blocked activity line.
		if payload == nil {
			payload = &event.AskPayload{Kind: kind, Command: cmd}
		}
		payload.Detail = ev.Detail
	}
	key := askKey(tool, cmd)

	var out []event.Event
	if ask := m.askByKey[key]; ask != nil {
		ask.dupes++
		// A fresh request proves the ask is still live.
		ask.resolving = false
		if agentID != ask.agentID && agentID != event.MainAgentID {
			if _, dup := ask.ghostIDs[agentID]; !dup {
				ask.ghostIDs[agentID] = struct{}{}
				m.ghosts[agentID] = key
				if a != nil && !a.folded {
					a.folded = true
					for _, p := range sortedKeys(a.editedFiles) {
						out = append(out, m.recomputeContention(p, now)...)
					}
				}
				out = append(out, m.derived(event.GhostAgentFolded, agentID, now, tool, cmd, "retry agent folded into ask"))
			}
		}
		out = append(out, m.derived(event.AskDeduped, agentID, now, tool, cmd, fmt.Sprintf("×%d", ask.dupes)))
		return out
	}

	ask := &askState{
		key:       key,
		agentID:   agentID,
		agentName: agentName,
		tool:      tool,
		command:   cmd,
		kind:      kind,
		raisedAt:  at,
		dupes:     1,
		ghostIDs:  map[string]struct{}{},
	}
	m.asks = append(m.asks, ask)
	m.askByKey[key] = ask
	if a != nil {
		if !a.folded {
			a.status = StatusAsk
		}
		a.ask = payload
	}
	return out
}

// resolveAsksFor applies §3.7.8 to every ask owned by agentID: a resolution
// signal marks it resolving, the next event (or the Tick grace) clears it,
// and a denial or return clears it immediately. No derived kind exists for
// ask resolution; the World reflects it.
func (m *Machine) resolveAsksFor(agentID string, kind event.Kind, at time.Time) {
	var clear []*askState
	for _, ask := range m.asks {
		if ask.agentID != agentID {
			continue
		}
		switch {
		case kind == event.AgentReturned || kind == event.AgentMerged || kind == event.PermissionDenied:
			clear = append(clear, ask)
		case ask.resolving:
			clear = append(clear, ask)
		case kind == event.PermissionGranted || isActivity(kind):
			ask.resolving = true
			ask.resolvingAt = at
		}
	}
	for _, ask := range clear {
		m.removeAsk(ask)
	}
}

func (m *Machine) removeAsk(ask *askState) {
	delete(m.askByKey, ask.key)
	for i, x := range m.asks {
		if x == ask {
			m.asks = append(m.asks[:i], m.asks[i+1:]...)
			break
		}
	}
	if a := m.agents[ask.agentID]; a != nil {
		a.ask = nil
		if a.status == StatusAsk {
			a.status = StatusRun
		}
		a.rev = m.bumpRev()
	} else {
		m.bumpRev()
	}
}

// --- contention (§2.7) ---

func (m *Machine) editorsOf(path string) []string {
	var ids []string
	if _, ok := m.main.editedFiles[path]; ok && m.sessionState != SessionEnded {
		ids = append(ids, event.MainAgentID)
	}
	for _, id := range m.order {
		a := m.agents[id]
		if a.folded || a.status == StatusDone {
			continue
		}
		if _, ok := a.editedFiles[path]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *Machine) recomputeContention(path string, now time.Time) []event.Event {
	eds := m.editorsOf(path)
	c := m.contentions[path]
	switch {
	case len(eds) >= 2 && c == nil:
		m.contentions[path] = &contentionState{path: path, agentIDs: eds, detectedAt: now}
		if m.run != nil {
			m.run.contentions++
		}
		m.bumpRev()
		return []event.Event{m.derived(event.ContentionDetected, "", now, "", path,
			fmt.Sprintf("%d agents editing %s", len(eds), path))}
	case len(eds) >= 2:
		c.agentIDs = eds
		m.bumpRev()
	case c != nil:
		delete(m.contentions, path)
		m.bumpRev()
		return []event.Event{m.derived(event.ContentionCleared, "", now, "", path, "")}
	}
	return nil
}

func (m *Machine) resetMainEdits(now time.Time) []event.Event {
	if len(m.main.editedFiles) == 0 {
		return nil
	}
	paths := sortedKeys(m.main.editedFiles)
	m.main.editedFiles = map[string]struct{}{}
	var out []event.Event
	for _, p := range paths {
		out = append(out, m.recomputeContention(p, now)...)
	}
	return out
}

// --- runs (§2.8) ---

func (m *Machine) checkRunEnd(now time.Time) []event.Event {
	if m.run == nil || m.sessionState != SessionIdle {
		return nil
	}
	for _, id := range m.run.members {
		a := m.agents[id]
		if a == nil || a.folded || a.teammate {
			continue
		}
		switch a.status {
		case StatusQueued, StatusRun, StatusStuck, StatusAsk:
			return nil
		}
	}
	return m.endRun(now)
}

func (m *Machine) endRun(now time.Time) []event.Event {
	if m.run == nil {
		return nil
	}
	run := m.buildRun(now, now)
	m.lastRun = &run
	m.run = nil
	m.bumpRev()
	detail, err := json.Marshal(run)
	if err != nil {
		detail = []byte("{}")
	}
	return []event.Event{m.derived(event.RunEnded, "", now, "", "", string(detail))}
}

func (m *Machine) buildRun(now, endedAt time.Time) Run {
	r := Run{
		ID:          m.run.id,
		SessionID:   m.sessionID,
		StartedAt:   m.run.startedAt,
		EndedAt:     endedAt,
		Prompt:      m.run.prompt,
		Tokens:      m.run.tokens,
		Stalls:      m.run.stalls,
		Contentions: m.run.contentions,
	}
	ref := endedAt
	if ref.IsZero() {
		ref = now
	}
	for _, id := range m.run.members {
		a := m.agents[id]
		if a == nil || a.folded || m.quiet(a) {
			// A run summary is a per-agent bar chart; an agent with no name
			// and no observed work has no bar to draw, and forty of them
			// would relocate the phantom-row problem into the run history.
			continue
		}
		end := ref
		if a.status == StatusDone && !a.doneAt.IsZero() {
			end = a.doneAt
		}
		dur := end.Sub(a.spawnedAt)
		if dur < 0 {
			dur = 0
		}
		r.Agents = append(r.Agents, RunAgent{
			ID:       a.id,
			Name:     a.name,
			Status:   a.status,
			Duration: dur,
			Tokens:   a.tokens,
		})
	}
	return r
}

// --- stall (§2.5) ---

func (m *Machine) stallThreshold(a *agentState) time.Duration {
	if a.open != nil && a.open.Tool == "Bash" {
		cmd := a.open.Target
		// A quiet watch is the canonical stuck case: it beats the
		// allowlist whenever WatchNeverExempt holds.
		if m.cfg.WatchNeverExempt && isWatchCommand(cmd) {
			return m.cfg.StuckAfter
		}
		if m.allowRe != nil && m.allowRe.MatchString(cmd) {
			return m.cfg.LongRunningAfter
		}
	}
	return m.cfg.StuckAfter
}

func isWatchCommand(cmd string) bool {
	if watchRe.MatchString(cmd) {
		return true
	}
	for _, f := range strings.Fields(cmd) {
		if f == "-w" {
			return true
		}
	}
	return false
}

// --- helpers ---

func (m *Machine) advance(now time.Time) {
	if now.After(m.now) {
		m.now = now
	}
}

func (m *Machine) bumpRev() uint64 {
	m.rev++
	return m.rev
}

// firstTime implements the (AgentID, Kind, Seq) dedupe key of §1.4 over a
// bounded two-generation window. Seq 0 means "unassigned" and is never
// deduped on.
func (m *Machine) firstTime(ev event.Event) bool {
	if ev.Seq == 0 {
		return true
	}
	k := dupKey{agent: ev.AgentID, kind: ev.Kind, seq: ev.Seq}
	if _, ok := m.seen[k]; ok {
		return false
	}
	if _, ok := m.seenOld[k]; ok {
		return false
	}
	if len(m.seen) >= dedupeWindow {
		m.seenOld = m.seen
		m.seen = make(map[dupKey]struct{}, dedupeWindow)
	}
	m.seen[k] = struct{}{}
	return true
}

func (m *Machine) derived(kind event.Kind, agentID string, now time.Time, tool, target, detail string) event.Event {
	m.derivedSeq++
	return event.Event{
		Seq:       m.derivedSeq,
		At:        now,
		SessionID: m.sessionID,
		AgentID:   agentID,
		Kind:      kind,
		Tool:      tool,
		Target:    target,
		Detail:    detail,
	}
}

func (m *Machine) agentDisplayName(id string) string {
	if id == event.MainAgentID {
		return event.MainAgentID
	}
	if a := m.agents[id]; a != nil && a.name != "" {
		return a.name
	}
	return id
}

func (m *Machine) snapshotAgent(a *agentState) Agent {
	out := Agent{
		ID:            a.id,
		Name:          a.name,
		Type:          a.typ,
		Teammate:      a.teammate,
		Status:        a.status,
		Station:       a.station,
		Retries:       a.retries,
		Verified:      a.verified,
		Decayed:       a.decayed,
		SpawnIndex:    a.spawnIndex,
		SpawnedAt:     a.spawnedAt,
		LastEventAt:   a.lastEventAt,
		DoneAt:        a.doneAt,
		Tokens:        a.tokens,
		LinesAdd:      a.linesAdd,
		LinesDel:      a.linesDel,
		CurrentTool:   a.currentTool,
		CurrentTarget: a.currentTarget,
		ActivityLine:  a.activity,
		OpenFile:      a.openFile,
		LastError:     a.lastError,
		LastMessage:   a.lastMessage,
		TotalCalls:    a.totalCalls,
		EditedFiles:   sortedKeys(a.editedFiles),
		Rev:           a.rev,
	}
	if a.open != nil {
		oc := *a.open
		out.OpenCall = &oc
	}
	if len(a.calls) > 0 {
		out.CallHistory = append([]Call(nil), a.calls...)
	}
	if a.ask != nil {
		cp := *a.ask
		cp.Options = append([]string(nil), a.ask.Options...)
		out.Ask = &cp
	}
	out.SparkBuckets, out.SparkAt = a.sparkSnapshot(m.now)
	return out
}

func (a *agentState) bucket(at time.Time) *SparkBucket {
	sec := at.Unix()
	i := int(sec % 60)
	if i < 0 {
		i += 60
	}
	if a.sparkSec[i] != sec {
		a.sparkSec[i] = sec
		a.spark[i] = SparkBucket{}
	}
	return &a.spark[i]
}

func (a *agentState) sparkSnapshot(now time.Time) ([60]SparkBucket, time.Time) {
	var out [60]SparkBucket
	if now.IsZero() {
		return out, time.Time{}
	}
	end := now.Unix()
	for j := 0; j < 60; j++ {
		sec := end - int64(59-j)
		i := int(sec % 60)
		if i < 0 {
			i += 60
		}
		if a.sparkSec[i] == sec {
			out[j] = a.spark[i]
		}
	}
	return out, time.Unix(end, 0).UTC()
}

func (a *agentState) appendCall(c Call) {
	a.totalCalls++
	a.calls = append(a.calls, c)
	if len(a.calls) > callRing {
		a.calls = a.calls[len(a.calls)-callRing:]
	}
}

// mergeEditCall folds a FileEdited into the ToolEnd record the same edit
// usually also produced, so one edit never shows as two calls.
//
// TODO(agentpane): this assumes the hooks order (ToolEnd, then FileEdited).
// The transcript source emits FileEdited BEFORE its ToolEnd (parse.go
// richEvents), so in transcript-fallback mode one edit still records two
// calls; toolEnd needs the symmetric merge.
func (a *agentState) mergeEditCall(ev event.Event, at time.Time) {
	meta := diffMeta(ev.LinesAdd, ev.LinesDel)
	if n := len(a.calls); n > 0 {
		last := &a.calls[n-1]
		if last.Target == ev.Target && isEditTool(last.Tool) {
			if meta != "" {
				last.Meta = meta
			}
			return
		}
	}
	tool := ev.Tool
	if tool == "" {
		tool = "Edit"
	}
	a.appendCall(Call{Tool: tool, Target: ev.Target, Meta: meta, At: at})
}

// accrueTokens folds an event's token report into a running total, handling
// both cumulative and delta reporting (event.Event.TokensCum), and returns
// the positive delta. Stale cumulative values (out-of-order) accrue nothing.
func accrueTokens(total *int, ev event.Event) int {
	if ev.Tokens <= 0 {
		return 0
	}
	if ev.TokensCum {
		if ev.Tokens <= *total {
			return 0
		}
		d := ev.Tokens - *total
		*total = ev.Tokens
		return d
	}
	*total += ev.Tokens
	return ev.Tokens
}

// isActivity reports the kinds that prove the agent is unblocked and doing
// work — the "PostToolUse-ish" set of §3.7.8. NotificationRaised and
// ErrorRaised are deliberately excluded: the permission nudge notification
// must not resolve its own ask.
func isActivity(k event.Kind) bool {
	switch k {
	case event.ToolStart, event.ToolEnd, event.ToolOutput,
		event.FileRead, event.FileEdited,
		event.TokensUpdated, event.ThinkingEnded:
		return true
	}
	return false
}

func isEditTool(tool string) bool {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

func setActivity(dst *string, ev event.Event) {
	if ev.Detail != "" {
		*dst = ev.Detail
		return
	}
	if s := strings.TrimSpace(ev.Tool + " " + ev.Target); s != "" {
		*dst = s
	}
}

func callMeta(ev event.Event) string {
	if ev.LinesAdd > 0 || ev.LinesDel > 0 {
		return diffMeta(ev.LinesAdd, ev.LinesDel)
	}
	if ev.Err != "" {
		return "1 err" // §3.1: "✖ Bash pnpm typecheck  1 err"
	}
	if ev.ExitCode != nil && *ev.ExitCode != 0 {
		return fmt.Sprintf("exit %d", *ev.ExitCode)
	}
	return ""
}

func diffMeta(add, del int) string {
	if add == 0 && del == 0 {
		return ""
	}
	return fmt.Sprintf("+%d −%d", add, del)
}

// normInput is the §3.7.7 input normalisation: trim, collapse whitespace.
func normInput(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func askKey(tool, cmd string) string {
	return tool + "\x00" + normInput(cmd)
}

func cmdKey(tool, target string) string {
	return tool + "\x00" + normInput(target)
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[:4]
}

func runID(sessionID string, seq int) string {
	if s := shortID(sessionID); s != "" {
		return fmt.Sprintf("%s-r%d", s, seq)
	}
	return fmt.Sprintf("r%d", seq)
}
