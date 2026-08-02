// Package state implements the agentpane domain state machine (SPEC rev 3.1
// PART 2): it consumes event.Event values, derives the events the platform
// cannot emit, and exposes an ordered, copy-safe snapshot for the UI.
package state

import (
	"encoding/json"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// Status is the complete agent status set (§2.3). There is no failed and no
// killed: the platform emits neither signal.
type Status uint8

const (
	StatusQueued Status = iota
	StatusRun
	StatusAsk
	StatusStuck
	StatusDone
	StatusTeammate
	StatusUnknown
)

func (s Status) String() string {
	switch s {
	case StatusQueued:
		return "queued"
	case StatusRun:
		return "run"
	case StatusAsk:
		return "ask"
	case StatusStuck:
		return "stuck"
	case StatusDone:
		return "done"
	case StatusTeammate:
		return "teammate"
	default:
		return "unknown"
	}
}

// Rank is the §3.6 render order: ask, stuck, run, queued, done, teammate,
// unknown.
func (s Status) Rank() int {
	switch s {
	case StatusAsk:
		return 0
	case StatusStuck:
		return 1
	case StatusRun:
		return 2
	case StatusQueued:
		return 3
	case StatusDone:
		return 4
	case StatusTeammate:
		return 5
	default:
		return 6
	}
}

func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

func (s *Status) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch v {
	case "queued":
		*s = StatusQueued
	case "run":
		*s = StatusRun
	case "ask":
		*s = StatusAsk
	case "stuck":
		*s = StatusStuck
	case "done":
		*s = StatusDone
	case "teammate":
		*s = StatusTeammate
	default:
		*s = StatusUnknown
	}
	return nil
}

// SessionState is the session lifecycle for the header (§2.10, §3.9).
type SessionState uint8

const (
	SessionActive SessionState = iota
	SessionIdle
	SessionEnded
	SessionCompacting
)

func (s SessionState) String() string {
	switch s {
	case SessionActive:
		return "active"
	case SessionIdle:
		return "idle"
	case SessionEnded:
		return "ended"
	default:
		return "compacting"
	}
}

// World is the machine's snapshot for the UI. Every slice and pointer is a
// copy: mutating a World never touches machine state.
type World struct {
	Session Session
	Main    Main
	// Agents is already in §3.6 render order (ask, stuck, run, queued,
	// done, teammate, unknown; stable by spawn order within each group).
	// Folded retry ghosts never appear here (§3.7.7), and neither do agents
	// that have earned no row (Quiet).
	Agents []Agent
	// Quiet lists the agents the platform announced but never showed doing
	// anything: no name or description, no tool call, no tokens, no file, no
	// message — see Machine.quiet. Claude Code emits SubagentStart/Stop pairs
	// for entities that do no work and write no transcript (a 5-subagent
	// session was observed producing 40 of them), and rendering a row each was
	// technically honest and practically useless. They stay in the model and
	// are counted here so the UI can say how many exist and name them on
	// demand: suppressed, never silently dropped (§1.5, §3.9).
	Quiet []QuietAgent
	// Asks are the deduped alert-band entries, oldest first (§3.7).
	Asks        []Ask
	Contentions []Contention
	// Run is the active run aggregate, nil when no run is in flight.
	Run *Run
	// LastRun is the most recently completed run (persistence is
	// internal/runstore's job).
	LastRun       *Run
	Connected     bool
	DroppedEvents int
	// Rev increases on every visible state change; the UI diffs per-agent
	// Rev against it for the away-digest change marks (§3.19).
	Rev uint64
}

type Session struct {
	ID string
	// ShortID is the attached short id shown in the header (§3.8a).
	ShortID string
	State   SessionState
}

// Main is the root agent. It has a real todo list instead of pips (§2.2);
// when Todo is empty the UI falls back to Activity, never fake pips.
type Main struct {
	// Title is Claude Code's own short session title (event.SessionTitled,
	// transcript-only). It is the workstream label on main's row — the UI
	// prefers it over the raw run prompt, which is whole sentences long.
	// Empty until a title line is seen; agentpane never invents one.
	Title       string
	Todo        string
	Activity    string
	Tokens      int
	LastEventAt time.Time
	Rev         uint64
}

