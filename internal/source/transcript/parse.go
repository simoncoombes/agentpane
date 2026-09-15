package transcript

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const detailMaxLen = 120

// userDenyPrefix is the transcript signature of an interactive Esc denial
// (DATA-SOURCES §4.5).
const userDenyPrefix = "The user doesn't want to proceed"

// autoModeDenialKind marks an auto-mode classifier denial (DATA-SOURCES §4.5).
const autoModeDenialKind = "automode-blocked"

// aiTitleType is the transcript line carrying Claude Code's own short session
// title (DATA-SOURCES §4.2 metadata records, §6 SessionTitled row).
const aiTitleType = "ai-title"

// editTools are the tool names whose results carry a structuredPatch and mean
// a file was written (DATA-SOURCES §6 FileEdited row).
var editTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

type rawLine struct {
	Type string `json:"type"`
	// Effort is the session's reasoning effort ("high"), written on every
	// assistant line; PerTurnEffort overrides it for one turn and is usually
	// null. Neither hook payload carries either.
	Effort         string          `json:"effort"`
	PerTurnEffort  string          `json:"perTurnEffort"`
	Subtype        string          `json:"subtype"`
	Timestamp      string          `json:"timestamp"`
	SessionID      string          `json:"sessionId"`
	AgentID        string          `json:"agentId"`
	AITitle        string          `json:"aiTitle"`
	Message        json.RawMessage `json:"message"`
	ToolUseResult  json.RawMessage `json:"toolUseResult"`
	ToolDenialKind string          `json:"toolDenialKind"`
	DurationMs     int64           `json:"durationMs"`
}

type apiMessage struct {
	Content json.RawMessage `json:"content"`
	Usage   *usage          `json:"usage"`
	// Model is the model id the platform ran this turn on, verbatim
	// ("claude-opus-5"). Subagents inherit the session's model unless they were
	// spawned onto another one, which is exactly the thing worth showing.
	Model string `json:"model"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// total is what one assistant message COST IN WORK: the tokens the model read
// for the first time plus the tokens it wrote.
//
// cache_read_input_tokens is deliberately excluded. It is the whole prompt
// re-read from cache on every turn, so it is by far the largest of the four and
// it grows with the length of the conversation rather than with anything the
// agent did — a thirty-turn agent on a 300k context reported 10M "tokens" while
// having written a few thousand. Every number in the pane derives from this
// sum: the row's k/s burn rate, the token column, the run summary. A rate that
// tracks context size instead of output is a rate that answers nothing.
//
// The field is still parsed, so the choice is one line to revisit and nothing
// downstream assumes either reading.
func (u *usage) total() int {
	if u == nil {
		return 0
	}
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"` // tool_use
	ID        string          `json:"id"`   // tool_use id
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result body: string or blocks
}

// richResult is the lenient shape of the top-level toolUseResult
// (DATA-SOURCES §4.4); it may also be a bare string, in which case
// unmarshalling fails and every field stays zero.
type richResult struct {
	Stdout          string      `json:"stdout"`
	Stderr          string      `json:"stderr"`
	FilePath        string      `json:"filePath"`
	AgentID         string      `json:"agentId"`
	Status          string      `json:"status"`
	Description     string      `json:"description"`
	StructuredPatch []patchHunk `json:"structuredPatch"`
}

type patchHunk struct {
	Lines []string `json:"lines"`
}

// newEvent stamps common fields. sessionID "" falls back to the configured
// session; agent name/type enrichment comes from the sidecar cache.
func (s *Source) newEvent(kind event.Kind, at time.Time, agentID, sessionID string, raw []byte, mut func(*event.Event)) event.Event {
	s.seq++
	if sessionID == "" {
		sessionID = s.sessionID
	}
	ev := event.Event{
		Seq:       s.seq,
		At:        at,
		SessionID: sessionID,
		AgentID:   agentID,
		Kind:      kind,
	}
	if info, ok := s.info[agentID]; ok {
		ev.AgentName = info.name
		ev.AgentType = info.typ
		// The sidecar holds the agent id and its description in ONE file, so
		// this name needs no correlation and can correct a hooks-derived one.
		ev.NameExact = info.name != ""
	}
	if raw != nil {
		ev.Raw = json.RawMessage(raw)
	}
	if mut != nil {
		mut(&ev)
	}
	return ev
}

// lineTime extracts a line's own timestamp, falling back to the injected
// clock when absent or unparseable (frozen contract: ingest time if absent).
func (s *Source) lineTime(raw []byte) time.Time {
	var ln struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &ln); err == nil && ln.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, ln.Timestamp); err == nil {
			return t
		}
	}
	return s.clock()
}

