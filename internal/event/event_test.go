package event

import "testing"

// sourceKinds is every kind a Source is allowed to emit; derivedKinds is
// every kind only the state machine may produce. Together they must be the
// whole taxonomy, with no overlap — the Derived() boundary is a `>=` on the
// iota block, so inserting a kind in the wrong place silently reclassifies
// half the taxonomy.
var sourceKinds = []Kind{
	SourceConnected, SourceDisconnected,
	SessionStart, SessionEnd, SessionIdle, UserPromptSubmitted,
	CompactStarted, CompactFinished, SessionTitled,
	AgentSpawned, AgentStarted, AgentReturned, AgentMerged,
	ToolStart, ToolEnd, ToolOutput, FileRead, FileEdited,
	TokensUpdated, ThinkingEnded,
	PermissionRequested, PermissionGranted, PermissionDenied,
	NotificationRaised, ErrorRaised,
}

var derivedKinds = []Kind{
	StallDetected, StallCleared, ContentionDetected, ContentionCleared,
	RunStarted, RunEnded, AgentDecayed, VerifyObserved, RetryInferred,
	CallOpened, CallClosed, AskDeduped, GhostAgentFolded,
}

func TestDerivedOnlyForDerivedKinds(t *testing.T) {
	for _, k := range sourceKinds {
		if k.Derived() {
			t.Errorf("%s: Derived() = true, want false (Apply drops derived-from-source)", k)
		}
	}
	for _, k := range derivedKinds {
		if !k.Derived() {
			t.Errorf("%s: Derived() = false, want true", k)
		}
	}
	if KindInvalid.Derived() {
		t.Error("KindInvalid: Derived() = true, want false")
	}
}

// Every kind in the iota block must be named, and no two may share a name.
func TestKindNamesCoverTheTaxonomy(t *testing.T) {
	all := append(append([]Kind{KindInvalid}, sourceKinds...), derivedKinds...)
	if len(kindNames) != len(all) {
		t.Fatalf("kindNames has %d entries, taxonomy has %d", len(kindNames), len(all))
	}
	seen := map[string]Kind{}
	for _, k := range all {
		name, ok := kindNames[k]
		if !ok {
			t.Errorf("kind %d has no name", k)
			continue
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("name %q used by kinds %d and %d", name, prev, k)
		}
		seen[name] = k
		if k.String() != name {
			t.Errorf("kind %d: String() = %q, want %q", k, k.String(), name)
		}
	}
	// The taxonomy is contiguous: no gaps between KindInvalid and the last
	// derived kind.
	if int(GhostAgentFolded) != len(all)-1 {
		t.Errorf("taxonomy is not contiguous: GhostAgentFolded = %d, want %d", GhostAgentFolded, len(all)-1)
	}
}

// SessionTitled is a real source event that must sort before the derived
// block, next to the other session-scoped kinds.
func TestSessionTitledIsASourceKindInTheSessionBlock(t *testing.T) {
	if SessionTitled.Derived() {
		t.Fatal("SessionTitled is emitted by the transcript source; Derived() must be false")
	}
	if SessionTitled >= AgentSpawned {
		t.Errorf("SessionTitled (%d) must sit in the session/transport block, before AgentSpawned (%d)", SessionTitled, AgentSpawned)
	}
	if SessionTitled <= SessionStart {
		t.Errorf("SessionTitled (%d) must sit after SessionStart (%d)", SessionTitled, SessionStart)
	}
}
