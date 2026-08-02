package hooksrc

import "testing"

// A duplicate SubagentStart must not buy a second description. corroborate()
// removes the id from starts, so without a tombstone the repeat looks new,
// re-registers with a fresh watermark, and claims a LATER agent's name —
// leaving that agent unnamed. Duplicate starts are real: hook connections are
// served concurrently, and a hook configured in both global and project
// settings fires twice.
func TestDuplicateStartCannotClaimTwice(t *testing.T) {
	m := NewMapper()
	const sess = "s1"

	// id1 starts and corroborates with no spawn pending: correctly unnamed.
	m.startSeen(sess, "id1", "Explore")
	if got := m.corroborate(sess, "id1"); got != "" {
		t.Fatalf("no spawn pending, want no name, got %q", got)
	}

	// A later spawn belongs to a different agent.
	m.remember(sess, pendingSpawn{desc: "desc BETA", typ: "Explore"})

	// The duplicate start for id1 must claim nothing.
	m.startSeen(sess, "id1", "Explore")
	if got := m.corroborate(sess, "id1"); got != "" {
		t.Fatalf("duplicate start stole a later agent's name: %q", got)
	}

	// The real owner still gets it.
	m.startSeen(sess, "id2", "Explore")
	if got := m.corroborate(sess, "id2"); got != "desc BETA" {
		t.Errorf("real owner lost its description: got %q, want %q", got, "desc BETA")
	}
}
