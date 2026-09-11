package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// quietWorld is a world holding n suppressed agents and nothing else: the
// shape session 6dbbfaa6 left behind once its real subagents had settled.
func quietWorld(n int, sessionState state.SessionState, now time.Time) state.World {
	w := state.World{
		Session:   state.Session{ID: "6dbbfaa6", ShortID: "6dbb", State: sessionState},
		Main:      state.Main{Activity: "waiting", LastEventAt: now.Add(-4 * time.Second)},
		Connected: true,
	}
	for i := 0; i < n; i++ {
		w.Quiet = append(w.Quiet, state.QuietAgent{
			ID:     fmt.Sprintf("f%016x", i),
			Type:   "general-purpose",
			Status: state.StatusDone,
		})
	}
	return w
}

// TestQuietStatedOnEveryScreen is FINDING 2: the idle, condensed and tiny
// screens are all reachable while World.Quiet is non-empty, and not one of
// them may claim there are no subagents.
func TestQuietStatedOnEveryScreen(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	const n = 40
	count := quietCountText(n)

	screens := []struct {
		name       string
		sess       state.SessionState
		cols, rows int
		want       string
	}{
		{"idle (session idle)", state.SessionIdle, 64, 44, count},
		{"idle (nothing has earned a row yet)", state.SessionActive, 64, 44, count},
		{"condensed (<12 rows)", state.SessionActive, 64, 10, count},
		{"tiny (<40 cols)", state.SessionActive, 38, 10, fmt.Sprintf("+%d", n)},
	}
	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			w := quietWorld(n, sc.sess, now)
			v := newUIState(testConfig())
			frame, _ := renderFrame(w, v, sc.cols, sc.rows, now, NewPalette(2, false))
			frame = stripANSI(frame)
			if !strings.Contains(frame, sc.want) {
				t.Errorf("the %d suppressed agents were never stated (%q missing)\n%s", n, sc.want, frame)
			}
			if strings.Contains(frame, copyNoSubagents) {
				t.Errorf("the pane claimed %q while the machine held %d agents\n%s", copyNoSubagents, n, frame)
			}
		})
	}
}

// TestQuietIdleScreenStillSaysNoSubagentsWhenTrue: the honest claim survives.
// An idle session that really has nothing suppressed keeps §3.8's copy.
func TestQuietIdleScreenStillSaysNoSubagentsWhenTrue(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	w := quietWorld(0, state.SessionIdle, now)
	v := newUIState(testConfig())
	frame, _ := renderFrame(w, v, 64, 44, now, NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), copyNoSubagents) {
		t.Errorf("an idle session with nothing suppressed lost its §3.8 copy\n%s", stripANSI(frame))
	}
}

// TestQuietSelectionOnlyWhereDrawn: j/k must never land on a line that was not
// painted. The count is a row on every screen that has space for one, and the
// header only on the <40-column screen — where it is not selectable.
func TestQuietSelectionOnlyWhereDrawn(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	w := quietWorld(40, state.SessionActive, now)

	for _, geom := range []struct {
		name       string
		cols, rows int
		wantSel    bool
	}{
		{"wide tree", 64, 44, true},
		{"condensed", 64, 10, true},
		{"tiny", 38, 10, false},
	} {
		t.Run(geom.name, func(t *testing.T) {
			v := newUIState(testConfig())
			// Selection IS the paint (selectableIDs), so the two can no longer
			// disagree; what this pins is that the line is painted exactly
			// where the geometry has room for it.
			_, meta := renderFrame(w, v, geom.cols, geom.rows, now, NewPalette(2, false))
			ids := selectableIDs(meta)
			selectable := false
			for _, id := range ids {
				if id == selQuiet {
					selectable = true
				}
			}
			if selectable != geom.wantSel {
				t.Fatalf("selQuiet in selection = %v, want %v (%v)", selectable, geom.wantSel, ids)
			}
		})
	}
}

// TestQuietIdleScreenExpands: selecting the count on the idle screen names the
// agents behind it, exactly as it does on the tree (§1.5).
func TestQuietIdleScreenExpands(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	w := quietWorld(40, state.SessionIdle, now)
	v := newUIState(testConfig())
	v.SelID = selQuiet
	frame, _ := renderFrame(w, v, 64, 44, now, NewPalette(2, false))
	frame = stripANSI(frame)
	first := placeholderName(w.Quiet[0].ID, w.Quiet[0].Type)
	if !strings.Contains(frame, first) {
		t.Errorf("expanding the idle screen's suppressed line did not name %q\n%s", first, frame)
	}
	if !strings.Contains(frame, fmt.Sprintf("+%d more", 40-quietPreview)) {
		t.Errorf("the expanded list did not account for the rest\n%s", frame)
	}
}

// TestPlaceholderNamesUniqueAcrossARun is FINDING 4: forty announced agents
// must produce forty distinguishable rows in the expanded quiet list, whatever
// shape the platform's ids take — the observed phantom ids shared every
// character but the last two.
func TestPlaceholderNamesUniqueAcrossARun(t *testing.T) {
	shapes := map[string]func(i int) string{
		"phantom ids sharing a long prefix": func(i int) string { return fmt.Sprintf("f%016x", i) },
		"hex ids of the captured shape":     func(i int) string { return fmt.Sprintf("a%02xb2c3d4e5f6071", i) },
		"ids sharing a suffix":              func(i int) string { return fmt.Sprintf("%04d-agent-session", i) },
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			seen := map[string]string{}
			for i := 0; i < 40; i++ {
				id := shape(i)
				got := placeholderName(id, "general-purpose")
				if prev, dup := seen[got]; dup {
					t.Fatalf("placeholder %q is shared by %q and %q", got, prev, id)
				}
				seen[got] = id
				if again := placeholderName(id, "general-purpose"); again != got {
					t.Fatalf("placeholder for %q is not stable: %q then %q", id, got, again)
				}
			}
		})
	}

	// The type still leads, and the discriminator is the §3.16 character set.
	got := placeholderName("a1b2c3d4e5f60718a", "Explore")
	if !strings.HasPrefix(got, "explore-") {
		t.Errorf("placeholder = %q, want the agent_type to lead", got)
	}
	for _, r := range got {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			t.Errorf("placeholder %q contains %q, outside the §3.16 set", got, r)
		}
	}
	if n := placeholderName("", ""); n != "agent" {
		t.Errorf("placeholder with nothing known = %q, want %q", n, "agent")
	}
}
