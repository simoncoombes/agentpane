package state

import (
	"testing"

	"github.com/simoncoombes/agentpane/internal/event"
)

// The activity line must say what the agent is DOING. PostToolUse carries
// duration_ms in Detail, so a Detail-first rule rendered rows as a bare
// "144ms" — a fact about the last call, not the work.
func TestActivityPrefersTheToolOverDuration(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withName("Summarize SPEC colour rules"))
	f.apply(0, event.ToolStart, "a1", withTool("Read", "docs/DATA-SOURCES.md"))
	f.apply(0, event.ToolEnd, "a1", withTool("Read", "docs/DATA-SOURCES.md"),
		withDetail("144ms"))

	w := f.m.Snapshot()
	if len(w.Agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(w.Agents))
	}
	got := w.Agents[0].ActivityLine
	if got == "144ms" {
		t.Fatalf("activity is a bare duration, not the work: %q", got)
	}
	if got != "Read docs/DATA-SOURCES.md" {
		t.Errorf("activity = %q, want the tool and its target", got)
	}

	// A settled agent keeps the last work it was seen doing; the row's tail
	// already says "returned · m:ss", so the activity need not restate it.
	f.apply(0, event.AgentReturned, "a1", withDetail("found it in acl.py"))
	if got := f.m.Snapshot().Agents[0].ActivityLine; got != "Read docs/DATA-SOURCES.md" {
		t.Errorf("settled activity = %q, want the last work", got)
	}
}
