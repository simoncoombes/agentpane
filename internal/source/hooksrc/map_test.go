package hooksrc

import (
	"reflect"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// Captured payloads quoted verbatim from docs/DATA-SOURCES.md §3.3.
const (
	capSessionStart = `{"session_id":"292ef2e2-...","transcript_path":"/Users/simoncoombes/.claude/projects/<munged-cwd>/292ef2e2-....jsonl","cwd":"<dir>","hook_event_name":"SessionStart","source":"startup"}`

	capSessionEnd = `{"session_id":"...","transcript_path":"...","cwd":"...","prompt_id":"...","hook_event_name":"SessionEnd","reason":"other"}`

	capUserPromptSubmit = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"auto","hook_event_name":"UserPromptSubmit","prompt":"Use the Agent tool to spawn..."}`

	capPreToolUseMainAgent = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":"Execute bash, read file, report done","prompt":"Run the bash command ` + "`echo subagent-was-here`" + `...","subagent_type":"general-purpose","run_in_background":false},"tool_use_id":"toolu_01FzZb..."}`

	capPreToolUseSubagent = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo subagent-was-here","description":"..."},"tool_use_id":"toolu_01AE7J..."}`

	capSubagentStart = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"SubagentStart"}`

	capSubagentStop = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"SubagentStop","stop_hook_active":false,"agent_transcript_path":"/Users/simoncoombes/.claude/projects/<munged-cwd>/292ef2e2-.../subagents/agent-ada0620f224b32d63.jsonl","last_assistant_message":"done","background_tasks":[],"session_crons":[]}`

	capStop = `{"session_id":"...","transcript_path":"...","cwd":"...","prompt_id":"...","permission_mode":"default","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"finished","background_tasks":[],"session_crons":[]}`

	capNotification = `{"session_id":"56d4684f-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"79eb4307-...","hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"}`

	capPermissionRequestMain = `{"session_id":"0d04849b-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"9a63c0ff-...","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /private/tmp/permreq-probe-a","description":"..."},"permission_suggestions":[{"type":"addDirectories","directories":["/private/tmp"],"destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`

	capPermissionRequestSubagent = `{"session_id":"fcd32602-...","...":"...","agent_id":"aff1225aad226c58e","agent_type":"general-purpose","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /private/tmp/permreq-probe-b","description":"..."}}`
)

// PostToolUse has no full quoted capture in §3.3 ("same fields as PreToolUse
// plus tool_response, duration_ms"), so these are constructed to that shape.
const (
	synthPostToolUseSubagentBash = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo subagent-was-here","description":"..."},"tool_use_id":"toolu_01AE7J...","tool_response":{"stdout":"subagent-was-here","stderr":""},"duration_ms":142}`

	synthPostToolUseEdit = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/tmp/x.go","old_string":"a","new_string":"b"},"tool_use_id":"toolu_01ED17...","tool_response":{"filePath":"/tmp/x.go","structuredPatch":[{"oldStart":1,"newStart":1,"lines":["-a","-c","+b","+d","+e"," ctx"]}]},"duration_ms":9}`

	synthPreToolUseRead = `{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/tmp/subagent-test.txt"},"tool_use_id":"toolu_01RD..."}`
)

// stripVolatile zeroes fields the emitter or transport owns so golden
// comparisons cover only what mapLine decides.
func stripVolatile(evs []event.Event) []event.Event {
	out := make([]event.Event, len(evs))
	for i, ev := range evs {
		ev.Seq = 0
		ev.Raw = nil
		out[i] = ev
	}
	return out
}

