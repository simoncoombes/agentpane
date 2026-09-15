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
	// verifyRe is the §2.2 inferred-verify heuristic over the Bash command
	// itself.
	verifyRe = regexp.MustCompile(`(?i)\b(tests?|lint|type-?check|vet|build)\b`)
	// verifySaysRe reads the tool's own description (event.Says) to recognise a
	// check whose command does not say so — `pnpm -s ci:all`, `make -s gate`
	// (§13.3 Q6: use the description to improve accuracy).
	//
	// It requires a verb of EXECUTION, because intent is not evidence: "run the
	// auth suite" describes a check being performed, while "fix the failing
	// tests" and "write a test for the parser" describe work ABOUT tests and
	// must never earn the marker. The exit code is still the only thing that
	// decides which marker.
	verifySaysRe = regexp.MustCompile(
		`(?i)\b(?:run|runs|running|re-?run|re-?runs|execute|executes|check|checks|verify|verifies)\b[^.]{0,32}?\b(?:tests?|suite|specs?|lint|type-?checks?|vet|build)\b`)
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
	// idleAt is when the last SessionIdle landed, so unidle can tell work
	// that came after it from work that was already in flight before it.
	idleAt time.Time
	// turnEnded latches the transcript's turn end until the next prompt, so a
	// run can settle without the session being called idle (checkRunEnd).
	turnEnded bool
	connected bool

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
	model       string
	effort      string
	tokens      int
	lastEventAt time.Time
	editedFiles map[string]struct{}
	rev         uint64
}

