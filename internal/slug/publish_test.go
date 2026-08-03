package slug

import "testing"

// TestExactCorrectionIsSilentAndAtomic: a guessed slug may be corrected once by
// an exact name (§13.1(a)), and the correction is a single table write — one
// Assign call later the id has exactly one name, never two, and the old one is
// released for reuse rather than lingering as a reservation.
func TestExactCorrectionIsSilentAndAtomic(t *testing.T) {
	tab := New(18)
	guessed := tab.Assign("a1", "Typecheck the workspace packages")
	if guessed != "typecheck-works…" {
		t.Fatalf("guessed slug = %q", guessed)
	}
	corrected := tab.AssignExact("a1", "Run the auth test suite in watch mode")
	if corrected != "run-auth-tests" {
		t.Fatalf("exact name did not correct the slug: %q", corrected)
	}
	// Atomic: the id resolves to the new name from now on, with no intermediate
	// state to render.
	if again := tab.Assign("a1", "anything else"); again != corrected {
		t.Errorf("slug is not settled after the correction: %q", again)
	}
	// The released name is available again — a corrected slug must not hold a
	// reservation on a name nothing is called.
	if got := tab.Assign("a2", "Typecheck the workspace packages"); got != guessed {
		t.Errorf("the released slug was not reusable: %q, want %q", got, guessed)
	}
	// An exact slug is never disturbed a second time.
	if got := tab.AssignExact("a1", "Audit the users schema for missing indexes"); got != corrected {
		t.Errorf("an exact slug was replaced: %q", got)
	}
}

// TestPublishedSlugNeverRenames: once the guessed name has left the pane — a
// notification, the standup — it is the agent's name. The correction is kept for
// the inspector instead, because a name that changes under the user mid-read
// reads as two different agents (§13.1(a)).
func TestPublishedSlugNeverRenames(t *testing.T) {
	tab := New(18)
	guessed := tab.Assign("a1", "Typecheck the workspace packages")
	if tab.Published("a1") {
		t.Fatal("a slug is not published until someone says so")
	}
	tab.Publish("a1")
	if !tab.Published("a1") {
		t.Fatal("Publish did not stick")
	}

	if got := tab.AssignExact("a1", "Run the auth test suite in watch mode"); got != guessed {
		t.Errorf("a published slug renamed to %q — it must keep %q", got, guessed)
	}
	if got := tab.LateName("a1"); got != "Run the auth test suite in watch mode" {
		t.Errorf("the real name was not recorded for the inspector: %q", got)
	}
	// The frozen name still owns its reservation: nothing else may take it.
	if got := tab.Assign("a2", "Typecheck the workspace packages"); got == guessed {
		t.Errorf("another agent took the published name %q", got)
	}
	// Publishing twice is a no-op, and an unknown id cannot be published.
	tab.Publish("a1")
	tab.Publish("nobody")
	if tab.Published("nobody") {
		t.Error("published an id that has no slug")
	}
	if tab.LateName("a2") != "" {
		t.Error("LateName invented a correction for an agent that never had one")
	}
}

// A correction that arrives BEFORE publication still lands: the freeze is about
// what the user has already been told, not about how late the transcript is.
func TestCorrectionBeforePublicationStillApplies(t *testing.T) {
	tab := New(18)
	tab.Assign("a1", "Typecheck the workspace packages")
	if got := tab.AssignExact("a1", "Run the auth test suite in watch mode"); got != "run-auth-tests" {
		t.Fatalf("slug = %q, want the corrected name", got)
	}
	tab.Publish("a1")
	if got := tab.LateName("a1"); got != "" {
		t.Errorf("LateName = %q, want none — the correction was applied, not deferred", got)
	}
}
