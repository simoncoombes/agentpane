package state

import (
	"testing"

	"github.com/simoncoombes/agentpane/internal/event"
)

// A hooks name is correlated (description and agent id arrive on different
// payloads) and can be wrong when the platform interleaves phantom starts.
// The transcript sidecar is an exact join, so it must be able to correct one —
// otherwise a single mis-correlation is frozen for the whole run.
func TestExactNameCorrectsCorrelatedName(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withName("desc BETA")) // hooks: wrong
	if got := f.m.Snapshot(); len(got.Agents) != 1 || got.Agents[0].Name != "desc BETA" {
		t.Fatalf("setup: want the correlated name first, got %+v", got.Agents)
	}

	f.apply(0, event.AgentSpawned, "a1", func(ev *event.Event) {
		ev.AgentName = "desc ALPHA"
		ev.NameExact = true
	})
	w := f.m.Snapshot()
	if w.Agents[0].Name != "desc ALPHA" {
		t.Errorf("exact name must correct the correlated one: got %q", w.Agents[0].Name)
	}

	// And an exact name is never downgraded by a later correlated one.
	f.apply(0, event.AgentStarted, "a1", withName("desc GAMMA"))
	if w = f.m.Snapshot(); w.Agents[0].Name != "desc ALPHA" {
		t.Errorf("correlated name must not overwrite an exact one: got %q", w.Agents[0].Name)
	}
}
