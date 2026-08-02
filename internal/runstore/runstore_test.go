package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

func mkRun(id string, start time.Time) Run {
	return Run{
		ID:    id,
		Start: start,
		End:   start.Add(4 * time.Minute),
		Agents: []AgentSummary{
			{Name: "fix-ts2345-fallout", Duration: 2 * time.Minute, Status: "done", Tokens: 44000},
			{Name: "run-auth-tests", Duration: 3 * time.Minute, Status: "done", Tokens: 12000},
		},
		Outcome:     "all merged",
		Contentions: 1,
		Stalls:      1,
	}
}

func TestAppendListRoundTrip(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	want := mkRun("r1", base)
	if err := st.Append(want); err != nil {
		t.Fatal(err)
	}
	runs, skipped, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	got := runs[0]
	if got.ID != "r1" || !got.Start.Equal(want.Start) || !got.End.Equal(want.End) ||
		got.Outcome != "all merged" || got.Contentions != 1 || got.Stalls != 1 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if len(got.Agents) != 2 || got.Agents[0].Name != "fix-ts2345-fallout" ||
		got.Agents[0].Duration != 2*time.Minute || got.Agents[0].Tokens != 44000 {
		t.Errorf("agents mismatch: %+v", got.Agents)
	}
}

func TestListNewestFirst(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	// Append out of chronological order on purpose.
	for _, min := range []int{2, 0, 3, 1} {
		if err := st.Append(mkRun(strings.Repeat("r", min+1), base.Add(time.Duration(min)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("len(runs) = %d, want 4", len(runs))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].Start.After(runs[i-1].Start) {
			t.Errorf("runs[%d].Start %v is newer than runs[%d].Start %v — want newest-first",
				i, runs[i].Start, i-1, runs[i-1].Start)
		}
	}
	if runs[0].Start != base.Add(3*time.Minute) {
		t.Errorf("newest run Start = %v, want %v", runs[0].Start, base.Add(3*time.Minute))
	}
}

func TestPruneToHistoryRuns(t *testing.T) {
	st := &Store{Dir: t.TempDir(), HistoryRuns: 3}
	for i := 0; i < 5; i++ {
		if err := st.Append(mkRun(
			"run-"+string(rune('a'+i)),
			base.Add(time.Duration(i)*time.Minute),
		)); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3 after prune", len(runs))
	}
	// The three newest survive, the two oldest are gone.
	if runs[0].ID != "run-e" || runs[1].ID != "run-d" || runs[2].ID != "run-c" {
		t.Errorf("surviving runs = %s, %s, %s; want run-e, run-d, run-c",
			runs[0].ID, runs[1].ID, runs[2].ID)
	}
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			files++
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
	if files != 3 {
		t.Errorf("run files on disk = %d, want 3", files)
	}
}

func TestPruneLeavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "notes.jsonl")
	if err := os.WriteFile(foreign, []byte("not a run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &Store{Dir: dir, HistoryRuns: 1}
	for i := 0; i < 3; i++ {
		if err := st.Append(mkRun("r"+string(rune('0'+i)), base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign file was pruned: %v", err)
	}
}

func TestCorruptToleranceSkipAndCount(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	if err := st.Append(mkRun("good", base)); err != nil {
		t.Fatal(err)
	}
	// A file holding garbage, a bare JSON null, and one valid run line.
	mixed := "garbage not json\n" +
		"null\n" +
		`{"id":"mixed","start":"2026-08-01T09:05:00Z","end":"2026-08-01T09:06:00Z","agents":null,"outcome":"ok","contentions":0,"stalls":0}` + "\n"
	if err := os.WriteFile(filepath.Join(st.Dir, "00000000000000000001-mixed.jsonl"), []byte(mixed), 0o644); err != nil {
		t.Fatal(err)
	}
	// A wholly corrupt file.
	if err := os.WriteFile(filepath.Join(st.Dir, "00000000000000000002-bad.jsonl"), []byte("\x00\x01broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs, skipped, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("len(runs) = %d, want 2 (good + mixed)", len(runs))
	}
	if runs[0].ID != "mixed" || runs[1].ID != "good" {
		t.Errorf("runs = %s, %s; want mixed (newer) then good", runs[0].ID, runs[1].ID)
	}
	// mixed: "garbage" + "null" = 2; bad file: 1.
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
}

func TestLoad(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	for i := 0; i < 3; i++ {
		if err := st.Append(mkRun("run-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := st.Load("run-b")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != "run-b" || !got.Start.Equal(base.Add(time.Minute)) {
		t.Errorf("Load(run-b) = %+v ok=%v", got, ok)
	}
	if _, ok, err := st.Load("nope"); err != nil || ok {
		t.Errorf("Load(nope) ok=%v err=%v, want not found without error", ok, err)
	}
}

func TestAppendDerivesEmptyID(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	if err := st.Append(Run{Start: base}); err != nil {
		t.Fatal(err)
	}
	runs, _, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID == "" {
		t.Errorf("empty ID was not derived: %+v", runs)
	}
}

func TestAppendSanitizesID(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	if err := st.Append(mkRun("../../etc/passwd run", base)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "/") || strings.Contains(e.Name(), " ") {
			t.Errorf("unsanitized filename %q", e.Name())
		}
	}
	// The record itself keeps the original ID for Load.
	if _, ok, _ := st.Load("../../etc/passwd run"); !ok {
		t.Error("run not loadable by its original ID")
	}
}

func TestListMissingDir(t *testing.T) {
	st := &Store{Dir: filepath.Join(t.TempDir(), "never-created")}
	runs, skipped, err := st.List()
	if err != nil || len(runs) != 0 || skipped != 0 {
		t.Errorf("missing dir: runs=%v skipped=%d err=%v, want empty history", runs, skipped, err)
	}
}

// --- session scoping ---
//
// The bug these cover: a pane pinned to session A rendered session B's run
// history under an "AGENTS · A" header, and B's runs pruned A's away.

func TestScopedStoreSeesOnlyItsOwnSession(t *testing.T) {
	dir := t.TempDir()
	a := &Store{Dir: dir, Session: "sess-aaaa"}
	b := &Store{Dir: dir, Session: "sess-bbbb"}
	if err := a.Append(mkRun("a1", base)); err != nil {
		t.Fatal(err)
	}
	// B's runs are newer, so an unscoped read would put them first.
	for i := 0; i < 3; i++ {
		if err := b.Append(mkRun("b"+string(rune('1'+i)), base.Add(time.Duration(i+1)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	runs, _, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "a1" {
		t.Fatalf("session A sees %+v, want only its own run a1", ids(runs))
	}
	if runs[0].Session != "sess-aaaa" {
		t.Errorf("run.Session = %q, want the writing session stamped on the record", runs[0].Session)
	}
	runs, _, err = b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("session B sees %v, want its own 3", ids(runs))
	}
	for _, r := range runs {
		if r.ID == "a1" {
			t.Fatal("session A's run leaked into session B's history")
		}
	}
}

func TestPruneNeverEvictsAnotherSession(t *testing.T) {
	dir := t.TempDir()
	a := &Store{Dir: dir, Session: "sess-aaaa", HistoryRuns: 2}
	b := &Store{Dir: dir, Session: "sess-bbbb", HistoryRuns: 2}
	if err := a.Append(mkRun("a1", base)); err != nil {
		t.Fatal(err)
	}
	// A busy sibling: far more runs than the shared history cap.
	for i := 0; i < 10; i++ {
		if err := b.Append(mkRun(fmt.Sprintf("b%d", i), base.Add(time.Duration(i+1)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "a1" {
		t.Fatalf("session A's history = %v after a busy sibling; want a1 intact", ids(runs))
	}
	if runs, _, _ := b.List(); len(runs) != 2 {
		t.Errorf("session B kept %d runs, want its own cap of 2", len(runs))
	}
}

// The `agentpane runs` view: everything, including files written before runs
// were session-scoped. Those carry no session and are never attributed to one.
func TestUnscopedStoreListsEverySessionPlusLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	legacy := &Store{Dir: dir} // pre-scoping writer: flat file, no session
	if err := legacy.Append(mkRun("old", base)); err != nil {
		t.Fatal(err)
	}
	a := &Store{Dir: dir, Session: "sess-aaaa"}
	b := &Store{Dir: dir, Session: "sess-bbbb"}
	if err := a.Append(mkRun("a1", base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := b.Append(mkRun("b1", base.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}

	all, skipped, err := (&Store{Dir: dir}).List()
	if err != nil || skipped != 0 {
		t.Fatalf("List: skipped=%d err=%v", skipped, err)
	}
	if got := ids(all); len(got) != 3 || got[0] != "b1" || got[1] != "a1" || got[2] != "old" {
		t.Fatalf("unscoped list = %v, want newest-first b1, a1, old", got)
	}
	for _, r := range all {
		if r.ID == "old" && r.Session != "" {
			t.Errorf("legacy flat file was attributed to session %q — provenance is unknown", r.Session)
		}
	}
	// And the pinned pane must not see the legacy file either.
	if runs, _, _ := a.List(); len(runs) != 1 || runs[0].ID != "a1" {
		t.Fatalf("pinned pane sees %v, want only a1 (a flat file has no known session)", ids(runs))
	}
}

func TestScopedLoadAndUnscopedLoad(t *testing.T) {
	dir := t.TempDir()
	a := &Store{Dir: dir, Session: "sess-aaaa"}
	b := &Store{Dir: dir, Session: "sess-bbbb"}
	if err := a.Append(mkRun("a1", base)); err != nil {
		t.Fatal(err)
	}
	if err := b.Append(mkRun("b1", base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	// `agentpane runs show <id>` is unscoped: any run is addressable.
	if got, ok, _ := (&Store{Dir: dir}).Load("a1"); !ok || got.ID != "a1" {
		t.Errorf("unscoped Load(a1) = %+v ok=%v", got, ok)
	}
	// A scoped store cannot reach another session's run.
	if _, ok, _ := a.Load("b1"); ok {
		t.Error("scoped Load reached another session's run")
	}
}

func TestSessionDirNameIsSanitized(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir, Session: "../../etc/passwd"}
	if err := st.Append(mkRun("r", base)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found = true
		// The name must stay one level inside Dir: no separators, and
		// cleaning the join must not walk out of the store.
		if strings.ContainsRune(e.Name(), filepath.Separator) {
			t.Errorf("session directory %q contains a path separator", e.Name())
		}
		if got := filepath.Dir(filepath.Clean(filepath.Join(dir, e.Name()))); got != filepath.Clean(dir) {
			t.Errorf("session directory %q escapes the store: parent %q, want %q", e.Name(), got, dir)
		}
	}
	if !found {
		t.Fatal("no session directory created")
	}
	if runs, _, _ := st.List(); len(runs) != 1 {
		t.Errorf("sanitized session dir is not readable back: %+v", runs)
	}
}

func ids(runs []Run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	if err := st.Append(mkRun("persist", base)); err != nil {
		t.Fatal(err)
	}
	// A fresh Store over the same dir sees the run (§2.8: survive restart).
	st2 := &Store{Dir: dir}
	got, ok, err := st2.Load("persist")
	if err != nil || !ok || got.ID != "persist" {
		t.Errorf("restart lost the run: %+v ok=%v err=%v", got, ok, err)
	}
}
