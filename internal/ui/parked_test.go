package ui

// parked_test.go — the pane follows its tab onto a background job.
//
// Claude Code can park an interactive session's work on a bg job process that
// runs under a DIFFERENT session id. The CLI's followSession re-attaches the
// source stack; this is the other half, where the world has to be re-keyed or
// every event the pane just subscribed to is dropped as cross-session.

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// TestAttachHopReKeysTheWorld: the attached session changes underneath the
// Model, so the machine is re-keyed, the previous session's world is dropped
// rather than merged, and the job's events start landing.
func TestAttachHopReKeysTheWorld(t *testing.T) {
	m, _ := newTestModel(t)
	attached := "tab-1"
	m.cfg.AttachedSession = func() string { return attached }
	m.cfg.Machine.Reset("tab-1")
	m.v.PinnedSession, m.v.Attached = "tab-1", "tab-1"
	m.ingest(event.Event{SessionID: "tab-1", AgentID: "a1", AgentName: "on the tab",
		AgentType: "general-purpose", Kind: event.AgentSpawned, At: m.clock})
	if len(m.world.Agents) != 1 {
		t.Fatalf("the tab's own agent did not land: %d agents", len(m.world.Agents))
	}

	// The job's events while the world is still keyed to the tab: dropped.
	// This is the bug, not a nicety — three agents ran for eight minutes
	// behind a pane drawing an idle tab.
	job := event.Event{SessionID: "job-1", AgentID: "b1", AgentName: "on the job",
		AgentType: "general-purpose", Kind: event.AgentSpawned, At: m.clock.Add(time.Second)}
	m.ingest(job)
	if len(m.world.Agents) != 1 {
		t.Fatalf("a foreign session's agent landed before the hop: %d agents", len(m.world.Agents))
	}

	attached = "job-1"
	m.tick()

	if got := m.cfg.Machine.SessionID(); got != "job-1" {
		t.Fatalf("machine still keyed to %q after the hop", got)
	}
	if len(m.world.Agents) != 0 {
		t.Errorf("the tab's agents survived the hop: %d", len(m.world.Agents))
	}
	if !strings.Contains(m.v.Toast.Text, "job-") {
		t.Errorf("toast = %q, want the session it followed to", m.v.Toast.Text)
	}
	m.ingest(job)
	if len(m.world.Agents) != 1 {
		t.Fatalf("the job's agent still does not land: %d agents", len(m.world.Agents))
	}
}

// TestHeaderNamesBothSessionsWhileFollowing: the header is the first thing
// read, and "which session am I looking at" has two honest answers while the
// pane is following a parked tab. Naming only the pin is how this went
// unnoticed for a whole session.
func TestHeaderNamesBothSessionsWhileFollowing(t *testing.T) {
	m, _ := newTestModel(t)
	m.v.PinnedSession, m.v.Attached = "10e378ef-4639", "e9e4ae8e-9402"
	if got := headerSessionID(m.world, m.v); got != "10e3 → e9e4" {
		t.Errorf("header session id = %q, want \"10e3 → e9e4\"", got)
	}

	// Not following: one id, as before.
	m.v.Attached = m.v.PinnedSession
	if got := headerSessionID(m.world, m.v); got != "10e3" {
		t.Errorf("header session id = %q, want 10e3", got)
	}
	m.v.Attached = ""
	if got := headerSessionID(m.world, m.v); got != "10e3" {
		t.Errorf("waiting pane header = %q, want the pin", got)
	}
}