// lineEvents maps one complete transcript line onto taxonomy events.
// Malformed lines are counted and dropped, never fatal (C9).
func (s *Source) lineEvents(st *fileState, raw []byte) []event.Event {
	var ln rawLine
	if err := json.Unmarshal(raw, &ln); err != nil {
		s.skipped++
		return nil
	}
	at := s.clock()
	if ln.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, ln.Timestamp); err == nil {
			at = t
		}
	}
	switch ln.Type {
	case "assistant":
		return s.assistantEvents(st, ln, at, raw)
	case "user":
		return s.userEvents(st, ln, at, raw)
	case aiTitleType:
		// Claude Code's own short session title, verbatim
		// ({"type":"ai-title","aiTitle":…,"sessionId":…} — exactly those
		// three fields, no timestamp, so `at` is ingest time). Rewritten
		// per turn, so the last line read wins downstream. It appears only
		// in the MAIN session jsonl; a copy under an agent file would name
		// the session, not that agent, so it is ignored there rather than
		// misattributed (C8).
		if ln.AITitle == "" || st.agentID != event.MainAgentID {
			return nil
		}
		return []event.Event{s.newEvent(event.SessionTitled, at, event.MainAgentID, ln.SessionID, raw, func(e *event.Event) {
			e.Detail = truncate(ln.AITitle)
		})}
	case "system":
		if ln.Subtype == "turn_duration" && st.agentID == event.MainAgentID {
			// The root agent finished a turn (DATA-SOURCES §4.2). That
			// settles the run; it does not mean the session is waiting for a
			// human, which is what this used to be read as and what the
			// sessions registry actually knows.
			return []event.Event{s.newEvent(event.TurnEnded, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
				e.Detail = "turn_duration"
			})}
		}
	}
	return nil
}

func (s *Source) assistantEvents(st *fileState, ln rawLine, at time.Time, raw []byte) []event.Event {
	var msg apiMessage
	if err := json.Unmarshal(ln.Message, &msg); err != nil {
		s.skipped++
		return nil
	}
	var evs []event.Event
	// What this agent is RUNNING on, before anything it did with it. A
	// synthetic line (Claude Code's own local messages) names no real model and
	// must not overwrite one (C8).
	if model := strings.TrimSpace(msg.Model); model != "" && !strings.HasPrefix(model, "<") {
		effort := strings.TrimSpace(ln.PerTurnEffort)
		if effort == "" {
			effort = strings.TrimSpace(ln.Effort)
		}
		if model != st.model || effort != st.effort {
			st.model, st.effort = model, effort
			evs = append(evs, s.newEvent(event.ModelObserved, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
				e.Model, e.Effort = model, effort
			}))
		}
	}
	var blocks []contentBlock
	// Content may legitimately be a bare string on some line shapes;
	// that is not an error, just no blocks.
	_ = json.Unmarshal(msg.Content, &blocks)

	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			evs = append(evs, s.toolUseEvents(st, ln, b, at, raw)...)
		case "thinking":
			evs = append(evs, s.newEvent(event.ThinkingEnded, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
				e.Detail = firstLine(b.Thinking)
			}))
		}
	}

	if delta := msg.Usage.total(); delta > 0 {
		st.tokens += delta
		tokens, cum := st.tokens, true
		if st.resumed {
			// Resumed mid-file: the true cumulative count is unknown,
			// so report the per-message delta honestly (C8).
			tokens, cum = delta, false
		}
		evs = append(evs, s.newEvent(event.TokensUpdated, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
			e.Tokens = tokens
			e.TokensCum = cum
		}))
	}
	return evs
}

func (s *Source) toolUseEvents(st *fileState, ln rawLine, b contentBlock, at time.Time, raw []byte) []event.Event {
	var input map[string]any
	_ = json.Unmarshal(b.Input, &input)

	target := toolTarget(b.Name, input)
	detail := str(input["description"])
	if b.Name == "TodoWrite" {
		// The state machine reads main's current todo item from here
		// (DATA-SOURCES §5).
		detail = todoInProgress(b.Input)
	}

	pend := pendingTool{tool: b.Name, target: target, agentID: st.agentID}
	if b.Name == "Agent" {
		pend.spawnDesc = str(input["description"])
		pend.spawnType = str(input["subagent_type"])
	}
	if b.ID != "" {
		s.pending[b.ID] = pend
	}

	evs := []event.Event{s.newEvent(event.ToolStart, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
		e.Tool = b.Name
		e.Target = target
		// The tool's own human summary, when it wrote one: a row shows this
		// rather than a shell condition truncated mid-expression.
		e.Says = str(input["description"])
		e.Detail = truncate(detail)
	})}
	if b.Name == "Read" && str(input["file_path"]) != "" {
		evs = append(evs, s.newEvent(event.FileRead, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
			e.Tool = b.Name
			e.Target = str(input["file_path"])
		}))
	}
	return evs
}

