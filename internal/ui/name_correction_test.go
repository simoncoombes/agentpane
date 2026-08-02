package ui

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

// A name the hooks correlated wrongly must be replaceable by the transcript's
// exact id↔description pairing. Table.Assign caches per id, so without the
// exact path the correction reached the model and never the screen.
func TestExactNameReplacesAGuessedSlug(t *testing.T) {
	v := newUIState(testConfig())

	guessed := []state.Agent{{ID: "a1", Name: "Explore the users schema", SpawnIndex: 0}}
	v.assignSlugsInSpawnOrder(guessed)
	first := v.Slugs.Assign("a1", guessed[0].Name)

	corrected := []state.Agent{{ID: "a1", Name: "Trace stall detection", NameExact: true, SpawnIndex: 0}}
	v.assignSlugsInSpawnOrder(corrected)
	got := v.Slugs.Assign("a1", corrected[0].Name)

	if got == first {
		t.Fatalf("exact name did not replace the guessed slug (still %q)", got)
	}
	if got != "trace-stall-det…" && got != "trace-stall-detection" {
		t.Errorf("slug = %q, want one derived from the corrected description", got)
	}

	// An exact slug is never disturbed again: names settle permanently.
	later := []state.Agent{{ID: "a1", Name: "Something else entirely", NameExact: true, SpawnIndex: 0}}
	v.assignSlugsInSpawnOrder(later)
	if again := v.Slugs.Assign("a1", later[0].Name); again != got {
		t.Errorf("an exact slug must not change again: %q -> %q", got, again)
	}
}

// The correction must reach EVERY namer, not just the full tree's
// assignSlugsInSpawnOrder: the condensed, tiny, idle and band paths call
// slugFor directly, and a plain Assign would pin a wrongly-correlated name for
// the rest of the run on exactly the geometries with least room to notice.
func TestSlugForAppliesTheCorrection(t *testing.T) {
	v := newUIState(testConfig())

	guessed := state.Agent{ID: "a1", Name: "Explore the users schema"}
	first := v.slugFor(guessed)

	corrected := state.Agent{ID: "a1", Name: "Trace stall detection", NameExact: true}
	got := v.slugFor(corrected)

	if got == first {
		t.Fatalf("slugFor kept the guessed name %q", got)
	}
	if !strings.HasPrefix(got, "trace-stall") {
		t.Errorf("slugFor = %q, want it derived from the corrected description", got)
	}
	// And it settles: a second exact name never moves it again.
	again := v.slugFor(state.Agent{ID: "a1", Name: "Something else", NameExact: true})
	if again != got {
		t.Errorf("an exact slug must not change again: %q -> %q", got, again)
	}
}
