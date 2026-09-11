package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

var testNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

const (
	ts1 = "2026-08-01T10:00:01.000Z"
	ts2 = "2026-08-01T10:00:02.000Z"
	ts3 = "2026-08-01T10:00:03.000Z"

	agentA = "ada0620f224b32d63"
	agentB = "bff1225aad226c58e"
)

func newSource(t *testing.T, dir string, opts ...Option) *Source {
	t.Helper()
	opts = append(opts, WithClock(func() time.Time { return testNow }))
	return New(dir, "sess1", opts...)
}

func mainPath(dir string) string { return filepath.Join(dir, "sess1.jsonl") }

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

// mapped strips transport-state events so tests assert only line mappings.
func mapped(evs []event.Event) []event.Event {
	var out []event.Event
	for _, e := range evs {
		if e.Kind != event.SourceConnected && e.Kind != event.SourceDisconnected {
			out = append(out, e)
		}
	}
	return out
}

func asst(ts, blocks string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"sess1","message":{"content":[%s],"usage":{"input_tokens":%d,"output_tokens":%d}}}`, ts, blocks, in, out)
}

func toolUse(id, name, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, name, input)
}

func userResult(ts, toolUseID, content string, isErr bool, rich, denialKind string) string {
	line := fmt.Sprintf(`{"type":"user","timestamp":%q,"sessionId":"sess1","message":{"content":[{"type":"tool_result","tool_use_id":%q,"is_error":%t,"content":%s}]}`,
		ts, toolUseID, isErr, content)
	if rich != "" {
		line += `,"toolUseResult":` + rich
	}
	if denialKind != "" {
		line += fmt.Sprintf(`,"toolDenialKind":%q`, denialKind)
	}
	return line + "}"
}

type want struct {
	kind   event.Kind
	agent  string
	name   string
	tool   string
	target string
	detail string
	errSub string
	tokens int
	cum    bool
	add    int
	del    int
}

func checkEvents(t *testing.T, got []event.Event, wants []want) {
	t.Helper()
	if len(got) != len(wants) {
		var kinds []string
		for _, g := range got {
			kinds = append(kinds, g.Kind.String())
		}
		t.Fatalf("got %d events %v, want %d", len(got), kinds, len(wants))
	}
	for i, w := range wants {
		g := got[i]
		if g.Kind != w.kind {
			t.Errorf("event %d: kind = %s, want %s", i, g.Kind, w.kind)
		}
		if w.agent != "" && g.AgentID != w.agent {
			t.Errorf("event %d (%s): AgentID = %q, want %q", i, g.Kind, g.AgentID, w.agent)
		}
		if w.name != "" && g.AgentName != w.name {
			t.Errorf("event %d (%s): AgentName = %q, want %q", i, g.Kind, g.AgentName, w.name)
		}
		if w.tool != "" && g.Tool != w.tool {
			t.Errorf("event %d (%s): Tool = %q, want %q", i, g.Kind, g.Tool, w.tool)
		}
		if w.target != "" && g.Target != w.target {
			t.Errorf("event %d (%s): Target = %q, want %q", i, g.Kind, g.Target, w.target)
		}
		if w.detail != "" && g.Detail != w.detail {
			t.Errorf("event %d (%s): Detail = %q, want %q", i, g.Kind, g.Detail, w.detail)
		}
		if w.errSub != "" && !strings.Contains(g.Err, w.errSub) {
			t.Errorf("event %d (%s): Err = %q, want substring %q", i, g.Kind, g.Err, w.errSub)
		}
		if w.tokens != 0 {
			if g.Tokens != w.tokens {
				t.Errorf("event %d (%s): Tokens = %d, want %d", i, g.Kind, g.Tokens, w.tokens)
			}
			if g.TokensCum != w.cum {
				t.Errorf("event %d (%s): TokensCum = %t, want %t", i, g.Kind, g.TokensCum, w.cum)
			}
		}
		if w.add != 0 && g.LinesAdd != w.add {
			t.Errorf("event %d (%s): LinesAdd = %d, want %d", i, g.Kind, g.LinesAdd, w.add)
		}
		if w.del != 0 && g.LinesDel != w.del {
			t.Errorf("event %d (%s): LinesDel = %d, want %d", i, g.Kind, g.LinesDel, w.del)
		}
	}
}

func TestLineMapping(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []want
	}{
		{
			name: "bash tool start and end",
			lines: []string{
				asst(ts1, toolUse("toolu_1", "Bash", `{"command":"go test ./...","description":"Run tests"}`), 40, 20),
				userResult(ts2, "toolu_1", `"ok\nsecond"`, false, `{"stdout":"ok\nsecond","stderr":""}`, ""),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "Bash", target: "go test ./...", detail: "Run tests"},
				{kind: event.TokensUpdated, agent: "main", tokens: 60, cum: true},
				{kind: event.ToolEnd, agent: "main", tool: "Bash", target: "go test ./...", detail: "ok"},
			},
		},
		{
			name: "agent spawn via toolUseResult agentId join",
			lines: []string{
				asst(ts1, toolUse("toolu_9", "Agent", `{"description":"explore schema","prompt":"look around","subagent_type":"general-purpose"}`), 0, 0),
				userResult(ts2, "toolu_9", `"launched"`, false,
					fmt.Sprintf(`{"agentId":%q,"status":"async_launched","isAsync":true,"description":"explore schema"}`, agentA), ""),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "Agent", target: "explore schema"},
				{kind: event.AgentSpawned, agent: agentA, name: "explore schema"},
				{kind: event.ToolEnd, agent: "main", tool: "Agent"},
			},
		},
		{
			name: "todo write carries in_progress item in Detail",
			lines: []string{
				asst(ts1, toolUse("toolu_2", "TodoWrite",
					`{"todos":[{"content":"A","status":"completed","activeForm":"Doing A"},{"content":"B","status":"in_progress","activeForm":"Doing B"}]}`), 0, 0),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "TodoWrite", detail: "Doing B"},
			},
		},
		{
			name: "thinking block and Read tool",
			lines: []string{
				asst(ts1, `{"type":"thinking","thinking":"pondering\nmore"},`+
					toolUse("toolu_3", "Read", `{"file_path":"/tmp/x.go"}`), 0, 0),
			},
			want: []want{
				{kind: event.ThinkingEnded, agent: "main", detail: "pondering"},
				{kind: event.ToolStart, agent: "main", tool: "Read", target: "/tmp/x.go"},
				{kind: event.FileRead, agent: "main", tool: "Read", target: "/tmp/x.go"},
			},
		},
		{
			name: "edit result emits FileEdited with patch counts",
			lines: []string{
				asst(ts1, toolUse("toolu_4", "Edit", `{"file_path":"/tmp/f.go","old_string":"old","new_string":"new"}`), 0, 0),
				userResult(ts2, "toolu_4", `"edited"`, false,
					`{"filePath":"/tmp/f.go","structuredPatch":[{"lines":["-old"," ctx","+new1","+new2"]}]}`, ""),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "Edit", target: "/tmp/f.go"},
				{kind: event.FileEdited, agent: "main", tool: "Edit", target: "/tmp/f.go", add: 2, del: 1},
				{kind: event.ToolEnd, agent: "main", tool: "Edit"},
			},
		},
		{
			name: "interactive deny maps PermissionDenied",
			lines: []string{
				asst(ts1, toolUse("toolu_5", "Bash", `{"command":"rm -rf /tmp/x"}`), 0, 0),
				userResult(ts2, "toolu_5",
					`"The user doesn't want to proceed with this tool use. The tool use was rejected."`, true, "", ""),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "Bash"},
				{kind: event.ToolEnd, agent: "main", tool: "Bash", errSub: "doesn't want to proceed"},
				{kind: event.PermissionDenied, agent: "main", tool: "Bash", target: "rm -rf /tmp/x", errSub: "doesn't want to proceed"},
			},
		},
		{
			name: "automode classifier denial maps PermissionDenied with Detail",
			lines: []string{
				asst(ts1, toolUse("toolu_6", "Bash", `{"command":"curl evil"}`), 0, 0),
				userResult(ts2, "toolu_6",
					`"Permission for this action was denied by the Claude Code auto mode classifier. Reason: risky."`, true, "", "automode-blocked"),
			},
			want: []want{
				{kind: event.ToolStart, agent: "main", tool: "Bash"},
				{kind: event.ToolEnd, agent: "main", tool: "Bash", errSub: "auto mode classifier"},
				{kind: event.PermissionDenied, agent: "main", tool: "Bash",
					detail: "Permission for this action was denied by the Claude Code auto mode classifier. Reason: risky."},
			},
		},
		{
			name: "turn_duration system line hints SessionIdle",
			lines: []string{
				fmt.Sprintf(`{"type":"system","subtype":"turn_duration","timestamp":%q,"sessionId":"sess1","durationMs":5000}`, ts1),
			},
			want: []want{
				{kind: event.SessionIdle, agent: "main", detail: "turn_duration"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeLines(t, mainPath(dir), tc.lines...)
			s := newSource(t, dir)
			checkEvents(t, mapped(s.scan()), tc.want)
		})
	}
}

// File position wins over timestamps: an earlier-stamped line later in the
// file must be emitted after (DATA-SOURCES §4.6).
func TestFilePositionOrderingBeatsTimestamps(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, mainPath(dir),
		asst(ts3, toolUse("toolu_a", "Bash", `{"command":"first-in-file"}`), 0, 0),
		asst(ts1, toolUse("toolu_b", "Bash", `{"command":"second-in-file"}`), 0, 0),
	)
	s := newSource(t, dir)
	got := mapped(s.scan())
	checkEvents(t, got, []want{
		{kind: event.ToolStart, target: "first-in-file"},
		{kind: event.ToolStart, target: "second-in-file"},
	})
	if !got[0].At.After(got[1].At) {
		t.Errorf("At order: got[0]=%v got[1]=%v, want file order preserved with inverted timestamps", got[0].At, got[1].At)
	}
	if got[0].Seq >= got[1].Seq {
		t.Errorf("Seq not monotonic in file order: %d then %d", got[0].Seq, got[1].Seq)
	}
}

func TestTornLineBuffered(t *testing.T) {
	dir := t.TempDir()
	line1 := asst(ts1, "", 10, 0)
	line2 := asst(ts2, "", 20, 0)
	writeLines(t, mainPath(dir), line1)
	appendRaw(t, mainPath(dir), line2[:15]) // torn tail, no newline

	s := newSource(t, dir)
	checkEvents(t, mapped(s.scan()), []want{
		{kind: event.TokensUpdated, tokens: 10, cum: true},
	})
	if off := s.Offsets()[mainPath(dir)]; off != int64(len(line1)+1) {
		t.Fatalf("offset = %d, want %d (torn tail must not be consumed)", off, len(line1)+1)
	}

	appendRaw(t, mainPath(dir), line2[15:]+"\n")
	checkEvents(t, mapped(s.scan()), []want{
		{kind: event.TokensUpdated, tokens: 30, cum: true},
	})
}

func TestTokensAccumulatePerAgentFile(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, mainPath(dir), asst(ts1, "", 40, 20))
	s := newSource(t, dir)
	checkEvents(t, mapped(s.scan()), []want{{kind: event.TokensUpdated, tokens: 60, cum: true}})

	appendRaw(t, mainPath(dir), asst(ts2, "", 100, 50)+"\n")
	checkEvents(t, mapped(s.scan()), []want{{kind: event.TokensUpdated, tokens: 210, cum: true}})
}

func TestMetaSidecarSpawnsAndEnriches(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sess1", "subagents")
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".meta.json"),
		`{"agentType":"general-purpose","description":"explore schema","toolUseId":"toolu_9","spawnDepth":1}`)
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".jsonl"), asst(ts1, "", 70, 30))

	s := newSource(t, dir)
	checkEvents(t, mapped(s.scan()), []want{
		{kind: event.AgentSpawned, agent: agentA, name: "explore schema"},
		{kind: event.AgentStarted, agent: agentA, name: "explore schema"},
		{kind: event.TokensUpdated, agent: agentA, name: "explore schema", tokens: 100, cum: true},
	})
}

// Workflow agents have no description; the name must stay empty, never be
// invented (C8).
func TestWorkflowMetaLeavesNameEmpty(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "sess1", "subagents", "workflows", "wf_1")
	writeLines(t, filepath.Join(wf, "agent-"+agentB+".meta.json"),
		`{"agentType":"workflow-subagent","spawnDepth":1}`)

	s := newSource(t, dir)
	got := mapped(s.scan())
	checkEvents(t, got, []want{{kind: event.AgentSpawned, agent: agentB}})
	if got[0].AgentName != "" {
		t.Errorf("AgentName = %q, want empty for workflow agent", got[0].AgentName)
	}
	if got[0].AgentType != "workflow-subagent" {
		t.Errorf("AgentType = %q, want workflow-subagent", got[0].AgentType)
	}
}

func TestOffsetResumeDoesNotReplayHistory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sess1", "subagents")
	writeLines(t, mainPath(dir), asst(ts1, "", 40, 20), asst(ts2, "", 100, 50))
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".meta.json"),
		`{"agentType":"general-purpose","description":"explore schema","toolUseId":"toolu_9","spawnDepth":1}`)
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".jsonl"), asst(ts1, "", 30, 0))

	s1 := newSource(t, dir)
	if got := mapped(s1.scan()); len(got) == 0 {
		t.Fatal("first pass produced no events")
	}
	offsets := s1.Offsets()

	s2 := newSource(t, dir, WithOffsets(offsets))
	if got := mapped(s2.scan()); len(got) != 0 {
		var kinds []string
		for _, g := range got {
			kinds = append(kinds, g.Kind.String())
		}
		t.Fatalf("resume replayed history: %v", kinds)
	}

	// New activity after resume: tokens are honest per-message deltas
	// (cumulative unknown), and enrichment still applies.
	appendRaw(t, filepath.Join(sub, "agent-"+agentA+".jsonl"), asst(ts3, "", 25, 25)+"\n")
	checkEvents(t, mapped(s2.scan()), []want{
		{kind: event.TokensUpdated, agent: agentA, name: "explore schema", tokens: 50, cum: false},
	})
}

func TestMalformedLinesSkippedWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, mainPath(dir),
		`{not json at all`,
		`{"type":"assistant","message":"not an object shape"}`,
		asst(ts1, "", 10, 0),
	)
	s := newSource(t, dir)
	checkEvents(t, mapped(s.scan()), []want{{kind: event.TokensUpdated, tokens: 10, cum: true}})
	if s.Skipped() == 0 {
		t.Error("Skipped() = 0, want malformed lines counted")
	}
	// The whole file must be consumed despite the junk.
	fi, err := os.Stat(mainPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if off := s.Offsets()[mainPath(dir)]; off != fi.Size() {
		t.Errorf("offset = %d, want %d", off, fi.Size())
	}
}

func TestTurnDurationInAgentFileIsNotSessionIdle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sess1", "subagents")
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".jsonl"),
		fmt.Sprintf(`{"type":"system","subtype":"turn_duration","timestamp":%q,"durationMs":100}`, ts1))
	s := newSource(t, dir)
	for _, g := range mapped(s.scan()) {
		if g.Kind == event.SessionIdle {
			t.Error("agent-file turn_duration must not emit SessionIdle")
		}
	}
}

// aiTitleLine is the verbatim shape on this machine, verified 2026-08-02
// against 75 transcripts under ~/.claude/projects/: exactly three fields
// (type, aiTitle, sessionId) and NO timestamp.
func aiTitleLine(title string) string {
	return fmt.Sprintf(`{"type":"ai-title","aiTitle":%q,"sessionId":"sess1"}`, title)
}

// The ai-title metadata line is the honest source of main's row label
// (DATA-SOURCES §6 SessionTitled). It is rewritten per turn, so every line
// emits an event and the state machine takes the last.
func TestAITitleEmitsSessionTitled(t *testing.T) {
	const t1 = "Connect to SND on IP 52.43.123.31"
	const t2 = "Build agentpane — live TUI monitor for Claude Code subagents"
	dir := t.TempDir()
	writeLines(t, mainPath(dir),
		aiTitleLine(t1),
		asst(ts1, toolUse("toolu_1", "Bash", `{"command":"true"}`), 0, 0),
		aiTitleLine(t2),
	)
	s := newSource(t, dir)
	checkEvents(t, mapped(s.scan()), []want{
		{kind: event.SessionTitled, agent: event.MainAgentID, detail: t1},
		{kind: event.ToolStart, agent: event.MainAgentID, tool: "Bash"},
		{kind: event.SessionTitled, agent: event.MainAgentID, detail: t2},
	})
	// No timestamp on the line: `at` falls back to the injected clock, and
	// the session id is carried through for Accepts gating.
	evs := mapped(s.scan())
	if len(evs) != 0 {
		t.Fatalf("re-scan re-emitted %d events", len(evs))
	}
	appendRaw(t, mainPath(dir), aiTitleLine(t1)+"\n")
	got := mapped(s.scan())
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if !got[0].At.Equal(testNow) {
		t.Errorf("At = %v, want the injected clock %v (ai-title lines carry no timestamp)", got[0].At, testNow)
	}
	if got[0].SessionID != "sess1" {
		t.Errorf("SessionID = %q, want sess1", got[0].SessionID)
	}
}

// An empty or absent aiTitle invents nothing (C8), and a copy landing in a
// subagent file names the session rather than that agent, so it is ignored
// there instead of misattributed.
func TestAITitleEdgeCases(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, mainPath(dir),
		`{"type":"ai-title","aiTitle":"","sessionId":"sess1"}`,
		`{"type":"ai-title","sessionId":"sess1"}`,
	)
	sub := filepath.Join(dir, "sess1", "subagents")
	writeLines(t, filepath.Join(sub, "agent-"+agentA+".jsonl"), aiTitleLine("a subagent's copy"))
	s := newSource(t, dir)
	for _, g := range mapped(s.scan()) {
		if g.Kind == event.SessionTitled {
			t.Errorf("SessionTitled emitted for %q (agent %s)", g.Detail, g.AgentID)
		}
	}
	if n := s.Skipped(); n != 0 {
		t.Errorf("well-formed metadata lines counted as skipped: %d", n)
	}
}

func TestEventsChannelDelivers(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, mainPath(dir), asst(ts1, toolUse("toolu_1", "Bash", `{"command":"true"}`), 0, 0))
	s := newSource(t, dir, WithPollInterval(5*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == event.ToolStart {
				cancel()
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for ToolStart")
		}
	}
}

// A message's token count is the work it did — tokens read for the first time
// plus tokens written — and NOT the cached prompt it re-read to do it.
// cache_read dwarfs the other three and grows with the conversation rather than
// with anything the agent did, so counting it made every rate in the pane track
// context size.
func TestTokensExcludeTheCachedPromptReRead(t *testing.T) {
	u := &usage{
		InputTokens:              7,
		OutputTokens:             300,
		CacheCreationInputTokens: 3000,
		CacheReadInputTokens:     400000,
	}
	if got, want := u.total(), 3307; got != want {
		t.Errorf("usage.total() = %d, want %d (cache_read must not be counted)", got, want)
	}
	if (*usage)(nil).total() != 0 {
		t.Error("a message with no usage block reports no tokens")
	}
}
