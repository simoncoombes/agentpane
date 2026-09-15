// Package event defines the taxonomy every agentpane source emits and the
// Source interface the UI consumes them through (SPEC.md rev 3.1 §1.2, §1.3).
// Nothing outside this taxonomy may reach the state machine.
package event

import (
	"context"
	"encoding/json"
	"strings"
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
	// Target is the file path, command or pattern, FLATTENED on ingest
	// (§13.1: one line, whitespace runs collapsed, control characters gone).
	// Nothing downstream may assume it is byte-identical to what the tool ran —
	// RawTarget is.
	Target string
	// RawTarget is Target before flattening: the exact bytes the tool ran. It
	// is set only when flattening changed something, so TargetRaw() is the
	// accessor to use. `y` yanks this, never the flattened form (§13.1): a
	// python3 -c one-liner with its newlines turned into spaces is no longer a
	// command you can paste.
	RawTarget string
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
	// Model and Effort are what the platform ran this agent WITH, read off the
	// transcript's assistant lines (`message.model`, and the session's
	// `effort`). No hook carries either. Both are verbatim platform strings —
	// agentpane never maps a model id onto a friendlier name it made up (C8).
	Model    string
	Effort   string
	LinesAdd int
	LinesDel int
	ExitCode *int            // nil unless a process ended with a known code
	Err      string          // error text, empty if none
	Ask      *AskPayload     // non-nil only for PermissionRequested
	Raw      json.RawMessage // original payload for the debug ring
}

// TargetRaw is the exact text the tool ran: RawTarget when flattening changed
// Target, Target itself otherwise. Every yank, every clipboard path and
// anything that re-runs a command must use this, not Target.
func (e Event) TargetRaw() string {
	if e.RawTarget != "" {
		return e.RawTarget
	}
	return e.Target
}

// flattenMax caps flattened text in runes. Rows are at most ~200 columns and
// the inspector shows one line per call, so anything past this can only be
// stored, never shown — and a megabyte heredoc kept per call in the ring is a
// leak, not information. The original is still reachable through TargetRaw.
const flattenMax = 240

// FlattenText is the §13.1 ingest flattener: runs of whitespace (newlines and
// tabs included) collapse to one space, control characters are dropped, the
// result is trimmed, and only then is it truncated to flattenMax runes.
//
// Truncating last is the whole point of the order: a newline has zero display
// width, so cutting first counts characters the terminal will never show and
// throws away the text that would have been visible.
func FlattenText(s string) string {
	if flat(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false // pending separator, written only before real content
	runes := 0
	for _, r := range s {
		switch {
		case r == 0x7f, r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\v' && r != '\f':
			continue // control characters carry no meaning and move the cursor
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f':
			space = b.Len() > 0
			continue
		}
		if space {
			if runes >= flattenMax {
				return b.String() + "…"
			}
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes >= flattenMax {
			return b.String() + "…"
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

// flat reports whether s already satisfies FlattenText, so the common path
// allocates nothing.
func flat(s string) bool {
	prevSpace := true // a leading space would be trimmed
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
		if r == ' ' {
			if prevSpace {
				return false
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		n++
		if n > flattenMax {
			return false
		}
	}
	return !prevSpace || s == ""
}

// Flattened returns ev with its display text flattened for the row, keeping the
// original command in RawTarget. Sources call it at ingest so no draw-time code
// has to defend the frame's geometry (§13.1: flatten on ingest, not at draw
// time); the state machine calls it again defensively, which is a no-op on
// already-flattened text.
//
// Says (the tool's own one-line description) and the human summary in Detail
// are display text and are flattened in place. Detail is NOT flattened where it
// carries verbatim content that something else owns: the user's prompt, the
// session title, an agent's final message and error text are quoted, wrapped or
// yanked elsewhere, and collapsing a paragraph into one long line there would
// destroy the only copy.
func Flattened(ev Event) Event {
	if t := FlattenText(ev.Target); t != ev.Target {
		if ev.RawTarget == "" {
			ev.RawTarget = ev.Target
		}
		ev.Target = t
	}
	ev.Says = FlattenText(ev.Says)
	if flattenDetail(ev.Kind) {
		ev.Detail = FlattenText(ev.Detail)
	}
	if ev.Ask != nil && ev.Ask.Detail != "" {
		cp := *ev.Ask
		cp.Detail = FlattenText(cp.Detail)
		ev.Ask = &cp
	}
	return ev
}

// flattenDetail reports whether this kind's Detail is display text the pane
// owns (true) or verbatim content it is merely carrying (false).
func flattenDetail(k Kind) bool {
	switch k {
	case UserPromptSubmitted, SessionTitled, AgentReturned, AgentMerged, ErrorRaised:
		return false
	}
	return true
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
	// TurnEnded is the transcript's `{"type":"system","subtype":"turn_duration"}`
	// line: the main agent finished a turn. It is NOT SessionIdle. A turn
	// ending means the run is over, which is why it settles one; whether the
	// SESSION is now waiting for a human is a different question, and the
	// sessions registry answers it (DATA-SOURCES §2). A background job runs
	// many turns without a human anywhere near it.
	TurnEnded
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
	// ModelObserved reports the model an agent is running on, and the effort
	// the session is set to, the first time a transcript line names them and
	// again whenever they change (a `/model` mid-session, a subagent spawned
	// onto a different one). Transcript-only: the hook payloads carry neither.
	ModelObserved

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
	TurnEnded:           "TurnEnded",
	UserPromptSubmitted: "UserPromptSubmitted",
	CompactStarted:      "CompactStarted",
	CompactFinished:     "CompactFinished",
	SessionTitled:       "SessionTitled",
	ModelObserved:       "ModelObserved",
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
