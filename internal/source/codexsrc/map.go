// Package codexsrc reads OpenAI Codex CLI sessions and maps them onto the
// agentpane event vocabulary.
//
// Codex is a second platform with the same shape as the first: a root agent
// that fans out to subagents, each doing tool calls and spending tokens. What
// differs is entirely in how it says so, and that difference is this package —
// everything above event.Source (the state machine, the tree, the inspector, the
// animation) already knows nothing about Claude Code.
//
// # Where the data is
//
// One JSONL "rollout" file per thread, under ~/.codex/sessions/<yyyy>/<mm>/<dd>/
// rollout-<timestamp>-<thread-id>.jsonl. A subagent is a thread like any other,
// and its first line names its parent — so the whole tree is on disk, in files
// this package can tail, with no database and no new dependency:
//
//	session_meta   thread identity: id, session_id (the ROOT thread, shared by
//	               every thread of one run), parent_thread_id, thread_source
//	               ("user" | "subagent"), agent_nickname, agent_path, cwd
//	turn_context   model, cwd, approval and sandbox policy, per turn
//	response_item  custom_tool_call / custom_tool_call_output — the work
//	event_msg      task_started, task_complete (with the agent's last message)
//	token_usage_record  usage, split input/cached/output per turn
//
// # What is NOT here
//
// Codex has a hook system whose event names and payload fields are nearly
// identical to Claude Code's (PreToolUse, SubagentStart, agent_id, tool_input…),
// which would be a faster and more precise source than tailing files. It needs
// hooks.json configured on the Codex side and a trust step, so it is a second
// source rather than a replacement for this one — the same relationship the
// hook and transcript sources already have.
package codexsrc

import (
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// rawLine is one line of a rollout file.
type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// meta is the session_meta payload: who this thread is and where it came from.
type meta struct {
	SessionID      string `json:"session_id"` // the ROOT thread: one per run
	ID             string `json:"id"`         // this thread
	ParentThreadID string `json:"parent_thread_id"`
	ThreadSource   string `json:"thread_source"` // "user" | "subagent"
	Nickname       string `json:"agent_nickname"`
	AgentPath      string `json:"agent_path"`
	CWD            string `json:"cwd"`
	Originator     string `json:"originator"`
	CLIVersion     string `json:"cli_version"`
}

type turnContext struct {
	Model string `json:"model"`
}

// payloadKind is the discriminator inside event_msg and response_item.
type payloadKind struct {
	Type string `json:"type"`
}

type toolCall struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
	Status string `json:"status"`
}

type toolOutput struct {
	CallID string `json:"call_id"`
	// Output is whatever the tool returned — a string for a shell call, an
	// object for a structured tool. It is read raw and stringified, because a
	// stricter type here silently dropped every ToolEnd on the first real
	// session this package was pointed at.
	Output json.RawMessage `json:"output"`
}

// text renders an output payload for display: the string it is, or the JSON it
// is, and never an error.
func (o toolOutput) text() string {
	if len(o.Output) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(o.Output, &s) == nil {
		return s
	}
	return string(o.Output)
}

type taskComplete struct {
	LastAgentMessage string `json:"last_agent_message"`
	DurationMS       int64  `json:"duration_ms"`
}

type agentMessage struct {
	Author  string `json:"author"`
	Content string `json:"content"`
}

type usageRecord struct {
	ThreadID string `json:"thread_id"`
	Usage    struct {
		InputTokens      int `json:"input_tokens"`
		CachedInput      int `json:"cached_input_tokens"`
		CacheWriteTokens int `json:"cache_write_input_tokens"`
		OutputTokens     int `json:"output_tokens"`
	} `json:"usage"`
}

// work is what one turn cost: tokens read for the first time plus tokens
// written. cached_input_tokens is excluded for the same reason it is on the
// Claude Code side — it is the prompt re-read every turn, it dwarfs the rest,
// and it grows with the conversation rather than with anything the agent did.
func (u usageRecord) work() int {
	return u.Usage.InputTokens + u.Usage.OutputTokens + u.Usage.CacheWriteTokens
}

