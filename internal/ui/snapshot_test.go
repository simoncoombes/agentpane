package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/state"
)

// Snapshot tests: golden frames under testdata/. Regenerate with
//
//	UPDATE_SNAPSHOTS=1 go test ./internal/ui -run TestSnapshot
//
// PART 10's "three colour-scheme snapshots (green-on-black, light, NO_COLOR)"
// is satisfied by wide64 / narrow44 / nocolor: frames emit ANSI *indices*
// only (§3.10.1, enforced by TestSGRScanTreeFrame), so a green-on-black and a
// light scheme receive byte-identical frames — per-scheme goldens would
// duplicate demo_wide_64 exactly. Scheme *adaptation* (accent choice against
// the probed palette) is covered by term's TestAccentPick instead.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_SNAPSHOTS") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_SNAPSHOTS=1): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, string(want))
	}
}

func TestSnapshotDemoWide64(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "demo_wide_64", frame)
}

func TestSnapshotDemoNarrow44(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.WidthMode = "narrow"
	frame, _ := renderFrame(w, v, 44, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "demo_narrow_44", frame)
}

func TestSnapshotDemoNoColor(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, true))
	checkGolden(t, "demo_nocolor", frame)
}

// A realistically long session title (60 columns, verbatim from this
// machine) on main's row, budgeted at both spec widths. The demo's own label
// is the short "refactor auth" prompt, which never showed the failure mode.
func TestSnapshotMainTitleWide64(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.Main.Title = realTitle
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "main_title_64", frame)
}

func TestSnapshotMainTitleNarrow44(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.Main.Title = realTitle
	v.WidthMode = "narrow"
	frame, _ := renderFrame(w, v, 44, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "main_title_44", frame)
}

func TestSnapshotInspectorOnAskAgent(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.InspectorOpen = true // selection already sits on the ask agent
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "demo_inspector_ask", frame)
}

func TestSnapshotIdle(t *testing.T) {
	v := newUIState(testConfig())
	for _, s := range fake.DemoSessions() {
		v.Sessions = append(v.Sessions, SessionRow{
			ID: s.ID, ShortID: s.ShortID, Dir: s.Dir, Live: s.Live,
			Idle: s.Idle, Attached: s.Attached, Agents: s.Agents, EndedAgo: s.EndedAgo,
		})
	}
	end := demoNow().Add(-14 * time.Minute)
	w := state.World{
		Session:   state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionIdle},
		Connected: true,
		// The run this pane watched end — the §3.8 mock's numbers, now the
		// only run the idle screen has to show.
		LastRun: &state.Run{
			ID: "3f9c", SessionID: "3f9c1a2b", Prompt: "refactor auth",
			StartedAt: end.Add(-(4*time.Minute + 12*time.Second)), EndedAt: end,
			Tokens: 611000, Stalls: 1,
			Agents: []state.RunAgent{
				{ID: "a1", Name: "migrate writer", Duration: 2*time.Minute + 32*time.Second, Status: state.StatusDone, Tokens: 118000},
				{ID: "a2", Name: "api types", Duration: 2 * time.Minute, Status: state.StatusDone, Tokens: 84000},
				{ID: "a3", Name: "explore schema", Duration: 1*time.Minute + 28*time.Second, Status: state.StatusDone, Tokens: 61000},
				{ID: "a4", Name: "test runner", Duration: 3*time.Minute + 4*time.Second, Status: state.StatusDone, Tokens: 97000},
			},
		},
	}
	frame, _ := renderFrame(w, v, 64, 28, demoNow(), NewPalette(2, false))
	checkGolden(t, "idle", frame)
}

func TestSnapshotDisconnected(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.Connected = false
	v.DisconnectedAt = demoNow().Add(-2 * time.Second)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	checkGolden(t, "disconnected", frame)
}

func TestSnapshotTinyWidth(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 38, 16, demoNow(), NewPalette(2, false))
	checkGolden(t, "tiny_38", frame)
}

func TestSnapshotScroll40Agents(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(40, now)
	v := newUIState(testConfig())
	v.SelID = w.Agents[12].ID
	frame, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	checkGolden(t, "scroll_40_agents", frame)
}