func TestMapLineGolden(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    []event.Event
	}{
		{
			name:    "SessionStart",
			payload: capSessionStart,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
				Kind: event.SessionStart, Detail: "startup",
			}},
		},
		{
			name:    "SessionEnd",
			payload: capSessionEnd,
			want: []event.Event{{
				SessionID: "...", AgentID: event.MainAgentID,
				Kind: event.SessionEnd, Detail: "other",
			}},
		},
		{
			name:    "UserPromptSubmit uses 'prompt' not 'user_prompt'",
			payload: capUserPromptSubmit,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
				Kind: event.UserPromptSubmitted, Detail: "Use the Agent tool to spawn...",
			}},
		},
		{
			// The spawning call is MAIN's tool call and nothing else: it
			// carries no agent_id, so an AgentSpawned built from it would be
			// keyed to "main" — naming nobody and crediting main with a
			// phantom spawn. The description is held for the SubagentStart
			// instead (see Mapper).
			name:    "PreToolUse main Agent tool is only a tool call",
			payload: capPreToolUseMainAgent,
			want: []event.Event{
				{
					SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
					Kind: event.ToolStart, Tool: "Agent",
					Target: "Execute bash, read file, report done",
					Says:   "Execute bash, read file, report done",
				},
			},
		},
		{
			name:    "PreToolUse subagent attribution via agent_id",
			payload: capPreToolUseSubagent,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
				Kind: event.ToolStart, Tool: "Bash", Target: "echo subagent-was-here",
				Says: "...",
			}},
		},
		{
			name:    "PreToolUse Read emits FileRead",
			payload: synthPreToolUseRead,
			want: []event.Event{
				{
					SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
					Kind: event.ToolStart, Tool: "Read", Target: "/tmp/subagent-test.txt",
				},
				{
					SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
					Kind: event.FileRead, Tool: "Read", Target: "/tmp/subagent-test.txt",
				},
			},
		},
		{
			name:    "PostToolUse subagent Bash with duration",
			payload: synthPostToolUseSubagentBash,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
				Kind: event.ToolEnd, Tool: "Bash", Target: "echo subagent-was-here",
				Says:     "...",
				Duration: 142 * time.Millisecond,
			}},
		},
		{
			name:    "PostToolUse Edit emits FileEdited with patch lines",
			payload: synthPostToolUseEdit,
			want: []event.Event{
				{
					SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
					Kind: event.ToolEnd, Tool: "Edit", Target: "/tmp/x.go", Duration: 9 * time.Millisecond,
				},
				{
					SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
					Kind: event.FileEdited, Tool: "Edit", Target: "/tmp/x.go",
					LinesAdd: 3, LinesDel: 2,
				},
			},
		},
		{
			name:    "SubagentStart",
			payload: capSubagentStart,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
				Kind: event.AgentStarted,
			}},
		},
		{
			name:    "SubagentStop carries last_assistant_message",
			payload: capSubagentStop,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: "ada0620f224b32d63", AgentType: "general-purpose",
				Kind: event.AgentReturned, Detail: "done",
			}},
		},
		{
			name:    "Notification permission_prompt is an unattributed confirmer",
			payload: capNotification,
			want: []event.Event{{
				SessionID: "56d4684f-...", AgentID: event.MainAgentID,
				Kind: event.NotificationRaised, Detail: "Claude needs your permission",
				Target: "permission_prompt",
			}},
		},
		{
			name:    "Stop is unmapped",
			payload: capStop,
			want:    nil,
		},
		{
			name:    "PreCompact",
			payload: `{"session_id":"292ef2e2-...","hook_event_name":"PreCompact","trigger":"auto"}`,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
				Kind: event.CompactStarted, Detail: "auto",
			}},
		},
		{
			name:    "PostCompact",
			payload: `{"session_id":"292ef2e2-...","hook_event_name":"PostCompact","trigger":"auto","compact_summary":"..."}`,
			want: []event.Event{{
				SessionID: "292ef2e2-...", AgentID: event.MainAgentID,
				Kind: event.CompactFinished, Detail: "auto",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewMapper().Map([]byte(tt.payload))
			if err != nil {
				t.Fatalf("Map error: %v", err)
			}
			for i, ev := range got {
				if string(ev.Raw) != tt.payload {
					t.Errorf("event %d: Raw does not carry the original payload", i)
				}
			}
			if !reflect.DeepEqual(stripVolatile(got), stripVolatile(tt.want)) {
				t.Errorf("events mismatch\n got: %+v\nwant: %+v", stripVolatile(got), stripVolatile(tt.want))
			}
		})
	}
}