// threadState is what the mapper remembers about one rollout file.
type threadState struct {
	agentID string // event.MainAgentID for the root thread, else the thread id
	root    bool
	name    string
	model   string
	tokens  int
	started bool
	pending map[string]toolCall // call_id → the call still open
}

func newThreadState() *threadState {
	return &threadState{pending: map[string]toolCall{}}
}

// mapLine turns one rollout line into events. It is a pure function of the line
// and the thread's own accumulated state, so the tailer can call it with no
// knowledge of what a Codex line means.
func mapLine(st *threadState, raw []byte, seen time.Time) []event.Event {
	var ln rawLine
	if err := json.Unmarshal(raw, &ln); err != nil {
		return nil // a partial write at the tail; the next poll re-reads it
	}
	at := lineTime(ln.Timestamp, seen)

	switch ln.Type {
	case "session_meta":
		return metaEvents(st, ln.Payload, at)
	case "turn_context":
		var tc turnContext
		if json.Unmarshal(ln.Payload, &tc) == nil && tc.Model != "" && tc.Model != st.model {
			st.model = tc.Model
			return []event.Event{{AgentID: st.agentID, Kind: event.ModelObserved,
				At: at, Model: tc.Model}}
		}
	case "event_msg":
		return msgEvents(st, ln.Payload, at)
	case "response_item":
		return itemEvents(st, ln.Payload, at)
	case "token_usage_record":
		var u usageRecord
		if json.Unmarshal(ln.Payload, &u) != nil {
			return nil
		}
		if w := u.work(); w > 0 {
			st.tokens += w
			return []event.Event{{AgentID: st.agentID, Kind: event.TokensUpdated,
				At: at, Tokens: st.tokens, TokensCum: true}}
		}
	}
	return nil
}

func metaEvents(st *threadState, payload []byte, at time.Time) []event.Event {
	var m meta
	if json.Unmarshal(payload, &m) != nil || m.ID == "" {
		return nil
	}
	// The FIRST session_meta is the thread's identity; later ones are context.
	//
	// A subagent's rollout replays its parent's session_meta — the parent's id,
	// thread_source "user" and all — as part of the context it inherits. Taken
	// as identity that turns every subagent into the root halfway down its own
	// file, and its tokens and tool calls are then attributed to main. Identity
	// is claimed once.
	if st.agentID != "" {
		return nil
	}
	st.root = m.ThreadSource != "subagent" && (m.ParentThreadID == "" || m.ID == m.SessionID)
	if st.root {
		st.agentID = event.MainAgentID
		return []event.Event{{AgentID: event.MainAgentID, Kind: event.SessionStart,
			SessionID: m.SessionID, At: at, Detail: m.CWD}}
	}
	st.agentID = m.ID
	st.name = agentName(m)
	// NameExact: the spawn record and the description are the SAME line here, so
	// there is no correlation to get wrong (unlike the hooks path, where the two
	// arrive separately and are matched heuristically).
	return []event.Event{{
		AgentID: m.ID, AgentName: st.name, NameExact: true,
		AgentType: "subagent", Kind: event.AgentSpawned,
		SessionID: m.SessionID, At: at,
	}}
}

// agentName is what the row is called: the task the subagent was spawned for.
//
// Codex gives every subagent a nickname of its own — Popper, Lagrange, Hypatia —
// which is charming and says nothing about the work. agent_path is the task
// ("/root/current_date"), and its last segment is the closest thing to the
// description the Claude Code side slugs. The nickname is the fallback, because
// a row named "Popper" beats a row named nothing.
func agentName(m meta) string {
	if p := strings.TrimSpace(m.AgentPath); p != "" {
		if base := path.Base(p); base != "" && base != "/" && base != "." {
			return strings.ReplaceAll(base, "_", " ")
		}
	}
	return m.Nickname
}