// Agent is one subagent row (§2.9, rev 3.1).
type Agent struct {
	ID   string
	Name string // the spawn description; the UI slugs it (§3.16)
	// NameExact marks Name as coming from an exact id↔description join rather
	// than a hooks correlation, so the UI may replace a guessed slug with it.
	NameExact bool
	Type      string // subagent_type
	Teammate  bool
	Status    Status
	// Station is the highest observed station, monotonic (§2.2):
	// 0 spawn, 1 first tool, 2 first edit, 3 return.
	Station int
	Retries int // inferred (§2.3): dim ↺n, never a status
	// Verified is the inferred test/build/lint marker (§2.2), never a pip.
	Verified bool
	// Decayed marks a done agent past decay_after (§3.5): display
	// collapse only, not a status.
	Decayed    bool
	SpawnIndex int
	SpawnedAt  time.Time
	// LastEventAt drives the liveness clock (§2.6).
	LastEventAt   time.Time
	DoneAt        time.Time // zero until done
	Tokens        int
	LinesAdd      int
	LinesDel      int
	CurrentTool   string
	CurrentTarget string
	ActivityLine  string
	OpenFile      string // last Read/Edit target
	LastError     string
	// LastMessage is the agent's final platform text; the UI may colour it
	// but never adds a verdict (§2.3).
	LastMessage string
	// OpenCall is non-nil while a tool call is open: the UI renders
	// "◐ in <Tool>" + time in call instead of a sparkline (§2.6a).
	OpenCall *OpenCall
	// CallHistory holds the newest calls, oldest first, capped at 20
	// (§3.17); TotalCalls counts every recorded call for "+n more".
	CallHistory []Call
	TotalCalls  int
	// SparkBuckets are 60 one-second buckets ending at SparkAt, holding
	// both metrics so spark can switch without losing history (§2.9,
	// §3.4). Index 59 is the SparkAt second.
	SparkBuckets [60]SparkBucket
	SparkAt      time.Time
	EditedFiles  []string
	// Ask is a copy of the pending permission payload, nil when not
	// asking.
	Ask *event.AskPayload
	// Rev is the machine revision when this agent last changed (§3.19).
	Rev uint64
}

// QuietAgent is one suppressed row's honest residue: everything that is
// actually known about an agent that showed nothing. Type is its agent_type
// when the platform reported one; there is no name, by definition.
type QuietAgent struct {
	ID     string
	Type   string
	Status Status
}

// OpenCall is the §2.6a open-call state.
type OpenCall struct {
	Tool   string
	Target string
	Since  time.Time
}

// Call is one glyph-ready call-history entry (§3.3 lines 4-7).
type Call struct {
	Tool   string
	Target string
	Err    bool
	Meta   string // e.g. "+29 −4", "err", "exit 2"
	At     time.Time
}

// SparkBucket holds both sparkline metrics for one second (§3.4).
type SparkBucket struct {
	Tokens int
	Calls  int
}

// Ask is one deduped alert-band entry (§3.7).
type Ask struct {
	Key         string // tool + normalised input, the dedupe key (§3.7.7)
	AgentID     string // the original asker
	Description string // slug source (the asker's description)
	Tool        string
	Command     string // the exact command, unnormalised (§3.7.1)
	Kind        string // ask payload kind: command|file_write|network|mcp|other
	RaisedAt    time.Time
	// DupeCount is the total PermissionRequested events matched to this
	// ask ("×n"), starting at 1.
	DupeCount int
	// FoldedGhosts counts distinct retry agents folded into this entry.
	FoldedGhosts int
	// Resolving means a resolution signal was seen but no outcome
	// confirmed yet (§3.7.8): render "resolving…", never a verdict.
	Resolving bool
}

// Contention is one §2.7 row. Newest first in World.Contentions.
type Contention struct {
	Path       string
	AgentIDs   []string
	AgentNames []string
	DetectedAt time.Time
}

// Run is the §2.8 aggregate. It is attached as JSON to the RunEnded derived
// event's Detail and exposed as World.LastRun; internal/runstore owns disk.
type Run struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     time.Time  `json:"ended_at"`
	Prompt      string     `json:"prompt,omitempty"`
	Agents      []RunAgent `json:"agents"`
	Tokens      int        `json:"tokens"`
	Stalls      int        `json:"stalls"`
	Contentions int        `json:"contentions"`
}

type RunAgent struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Status   Status        `json:"status"`
	Duration time.Duration `json:"duration"`
	Tokens   int           `json:"tokens"`
}