func TestMapLinePermissionRequestAsk(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantAgentID string
		wantType    string
		wantAsk     event.AskPayload
	}{
		{
			name:        "main agent Bash with captured permission_suggestions",
			payload:     capPermissionRequestMain,
			wantAgentID: event.MainAgentID,
			wantAsk: event.AskPayload{
				Kind:    "command",
				Command: "touch /private/tmp/permreq-probe-a",
				Options: []string{"addDirectories", "setMode"},
			},
		},
		{
			name:        "subagent Bash is fully attributable, no suggestions",
			payload:     capPermissionRequestSubagent,
			wantAgentID: "aff1225aad226c58e",
			wantType:    "general-purpose",
			wantAsk: event.AskPayload{
				Kind:    "command",
				Command: "touch /private/tmp/permreq-probe-b",
			},
		},
		{
			name:        "Edit classifies file_write with file_path command",
			payload:     `{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"/tmp/x.go"}}`,
			wantAgentID: event.MainAgentID,
			wantAsk:     event.AskPayload{Kind: "file_write", Command: "/tmp/x.go"},
		},
		{
			name:        "Write classifies file_write",
			payload:     `{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"/tmp/y.md"}}`,
			wantAgentID: event.MainAgentID,
			wantAsk:     event.AskPayload{Kind: "file_write", Command: "/tmp/y.md"},
		},
		{
			name:        "mcp__ prefix classifies mcp",
			payload:     `{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"mcp__linear__save_issue","tool_input":{}}`,
			wantAgentID: event.MainAgentID,
			wantAsk:     event.AskPayload{Kind: "mcp"},
		},
		{
			name:        "anything else classifies other",
			payload:     `{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"WebFetch","tool_input":{"url":"https://example.com"}}`,
			wantAgentID: event.MainAgentID,
			wantAsk:     event.AskPayload{Kind: "other"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewMapper().Map([]byte(tt.payload))
			if err != nil {
				t.Fatalf("Map error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1", len(got))
			}
			ev := got[0]
			if ev.Kind != event.PermissionRequested {
				t.Fatalf("Kind = %v, want PermissionRequested", ev.Kind)
			}
			if ev.AgentID != tt.wantAgentID {
				t.Errorf("AgentID = %q, want %q", ev.AgentID, tt.wantAgentID)
			}
			if ev.AgentType != tt.wantType {
				t.Errorf("AgentType = %q, want %q", ev.AgentType, tt.wantType)
			}
			if ev.Ask == nil {
				t.Fatal("Ask is nil")
			}
			if !reflect.DeepEqual(*ev.Ask, tt.wantAsk) {
				t.Errorf("Ask = %+v, want %+v", *ev.Ask, tt.wantAsk)
			}
		})
	}
}

func TestMapLineJunk(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"not JSON", `this is not json`},
		{"torn line", `{"session_id":"292ef2e2-...","hook_event_name":"PreToolUse","tool_na`},
		{"missing hook_event_name", `{"session_id":"292ef2e2-..."}`},
		{"missing session_id", `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`},
		{"JSON scalar", `42`},
		{"JSON array", `[1,2,3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := NewMapper().Map([]byte(tt.payload))
			if err == nil {
				t.Fatal("want error for junk payload")
			}
			if len(evs) != 0 {
				t.Fatalf("junk payload produced %d events", len(evs))
			}
		})
	}
}

// Malformed nested shapes must degrade, never panic (C9).
func TestMapLineMalformedNestedShapes(t *testing.T) {
	payloads := []string{
		`{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":"not-an-object"}`,
		`{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":null}`,
		`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/x"},"tool_response":"string not object"}`,
		`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{},"tool_response":{"structuredPatch":"bogus"}}`,
		`{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{},"permission_suggestions":[{"no_type":true}]}`,
		`{"session_id":"s","hook_event_name":"SubagentStop","agent_id":"a1"}`,
	}
	for _, p := range payloads {
		if _, err := NewMapper().Map([]byte(p)); err != nil {
			t.Errorf("payload %q: want tolerant mapping, got error %v", p, err)
		}
	}
}