func msgEvents(st *threadState, payload []byte, at time.Time) []event.Event {
	var kind payloadKind
	if json.Unmarshal(payload, &kind) != nil {
		return nil
	}
	switch kind.Type {
	case "task_started":
		if st.started {
			return nil
		}
		st.started = true
		return []event.Event{{AgentID: st.agentID, Kind: event.AgentStarted, At: at}}
	case "task_complete":
		var tc taskComplete
		_ = json.Unmarshal(payload, &tc) //nolint:errcheck // a missing message is not an error
		if st.root {
			// The root thread finishing a turn is the session going quiet, not
			// an agent returning: main does not return (§2.10).
			return []event.Event{{AgentID: event.MainAgentID, Kind: event.SessionIdle, At: at}}
		}
		return []event.Event{{AgentID: st.agentID, Kind: event.AgentReturned, At: at,
			Detail:   firstLine(tc.LastAgentMessage),
			Duration: time.Duration(tc.DurationMS) * time.Millisecond}}
	}
	return nil
}

func itemEvents(st *threadState, payload []byte, at time.Time) []event.Event {
	var kind payloadKind
	if json.Unmarshal(payload, &kind) != nil {
		return nil
	}
	switch kind.Type {
	case "custom_tool_call":
		var c toolCall
		if json.Unmarshal(payload, &c) != nil || c.Name == "" {
			return nil
		}
		st.pending[c.CallID] = c
		return []event.Event{{AgentID: st.agentID, Kind: event.ToolStart, At: at,
			Tool: toolName(c.Name), Target: toolTarget(c), RawTarget: c.Input}}
	case "custom_tool_call_output":
		var o toolOutput
		if json.Unmarshal(payload, &o) != nil {
			return nil
		}
		c, ok := st.pending[o.CallID]
		if !ok {
			return nil // an output with no call: nothing to attribute it to (C8)
		}
		delete(st.pending, o.CallID)
		ev := event.Event{AgentID: st.agentID, Kind: event.ToolEnd, At: at,
			Tool: toolName(c.Name), Target: toolTarget(c), RawTarget: c.Input}
		if out := o.text(); failed(out) {
			ev.Err = firstLine(out)
		}
		return []event.Event{ev}
	case "agent_message":
		var m agentMessage
		if json.Unmarshal(payload, &m) != nil || strings.TrimSpace(m.Content) == "" {
			return nil
		}
		// What the agent said, as its activity line: the same role the Claude
		// Code side gives a thinking block.
		return []event.Event{{AgentID: st.agentID, Kind: event.ThinkingEnded, At: at,
			Detail: firstLine(m.Content)}}
	}
	return nil
}

// toolName is the tool as a row shows it. Codex names its shell tool by the
// call shape rather than the command; "Bash" is what every other surface in the
// pane calls that, and a reader comparing two platforms should not have to
// learn two words for one thing.
func toolName(name string) string {
	switch name {
	case "shell", "local_shell", "exec_command", "unified_exec":
		return "Bash"
	case "apply_patch", "edit_file", "write_file":
		return "Edit"
	case "read_file", "view_file":
		return "Read"
	case "grep", "search":
		return "Grep"
	}
	return name
}

// toolTarget is what the call was ON: the command, the path, the pattern. Codex
// packs the arguments into a JSON string, so the useful field is dug out where
// its shape is known and the raw input is kept otherwise — never invented.
func toolTarget(c toolCall) string {
	var args map[string]any
	if json.Unmarshal([]byte(c.Input), &args) != nil {
		return firstLine(c.Input)
	}
	for _, key := range []string{"command", "cmd", "path", "file_path", "pattern", "query"} {
		switch v := args[key].(type) {
		case string:
			if v != "" {
				return firstLine(v)
			}
		case []any:
			var parts []string
			for _, p := range v {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return firstLine(c.Input)
}

// failed reads a tool output for a non-zero exit. Codex reports the exit code in
// the output text rather than as a field, so this looks for the shape it writes
// and claims nothing when it does not find it (C8).
func failed(output string) bool {
	head := output
	if len(head) > 200 {
		head = head[:200]
	}
	return strings.Contains(head, "exit code 1") ||
		strings.Contains(head, "exited with code") ||
		strings.Contains(head, "\"exit_code\":1")
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			return ln
		}
	}
	return ""
}

func lineTime(ts string, fallback time.Time) time.Time {
	if ts == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	return fallback
}
