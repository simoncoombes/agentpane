// Package event defines the taxonomy every agentpane source emits and the
// Source interface the UI consumes them through (SPEC.md rev 3.1 §1.2, §1.3).
// Nothing outside this taxonomy may reach the state machine.
package event

import (
	"context"
	"encoding/json"
	"time"
)

// Source is the single acquisition interface. On transport failure it must
// emit SourceDisconnected and keep retrying with backoff, emitting
// SourceConnected on recovery. It never closes the channel on recoverable
// errors.
type Source interface {
	Events(ctx context.Context) (<-chan Event, error)
	Name() string
}

type Event struct {
	Seq       uint64    // monotonic, assigned on ingest
	At        time.Time // source timestamp; ingest time if absent
	SessionID string
	AgentID   string // stable id; "main" for the root agent
	AgentName string // display name source: the spawn description (UI slugs it)
	// NameExact marks AgentName as coming from an EXACT id↔description join
	// (the transcript sidecar, which holds both in one file) rather than a
	// hooks correlation, where the description and the agent id arrive on
	// different payloads and must be matched heuristically. The state machine
	// lets an exact name correct a correlated one; without this, a single
	// mis-correlation would be frozen for the whole run.
	NameExact bool
	AgentType string // e.g. "general-purpose"; empty when unknown
	Kind      Kind
	Tool      string // "Bash" | "Edit" | "Read" | "Grep" | "Agent" | ...
	Target    string // file path, command, or pattern (raw; UI shortens)
	// Says is the tool's own human summary of the call (the Bash tool's
	// `description`: "Hold an open 45s foreground call on main"). Target keeps
	// the raw command, because contention, open-file tracking and yanking all
	// need the real thing; Says is what a row should SHOW when the platform
	// bothered to write one.
	Says string
	// Duration is how long a finished call took. Deliberately not Detail:
	// putting it there once made rows describe their work as "144ms".
	Duration  time.Duration
	Detail    string // one-line human summary
	Tokens    int    // cumulative for this agent if known, else delta
	TokensCum bool   // true when Tokens is cumulative
	LinesAdd  int
	LinesDel  int
	ExitCode  *int            // nil unless a process ended with a known code
	Err       string          // error text, empty if none
	Ask       *AskPayload     // non-nil only for PermissionRequested
	Raw       json.RawMessage // original payload for the debug ring
}

type AskPayload struct {
	Kind    string   // "command" | "file_write" | "network" | "mcp" | "other"
	Command string   // exact command or path being requested
	Options []string // e.g. ["once","always","no"] if known
	Detail  string   // one-line "asked to …" summary when the source has one
}

type Kind uint8

const (
	KindInvalid Kind = iota

	// Session / transport.
	SourceConnected
	SourceDisconnected
	SessionStart
	SessionEnd
	SessionIdle
	UserPromptSubmitted
	CompactStarted
	CompactFinished
	// SessionTitled carries Claude Code's OWN short session title in Detail
	// (transcript `{"type":"ai-title","aiTitle":…}`, DATA-SOURCES §6). It
	// exists so main's row can show a short workstream label instead of the
	// user's entire prompt, without agentpane inventing a summary (C8): the
	// platform already writes one, and it is rewritten as the session
	// evolves, so the last one seen wins. Transcript-only — no hook carries
	// it. AgentID is always MainAgentID: the title names the session, not a
	// subagent.
	SessionTitled

	// Agent lifecycle. AgentFailed/AgentKilled do not exist: the platform
	// emits no failure or kill signal (SPEC §1.3, DATA-SOURCES §6).
	AgentSpawned
	AgentStarted
	AgentReturned
	AgentMerged

	// Tool activity. ToolOutput exists only for background tasks whose
	// output file grows; foreground commands emit nothing between
	// ToolStart and ToolEnd (SPEC §2.6a).
	ToolStart
	ToolEnd
	ToolOutput
	FileRead
	FileEdited
	TokensUpdated
	ThinkingEnded

	// Attention.
	PermissionRequested
	PermissionGranted
	PermissionDenied
	NotificationRaised
	ErrorRaised

	// Derived internally by the state machine — never from a Source.
	// Everything below must be visually marked as inferred (SPEC §3.14).
	StallDetected
	StallCleared
	ContentionDetected
	ContentionCleared
	RunStarted
	RunEnded
	AgentDecayed
	VerifyObserved
	RetryInferred
	CallOpened
	CallClosed
	AskDeduped
	GhostAgentFolded
)

var kindNames = map[Kind]string{
	KindInvalid:         "Invalid",
	SourceConnected:     "SourceConnected",
	SourceDisconnected:  "SourceDisconnected",
	SessionStart:        "SessionStart",
	SessionEnd:          "SessionEnd",
	SessionIdle:         "SessionIdle",
	UserPromptSubmitted: "UserPromptSubmitted",
	CompactStarted:      "CompactStarted",
	CompactFinished:     "CompactFinished",
	SessionTitled:       "SessionTitled",
	AgentSpawned:        "AgentSpawned",
	AgentStarted:        "AgentStarted",
	AgentReturned:       "AgentReturned",
	AgentMerged:         "AgentMerged",
	ToolStart:           "ToolStart",
	ToolEnd:             "ToolEnd",
	ToolOutput:          "ToolOutput",
	FileRead:            "FileRead",
	FileEdited:          "FileEdited",
	TokensUpdated:       "TokensUpdated",
	ThinkingEnded:       "ThinkingEnded",
	PermissionRequested: "PermissionRequested",
	PermissionGranted:   "PermissionGranted",
	PermissionDenied:    "PermissionDenied",
	NotificationRaised:  "NotificationRaised",
	ErrorRaised:         "ErrorRaised",
	StallDetected:       "StallDetected",
	StallCleared:        "StallCleared",
	ContentionDetected:  "ContentionDetected",
	ContentionCleared:   "ContentionCleared",
	RunStarted:          "RunStarted",
	RunEnded:            "RunEnded",
	AgentDecayed:        "AgentDecayed",
	VerifyObserved:      "VerifyObserved",
	RetryInferred:       "RetryInferred",
	CallOpened:          "CallOpened",
	CallClosed:          "CallClosed",
	AskDeduped:          "AskDeduped",
	GhostAgentFolded:    "GhostAgentFolded",
}

func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return "Invalid"
}

// Derived reports whether k is produced by the state machine rather than a
// Source. A Source emitting a derived kind is a bug; the state machine drops
// such events and counts them in the debug ring.
func (k Kind) Derived() bool { return k >= StallDetected }

// MainAgentID is the AgentID of the root agent.
const MainAgentID = "main"