// nameExact on agentState records whether the current name came from an exact
// id↔description join (transcript sidecar) rather than a hooks correlation.
type agentState struct {
	nameExact bool

	id   string
	name string
	typ  string
	// model/effort: what the platform is running this agent on, verbatim from
	// its transcript. Empty when the pane has no transcript access — the hooks
	// carry neither, and the inspector says nothing rather than guessing (C8).
	model    string
	effort   string
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
	verify     VerifyState
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
	activityRaw   string
	openFile      string
	lastError     string
	lastMessage   string

	open       *OpenCall
	calls      []Call
	totalCalls int

	sparkSec [60]int64
	spark    [60]SparkBucket

	// hist is the lifetime series behind Agent.HistoryBuckets: hist[i] covers
	// [histFrom + i*histStep, +histStep). histStep is in seconds and doubles
	// whenever the slice would exceed histCap.
	hist     []SparkBucket
	histFrom time.Time
	histStep int

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
	m.idleAt = time.Time{}
	m.turnEnded = false
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

// unidle takes the session out of SessionIdle when an event proves it is
// working again.
//
// SessionIdle is a hint, not a state the session announces. The transcript
// raises it off `{"type":"system","subtype":"turn_duration"}`, which means
// "that turn ended", and the only events that used to clear it were
// SessionStart, UserPromptSubmitted and PostCompact. An interactive tab gets
// one of those with every prompt. A background job session never does: the
// work arrives as a job rather than as a typed prompt, so after its first
// turn ended the session latched idle for good and the pane drew the §3.8
// idle screen over a main agent that was running a Bash call every few
// seconds `[captured 2026-09-15]`.
//
// A tool call, a spawn or a permission prompt is work by definition, so any
// of them says the hint has expired. The timestamp guard is what keeps this
// from fighting a real idle: the transcript runs seconds behind, and a tool
// event stamped BEFORE the turn ended is the tail of the turn that just
// ended, not evidence of a new one.
func (m *Machine) unidle(ev event.Event, at time.Time) {
	if m.sessionState != SessionIdle {
		return
	}
	switch ev.Kind {
	case event.ToolStart, event.AgentSpawned, event.PermissionRequested:
	default:
		return
	}
	if at.Before(m.idleAt) {
		return
	}
	m.sessionState = SessionActive
	m.bumpRev()
}

// SessionID is the id this world is keyed to ("" = unpinned, accepts
// everything). It is what Accepts compares against, so a caller that has just
// been told the source stack moved can ask whether the world still matches it.
func (m *Machine) SessionID() string { return m.sessionID }

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
	// Sources flatten at ingest (§13.1) and the mux guarantees it for every
	// stack the binary builds. This second call is for the paths that feed a
	// machine directly — replays, the demo harness, tests — and is a no-op on
	// text that is already one line, so the invariant holds however the world
	// was fed: everything stored for display is row-shaped, everything stored
	// for `y` is the original.
	ev = event.Flattened(ev)
	at := ev.At
	if at.IsZero() {
		at = now
	}

	m.unidle(ev, at)

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
			m.idleAt = at
			m.bumpRev()
		}
		out = append(out, m.checkRunEnd(now)...)
	case event.TurnEnded:
		// A turn ending settles the run and nothing else. It is not idleness:
		// the session state drives a whole-layout swap to the §3.8 idle
		// screen, and a session that pauses between turns has not finished
		// anything the reader needs summarised.
		//
		// Measured on a background job session: 40 turn ends, and the gap to
		// the next tool call ran to a median of 125 s and a p90 of 627 s. Read
		// as idleness that is a pane which drops out of the tree, into a
		// summary of a run from earlier, for minutes at a time, over and over,
		// while the work it was opened to watch carries on `[captured
		// 2026-09-15]`.
		m.turnEnded = true
		out = append(out, m.checkRunEnd(now)...)
	case event.UserPromptSubmitted:
		m.sessionState = SessionActive
		if m.sessionID == "" && ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		m.bumpRev()
		m.turnEnded = false
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
		if m.recordedNothing(a) {
			// An agent that has shown nothing at all cannot stall, decay or go
			// unknown: silence is all it has ever produced, so a derived alert
			// about it would be an alert about nothing — and a StallDetected
			// would put an unnamed phantom in the NEEDS YOU band and count a
			// stall against the run (§1.5, Machine.quiet).
			//
			// This is the evidence test, NOT the display one: an empty agent is
			// rendered for its first stuck_after (§13.1) and must still be
			// spared these derivations for the whole run, or the moment it
			// crossed the threshold it would earn a permanent row by stalling.
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
			Model:       m.main.model,
			Effort:      m.main.effort,
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
//
// §13.1 amendment: "no recorded activity" means no tool call and no tokens, NOT
// "nothing yet". An agent spawned two seconds ago has shown nothing either, and
// hiding a real agent during its first breath is far worse than carrying a
// phantom row for a while — so suppression also waits for the agent to have
// been silent past stuck_after with nothing recorded. Row eligibility is still
// evidence-based; this only delays the negative verdict until the evidence has
// had time to arrive.
func (m *Machine) quiet(a *agentState) bool {
	if !m.recordedNothing(a) {
		return false
	}
	return m.pastFirstBreath(a)
}

// recordedNothing is the evidence half of the rule: this agent has shown none
// of the things that earn a row, now or ever (the answer is latched in
// agentState.earned once it flips).
//
// It is the predicate for every decision that must not be made about an empty
// agent regardless of how long it has existed: no stall, decay or unknown
// derivation (a StallDetected would put an unnamed phantom in the NEEDS YOU
// band and count a stall against the run), and no bar in a run summary.
func (m *Machine) recordedNothing(a *agentState) bool {
	if a.earned {
		return false
	}
	if earnsRow(a) {
		a.earned = true
		return false
	}
	return true
}

// pastFirstBreath reports whether an agent has had long enough to show
// something: stuck_after since it was announced AND stuck_after of silence.
// Both, because the two differ — an agent that keeps emitting events which earn
// nothing (a bare notification) is not a phantom that never woke up, and a
// clock skewed spawn time must not suppress a row on its own.
func (m *Machine) pastFirstBreath(a *agentState) bool {
	if m.now.IsZero() {
		return false
	}
	if m.now.Sub(a.spawnedAt) < m.cfg.StuckAfter {
		return false
	}
	if a.status == StatusDone {
		// A returned agent will never record anything, so its silence clock can
		// only postpone a verdict that cannot change. The spawn floor still
		// applies: a phantom that starts and stops in the same second keeps its
		// row for the first breath like everything else.
		return true
	}
	return m.now.Sub(a.lastEventAt) >= m.cfg.StuckAfter
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
		a.addTokens(at, d)
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
		a.addCall(at)
	case event.FileRead:
		a.station = max(a.station, 1)
		if ev.Target != "" {
			a.openFile = ev.Target
		}
		setActivity(&a.activity, &a.activityRaw, ev)
	case event.FileEdited:
		out = append(out, m.fileEdited(a, ev, now, at)...)
	case event.TokensUpdated:
		// Accounted above for every event kind.
	case event.ThinkingEnded:
		// Labels past activity only; must never change a stall
		// threshold (§2.5).
		if ev.Detail != "" {
			a.activity, a.activityRaw = ev.Detail, ""
		}
	case event.ModelObserved:
		if ev.Model != "" {
			a.model = ev.Model
		}
		if ev.Effort != "" {
			a.effort = ev.Effort
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
			a.activity, a.activityRaw = ev.Detail, ""
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
	setActivity(&a.activity, &a.activityRaw, ev)
	a.open = &OpenCall{Tool: ev.Tool, Target: ev.Target, RawTarget: ev.RawTarget, Since: at}
	a.addCall(at)
	out = append(out, m.derived(event.CallOpened, a.id, now, ev.Tool, ev.Target, ""))
	if _, failed := a.failedCmds[cmdKey(ev.Tool, ev.TargetRaw())]; failed {
		a.retries++
		out = append(out, m.derived(event.RetryInferred, a.id, now, ev.Tool, ev.Target, fmt.Sprintf("retry %d (inferred)", a.retries)))
	}
	return out
}

func (m *Machine) toolEnd(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	a.station = max(a.station, 1)
	tool, target, raw := ev.Tool, ev.Target, ev.RawTarget
	if a.open != nil && (ev.Tool == "" || ev.Tool == a.open.Tool) {
		if tool == "" {
			tool = a.open.Tool
		}
		if target == "" {
			target, raw = a.open.Target, a.open.RawTarget
		}
		a.open = nil
		out = append(out, m.derived(event.CallClosed, a.id, now, tool, target, ""))
	}
	failed := ev.Err != "" || (ev.ExitCode != nil && *ev.ExitCode != 0)
	a.appendCall(Call{Tool: tool, Target: target, RawTarget: raw,
		Says: strings.TrimSpace(ev.Says), Err: failed, Meta: callMeta(ev), At: at})
	// Retry inference and the verify heuristic both read the EXACT command:
	// flattening caps display text, and two long commands sharing a prefix must
	// not look like one command run twice.
	exact := rawOr(target, raw)
	key := cmdKey(tool, exact)
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
	out = append(out, m.observeVerify(a, tool, exact, ev.Says, failed, now)...)
	a.currentTool, a.currentTarget = tool, target
	setActivity(&a.activity, &a.activityRaw, ev)
	a.addCall(at)
	return out
}

// observeVerify applies the §13.3 Q6 split to a call that has just COMPLETED:
// a zero exit sets VerifyOK, a non-zero one VerifyFailed, and a call that is
// still open reaches here at all — toolEnd is the only caller, so an open
// command can never assert either.
//
// The latest outcome wins rather than latching the first: an agent whose suite
// passed and then broke is not "verified", and one that fixed a failing suite
// is. Only a transition emits VerifyObserved, so a row re-running a green suite
// does not narrate the same fact every 30 seconds. There is deliberately no
// derived kind for the failing direction: VerifyObserved renders as
// "✓ verified" in the log, the failing ToolEnd is already logged with its exit
// code, and inventing a second inference to describe it would say nothing the
// row does not.
func (m *Machine) observeVerify(a *agentState, tool, target, says string, failed bool, now time.Time) []event.Event {
	if tool != "Bash" || !isVerifyCommand(target, says) {
		return nil
	}
	want := VerifyOK
	if failed {
		want = VerifyFailed
	}
	if a.verify == want {
		return nil
	}
	a.verify = want
	if want == VerifyFailed {
		m.bumpRev()
		return nil
	}
	return []event.Event{m.derived(event.VerifyObserved, a.id, now, tool, target,
		"verify pattern matched (inferred)")}
}

// isVerifyCommand reports whether a completed Bash call was a verification: the
// command text says so, or the tool's own description says it was RUNNING one
// (§13.3 Q6 — see verifySaysRe on why the verb matters).
func isVerifyCommand(target, says string) bool {
	return verifyRe.MatchString(target) || verifySaysRe.MatchString(says)
}

func (m *Machine) fileEdited(a *agentState, ev event.Event, now, at time.Time) []event.Event {
	var out []event.Event
	a.station = max(a.station, 2)
	a.linesAdd += ev.LinesAdd
	a.linesDel += ev.LinesDel
	setActivity(&a.activity, &a.activityRaw, ev)
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
			setActivity(&m.main.activity, nil, ev)
		}
	case event.FileRead:
		setActivity(&m.main.activity, nil, ev)
	case event.FileEdited:
		setActivity(&m.main.activity, nil, ev)
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
	case event.ModelObserved:
		if ev.Model != "" {
			m.main.model = ev.Model
		}
		if ev.Effort != "" {
			m.main.effort = ev.Effort
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
	// §3.7.1 wants the EXACT command in the band, and `y` copies it to answer
	// the ask elsewhere — so the ask keeps the pre-flatten bytes even though
	// the row that shows it is flattened (§13.1). askKey normalises whitespace
	// anyway, so dedupe is unaffected either way.
	cmd := ev.TargetRaw()
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

// checkRunEnd settles the run once the turn that started it is over and every
// member has landed.
//
// "The turn is over" has two spellings and they are not the same claim. The
// registry saying idle means the CLI is waiting for a human. The transcript's
// turn end means main stopped working, which is all a run needs: a background
// job ends turn after turn without a human anywhere near it, and gating on
// idleness alone left those runs open forever.
func (m *Machine) checkRunEnd(now time.Time) []event.Event {
	if m.run == nil || (m.sessionState != SessionIdle && !m.turnEnded) {
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
		if a == nil || a.folded || m.recordedNothing(a) {
			// A run summary is a per-agent bar chart; an agent with no name
			// and no observed work has no bar to draw, and forty of them
			// would relocate the phantom-row problem into the run history.
			// Evidence, not the display grace: a finished run is judged on what
			// its agents did, not on how recently they were announced.
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
		if m.cfg.WatchNeverExempt && IsWatchCommand(cmd) {
			return m.cfg.StuckAfter
		}
		if m.allowRe != nil && m.allowRe.MatchString(cmd) {
			return m.cfg.LongRunningAfter
		}
	}
	return m.cfg.StuckAfter
}

// IsWatchCommand reports whether a command is watch-shaped: --watch, nodemon,
// tail -f, or a bare -w flag. It is exported because the prose in
// internal/narrate needs the SAME test the stall threshold uses — a sentence
// that calls a command a watcher and a machine that does not agree would be two
// readings of one string, and the reader would only ever see one of them.
func IsWatchCommand(cmd string) bool {
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
		ID:              a.id,
		Name:            a.name,
		NameExact:       a.nameExact,
		Type:            a.typ,
		Model:           a.model,
		Effort:          a.effort,
		Teammate:        a.teammate,
		Status:          a.status,
		Station:         a.station,
		Retries:         a.retries,
		Verify:          a.verify,
		Verified:        a.verify == VerifyOK,
		Decayed:         a.decayed,
		SpawnIndex:      a.spawnIndex,
		SpawnedAt:       a.spawnedAt,
		LastEventAt:     a.lastEventAt,
		DoneAt:          a.doneAt,
		Tokens:          a.tokens,
		LinesAdd:        a.linesAdd,
		LinesDel:        a.linesDel,
		CurrentTool:     a.currentTool,
		CurrentTarget:   a.currentTarget,
		ActivityLine:    a.activity,
		RawActivityLine: a.activityRaw,
		OpenFile:        a.openFile,
		LastError:       a.lastError,
		LastMessage:     a.lastMessage,
		TotalCalls:      a.totalCalls,
		EditedFiles:     sortedKeys(a.editedFiles),
		Rev:             a.rev,
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
	// The lifetime series ends where the agent does: a settled agent's chart must
	// not grow a tail of silence it was not alive for.
	histEnd := m.now
	if a.status == StatusDone && !a.doneAt.IsZero() {
		histEnd = a.doneAt
	}
	out.HistoryBuckets, out.HistoryFrom, out.HistoryStep = a.histSnapshot(histEnd)
	return out
}

// addTokens and addCall are the ONLY two writers of activity. Both series are
// fed from one place so the row's rolling window and the inspector's lifetime
// chart can never be counting different events — two independent accumulators
// over the same stream is exactly how the two views would come to disagree.
func (a *agentState) addTokens(at time.Time, n int) {
	a.bucket(at).Tokens += n
	a.histAdd(at, SparkBucket{Tokens: n})
}

func (a *agentState) addCall(at time.Time) {
	a.bucket(at).Calls++
	a.histAdd(at, SparkBucket{Calls: 1})
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

// The lifetime activity series (§13.3 Q1).
//
// SparkBuckets is a strictly rolling 60-second window, so before this existed the
// only history anywhere in the process was the trailing minute and the inspector's
// "whole agent lifetime" chart could not be drawn — it drew the tail and said so,
// which was honest but did not meet the ruling.
//
// Two properties matter, and they pull against each other:
//
//   - It must cover the WHOLE run. A ring of 10s buckets would clip a long run
//     back into a tail, which is the failure being fixed.
//   - It must be bounded. An unbounded slice is a leak measured in run length,
//     and agentpane is a monitor that stays open all day.
//
// Both hold by halving the resolution instead of dropping the oldest bucket: at
// histCap the series folds pairs and doubles histStep, so 180 buckets covers 30
// minutes at 10s, an hour at 20s, and any run at some power-of-two multiple of
// 10s. The cost is 180 × 16 bytes ≈ 2.9 KB per agent, flat, whatever the run
// does. What is lost is resolution, never span — and the step travels with the
// data (Agent.HistoryStep) so the chart can name it rather than implying 10s.
const (
	// histStepSeconds is the §13.3 Q1 granularity the series starts at.
	histStepSeconds = 10
	// histCap bounds the series. It is the whole memory story: no agent's
	// history exceeds this many buckets, so a run cannot grow one.
	histCap = 180
)

// histFloor snaps an instant down to a step boundary in absolute time, so bucket
// edges depend on the clock and not on which event happened to arrive first — a
// replay of the same events lands in the same buckets.
func histFloor(t time.Time, step int) time.Time {
	s := int64(step)
	if s <= 0 {
		s = histStepSeconds
	}
	sec := t.Unix()
	return time.Unix(sec-((sec%s)+s)%s, 0).UTC()
}

// histCoarsen folds a series pairwise and doubles its step. Bucket 0 keeps its
// start instant, so histFrom never moves and no sample changes which side of the
// series it is on.
func histCoarsen(in []SparkBucket, step int) ([]SparkBucket, int) {
	n := (len(in) + 1) / 2
	for i := 0; i < n; i++ {
		b := in[2*i]
		if j := 2*i + 1; j < len(in) {
			b.Tokens += in[j].Tokens
			b.Calls += in[j].Calls
		}
		in[i] = b
	}
	return in[:n], step * 2
}

func (a *agentState) histIndex(at time.Time) int {
	return int(at.Sub(a.histFrom) / (time.Duration(a.histStep) * time.Second))
}

// histAdd files one delta into the lifetime series.
//
// The series starts at the agent's SPAWN when that is known, not at its first
// event: the gap between the two is measured silence and belongs on the chart,
// whereas the time before the spawn is not the agent's at all and padding it would
// fabricate quiet the agent never had.
func (a *agentState) histAdd(at time.Time, d SparkBucket) {
	if at.IsZero() || (d.Tokens == 0 && d.Calls == 0) {
		return
	}
	if a.histStep <= 0 {
		a.histStep = histStepSeconds
	}
	if len(a.hist) == 0 {
		start := at
		if !a.spawnedAt.IsZero() && a.spawnedAt.Before(start) {
			start = a.spawnedAt
		}
		a.histFrom = histFloor(start, a.histStep)
		a.hist = append(a.hist, SparkBucket{})
	}
	// An event older than the series start. The machine tolerates out-of-order
	// arrival everywhere else, so the sample is filed where it belongs rather
	// than folded into bucket 0 — but the extension is bounded like everything
	// else here, and a timestamp further back than the series could ever hold at
	// any resolution is dropped rather than allowed to define the horizon.
	if i := a.histIndex(at); i < 0 {
		n := -i
		if n > histCap {
			return
		}
		a.hist = append(make([]SparkBucket, n, n+len(a.hist)), a.hist...)
		a.histFrom = a.histFrom.Add(-time.Duration(n*a.histStep) * time.Second)
		for len(a.hist) > histCap {
			a.hist, a.histStep = histCoarsen(a.hist, a.histStep)
		}
	}
	for {
		i := a.histIndex(at)
		if i < len(a.hist) {
			a.hist[i].Tokens += d.Tokens
			a.hist[i].Calls += d.Calls
			return
		}
		if len(a.hist) < histCap {
			a.hist = append(a.hist, SparkBucket{})
			continue
		}
		a.hist, a.histStep = histCoarsen(a.hist, a.histStep)
	}
}

// histSnapshot copies the series out and extends it to the agent's current edge.
//
// The trailing extension is the mirror of the leading one: an agent that has done
// nothing for two minutes has two minutes of measured silence inside its lifetime,
// and a chart that stopped at the last event would show a run shorter than the
// clock on the row. It is computed here rather than written on every Tick so the
// machine does no work for an idle agent, and it coarsens under the same cap, so
// a long silence cannot grow the snapshot either.
func (a *agentState) histSnapshot(end time.Time) ([]SparkBucket, time.Time, time.Duration) {
	if len(a.hist) == 0 {
		return nil, time.Time{}, 0
	}
	step := a.histStep
	if step <= 0 {
		step = histStepSeconds
	}
	out := append(make([]SparkBucket, 0, len(a.hist)+8), a.hist...)
	if !end.IsZero() && end.After(a.histFrom) {
		for {
			want := int(end.Sub(a.histFrom)/(time.Duration(step)*time.Second)) + 1
			if want <= len(out) {
				break
			}
			if len(out) >= histCap {
				out, step = histCoarsen(out, step)
				continue
			}
			out = append(out, SparkBucket{})
		}
	}
	return out, a.histFrom, time.Duration(step) * time.Second
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
	a.appendCall(Call{Tool: tool, Target: ev.Target, RawTarget: ev.RawTarget, Meta: meta, At: at})
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

// setActivity records what an agent is doing. The TOOL and its target win
// over Detail: PostToolUse carries duration_ms in Detail, so preferring it
// rendered rows as a bare "144ms" — a fact about the last call, not a
// description of the work. Detail is the right answer only when there is no
// tool to name (AgentReturned's final message, a TodoWrite's current item).
//
// raw may be nil. When it is not, it receives the pre-flatten form of the same
// line — set only when it differs, so `y` can yank the command the tool really
// ran while the row keeps the one-line version (§13.1).
func setActivity(dst, raw *string, ev event.Event) {
	set := func(display, original string) {
		*dst = display
		if raw == nil {
			return
		}
		if original == display {
			original = ""
		}
		*raw = original
	}
	// The tool's own human summary wins: "Hold an open 45s foreground call on
	// main" beats `end=$(( $(date +%s) + 45 )); until [ "$(date +%…` truncated
	// mid-expression. Target still carries the command, so yanking,
	// contention and the open-file tracker are unaffected.
	if s := strings.TrimSpace(ev.Says); s != "" {
		set(s, s)
		return
	}
	if s := strings.TrimSpace(ev.Tool + " " + ev.Target); s != "" {
		set(s, strings.TrimSpace(ev.Tool+" "+ev.TargetRaw()))
		return
	}
	if ev.Detail != "" {
		set(ev.Detail, ev.Detail)
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

// rawOr is the exact form of a command: the pre-flatten bytes when they were
// kept, the flattened text otherwise.
func rawOr(target, raw string) string {
	if raw != "" {
		return raw
	}
	return target
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
