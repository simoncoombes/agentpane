package state

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// multiline is the shape that broke rows: a python3 one-liner with real
// newlines, a tab, and runs of indentation. It is also a command, so it has to
// survive intact somewhere.
const multiline = "/usr/bin/python3 -c \"\nimport json,re\n" +
	"p = '/Users/x/very/long/path'\n\tprint(p)\n\"   && echo done"

// TestIngestFlattensToolTextAndKeepsTheOriginal is §13.1(b): the row-facing text
// is flattened where the event enters, and the exact bytes are still available —
// `y` must yank what the tool actually ran, not a one-line paraphrase of it.
func TestIngestFlattensToolTextAndKeepsTheOriginal(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(time.Second, event.ToolStart, "a1", withTool("Bash", multiline))
	f.apply(2*time.Second, event.ToolEnd, "a1", withTool("Bash", multiline), withExit(0))

	a := agentByID(f.m.Snapshot(), "a1")
	if a == nil {
		t.Fatal("agent missing")
	}
	if len(a.CallHistory) != 1 {
		t.Fatalf("call history = %+v, want the one call", a.CallHistory)
	}
	c := a.CallHistory[0]

	// Display: one line, no control characters, no runs of whitespace.
	if strings.ContainsAny(c.Target, "\n\r\t\v\f") {
		t.Errorf("stored call target is not flattened: %q", c.Target)
	}
	if strings.Contains(c.Target, "  ") {
		t.Errorf("whitespace runs survived: %q", c.Target)
	}
	if !strings.HasPrefix(c.Target, "/usr/bin/python3 -c \" import json,re") {
		t.Errorf("flattened target = %q, want the command on one line", c.Target)
	}

	// Yank: the original bytes, exactly.
	if got := c.TargetRaw(); got != multiline {
		t.Errorf("yank value = %q, want the ORIGINAL command %q", got, multiline)
	}
	if c.RawTarget == "" {
		t.Error("RawTarget was not populated, so the original is unrecoverable")
	}

	// The activity line follows the same rule.
	if strings.ContainsAny(a.ActivityLine, "\n\t") {
		t.Errorf("activity line is not flattened: %q", a.ActivityLine)
	}
	if got := a.ActivityRaw(); !strings.Contains(got, "\n") {
		t.Errorf("activity yank value lost the original newlines: %q", got)
	}
}

// An open call keeps both forms too: the row shows "◐ in Bash", the inspector
// and `y` need the command.
func TestOpenCallKeepsTheOriginalCommand(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(time.Second, event.ToolStart, "a1", withTool("Bash", multiline))

	a := agentByID(f.m.Snapshot(), "a1")
	if a.OpenCall == nil {
		t.Fatal("no open call recorded")
	}
	if strings.Contains(a.OpenCall.Target, "\n") {
		t.Errorf("open call target is not flattened: %q", a.OpenCall.Target)
	}
	if got := a.OpenCall.TargetRaw(); got != multiline {
		t.Errorf("open call raw = %q, want the original", got)
	}
}

// §3.7.1 wants the exact command in the band, and answering the ask elsewhere
// means pasting it: the ask keeps the original bytes even though the row that
// displays it is flattened.
func TestAskKeepsTheExactCommand(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(time.Second, event.PermissionRequested, "a1", withTool("Bash", multiline))

	w := f.m.Snapshot()
	if len(w.Asks) != 1 {
		t.Fatalf("asks = %+v, want one", w.Asks)
	}
	if got := w.Asks[0].Command; got != multiline {
		t.Errorf("band command = %q, want the exact command", got)
	}
}

// Error text is never flattened: it is the first thing `y` reaches for, it is
// often a stack trace, and the draw-time guard already protects the frame from
// it. A multi-line error must survive byte-for-byte.
func TestErrorTextIsNotFlattened(t *testing.T) {
	const tsErr = "TS2345 handlers.ts:88\n'string | null' not assignable to 'string'"
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"), withErr(tsErr))
	if got := agentByID(f.m.Snapshot(), "a1").LastError; got != tsErr {
		t.Errorf("LastError = %q, want the original text", got)
	}
}

// Flattening is idempotent, so the mux doing it and the machine doing it again
// cannot double-truncate or lose the first original.
func TestFlattenedIsIdempotent(t *testing.T) {
	ev := event.Event{Kind: event.ToolStart, Tool: "Bash", Target: multiline,
		Says: "Run\tthe  suite", Detail: "line one\nline two"}
	once := event.Flattened(ev)
	twice := event.Flattened(once)
	if once.Target != twice.Target || once.RawTarget != twice.RawTarget ||
		once.Says != twice.Says || once.Detail != twice.Detail {
		t.Errorf("Flattened is not idempotent:\n once=%+v\ntwice=%+v", once, twice)
	}
	if once.RawTarget != multiline {
		t.Errorf("RawTarget = %q, want the first original", once.RawTarget)
	}
	if once.Says != "Run the suite" {
		t.Errorf("Says = %q, want it flattened", once.Says)
	}
	if once.Detail != "line one line two" {
		t.Errorf("Detail = %q, want it flattened", once.Detail)
	}
}

// The user's prompt, the session title and an agent's final message are carried,
// not owned: they are quoted, wrapped and yanked elsewhere, so ingest leaves
// them exactly as they arrived.
func TestVerbatimDetailSurvivesIngest(t *testing.T) {
	const prompt = "Read PART 13 of SPEC.md\n\n- start with §13.2\n- then Q2/Q3"
	f := newFix()
	f.apply(0, event.SessionStart, event.MainAgentID)
	f.apply(0, event.UserPromptSubmitted, event.MainAgentID, withDetail(prompt))
	w := f.m.Snapshot()
	if w.Run == nil {
		t.Fatal("no run started")
	}
	if w.Run.Prompt != prompt {
		t.Errorf("run prompt = %q, want the user's text verbatim", w.Run.Prompt)
	}

	const msg = "Done.\n\n+29 −4 in api/types.ts"
	f.spawnRun(time.Second, "a1")
	f.apply(2*time.Second, event.AgentReturned, "a1", withDetail(msg))
	if got := agentByID(f.m.Snapshot(), "a1").LastMessage; got != msg {
		t.Errorf("LastMessage = %q, want the agent's text verbatim", got)
	}
}