func (s *Source) userEvents(st *fileState, ln rawLine, at time.Time, raw []byte) []event.Event {
	var msg apiMessage
	if err := json.Unmarshal(ln.Message, &msg); err != nil {
		return nil // human prompt lines and metadata shapes are not errors
	}
	var blocks []contentBlock
	_ = json.Unmarshal(msg.Content, &blocks)

	var rich richResult
	haveRich := false
	if len(ln.ToolUseResult) > 0 && ln.ToolUseResult[0] == '{' {
		haveRich = json.Unmarshal(ln.ToolUseResult, &rich) == nil
	}

	var evs []event.Event
	first := true
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		pend, known := s.pending[b.ToolUseID]
		if known {
			delete(s.pending, b.ToolUseID)
		}
		text := resultText(b.Content)

		end := s.newEvent(event.ToolEnd, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
			e.Tool = pend.tool
			e.Target = pend.target
			if b.IsError {
				e.Err = text
				if e.Err == "" && first && haveRich {
					e.Err = rich.Stderr
				}
			} else if first && haveRich {
				e.Detail = truncate(firstLine(rich.Stdout))
			}
		})
		// The rich top-level toolUseResult describes a single call; apply
		// it to the first result block only (DATA-SOURCES §4.4).
		if first && haveRich {
			evs = append(evs, s.richEvents(st, ln, pend, rich, b, at, raw)...)
		}
		evs = append(evs, end)
		evs = append(evs, s.denialEvents(st, ln, pend, b, text, at, raw)...)
		first = false
	}
	return evs
}

// richEvents maps the top-level toolUseResult extras: FileEdited with line
// counts for edit tools, and the agentId join that yields AgentSpawned for
// the Agent tool (DATA-SOURCES §4.3, §6).
func (s *Source) richEvents(st *fileState, ln rawLine, pend pendingTool, rich richResult, b contentBlock, at time.Time, raw []byte) []event.Event {
	var evs []event.Event
	if editTools[pend.tool] && !b.IsError {
		add, del := patchCounts(rich.StructuredPatch)
		target := rich.FilePath
		if target == "" {
			target = pend.target
		}
		evs = append(evs, s.newEvent(event.FileEdited, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
			e.Tool = pend.tool
			e.Target = target
			e.LinesAdd = add
			e.LinesDel = del
		}))
	}
	if pend.tool == "Agent" && rich.AgentID != "" && !s.spawned[rich.AgentID] {
		s.spawned[rich.AgentID] = true
		name := pend.spawnDesc
		if name == "" {
			name = rich.Description
		}
		if _, ok := s.info[rich.AgentID]; !ok {
			s.info[rich.AgentID] = agentInfo{name: name, typ: pend.spawnType}
		}
		evs = append(evs, s.newEvent(event.AgentSpawned, at, rich.AgentID, ln.SessionID, raw, nil))
	}
	return evs
}

// denialEvents maps the §4.5 permission-denial signatures.
func (s *Source) denialEvents(st *fileState, ln rawLine, pend pendingTool, b contentBlock, text string, at time.Time, raw []byte) []event.Event {
	userDeny := b.IsError && strings.HasPrefix(text, userDenyPrefix)
	autoDeny := ln.ToolDenialKind == autoModeDenialKind
	if !userDeny && !autoDeny {
		return nil
	}
	return []event.Event{s.newEvent(event.PermissionDenied, at, st.agentID, ln.SessionID, raw, func(e *event.Event) {
		e.Tool = pend.tool
		e.Target = pend.target
		e.Detail = truncate(firstLine(text))
		e.Err = text
	})}
}

// toolTarget extracts the raw target per tool shape (UI shortens). The
// fallback chain covers unknown tools without inventing anything.
func toolTarget(name string, input map[string]any) string {
	switch name {
	case "Bash":
		return str(input["command"])
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		return str(input["file_path"])
	case "Grep", "Glob":
		return str(input["pattern"])
	case "Agent":
		return str(input["description"])
	}
	for _, k := range []string{"file_path", "command", "pattern", "url", "path", "description"} {
		if v := str(input[k]); v != "" {
			return v
		}
	}
	return ""
}

// todoInProgress returns the in_progress item's display text from a
// TodoWrite input (DATA-SOURCES §5: {content, status, activeForm}).
func todoInProgress(input json.RawMessage) string {
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	for _, t := range in.Todos {
		if t.Status == "in_progress" {
			if t.ActiveForm != "" {
				return t.ActiveForm
			}
			return t.Content
		}
	}
	return ""
}

// resultText flattens a tool_result body, which is either a bare string or
// an array of {type:"text", text} blocks (DATA-SOURCES §4.2).
func resultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func patchCounts(hunks []patchHunk) (add, del int) {
	for _, h := range hunks {
		for _, l := range h.Lines {
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

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s)
}

func truncate(s string) string {
	if len(s) <= detailMaxLen {
		return s
	}
	// Cut on a rune boundary so the result stays valid UTF-8.
	cut := detailMaxLen
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
