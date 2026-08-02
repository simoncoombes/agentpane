// Package runstore persists finished runs per §2.8: the last history_runs
// (default 20) runs live under ~/.local/state/agentpane/runs/ and survive
// restart.
//
// Layout choice (the spec allows either): one JSONL file per run, named
// <start-unix-nanos, zero-padded to 20 digits>-<id>.jsonl, containing one
// JSON line. The zero-padded prefix makes lexical filename order equal
// chronological order, so pruning never has to parse file contents, and a
// half-written run can only ever corrupt its own file. If a file does hold
// several lines (a rewrite appended), the last valid line wins; invalid
// lines are skipped and counted, never fatal (C9).
//
// Run files are SESSION-SCOPED: a store with a Session writes and reads only
// runs/s-<session>/, so a pane pinned to one session can never render another
// session's history and can never have its own history pruned away by a busy
// sibling. The unscoped store (Session "") is the whole-store view — every
// session's subdirectory plus any flat files written before session scoping
// existed — and belongs to `agentpane runs`, never to a pinned pane: a flat
// file records no session, and guessing which session produced it would be
// exactly the invention this store must not make (C8).
package runstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultHistoryRuns matches the config default history_runs = 20 (§7, §2.8).
const DefaultHistoryRuns = 20

// AgentSummary is the per-agent slice of a persisted run (§2.8).
type AgentSummary struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration"`
	Status   string        `json:"status"`
	Tokens   int           `json:"tokens"`
}

// Run is one persisted run record (§2.8): UserPromptSubmitted → all agents
// settled + SessionIdle. Session records which session produced the run;
// it is empty only for files written before runs were session-scoped, whose
// provenance is genuinely unknown.
type Run struct {
	ID          string         `json:"id"`
	Session     string         `json:"session,omitempty"`
	Start       time.Time      `json:"start"`
	End         time.Time      `json:"end"`
	Agents      []AgentSummary `json:"agents"`
	Outcome     string         `json:"outcome"`
	Contentions int            `json:"contentions"`
	Stalls      int            `json:"stalls"`
}

// Store reads and writes run records under Dir. The zero value plus a Dir is
// ready to use; HistoryRuns ≤ 0 means DefaultHistoryRuns.
//
// Session scopes every read, write and prune to runs/s-<session>/. The empty
// Session is the whole-store view (see the package comment): it lists flat
// legacy files plus every session subdirectory, and it prunes only the flat
// files — one session's Append can never delete another's history.
type Store struct {
	Dir         string
	HistoryRuns int
	Session     string
}

// sessionDirPrefix marks a per-session subdirectory of Dir. The prefix keeps
// the name distinguishable from anything else that may sit in the run
// directory and from the store's own ".run-*.tmp" temporaries.
const sessionDirPrefix = "s-"

// scopeDir is the single directory this store reads, writes and prunes.
func (st *Store) scopeDir() string {
	if st.Session == "" {
		return st.Dir
	}
	return filepath.Join(st.Dir, sessionDirPrefix+sanitizeID(st.Session))
}

// DefaultDir returns ~/.local/state/agentpane/runs (honouring
// $XDG_STATE_HOME), the directory §2.8 names.
func DefaultDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "agentpane", "runs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agentpane", "runs"), nil
}

func (st *Store) historyRuns() int {
	if st.HistoryRuns > 0 {
		return st.HistoryRuns
	}
	return DefaultHistoryRuns
}

// Append persists run as its own JSONL file (atomic tmp+rename) inside this
// store's scope and prunes that scope to the newest historyRuns run files. An
// empty ID is derived from the start timestamp so every record stays
// addressable by Load; an empty Session is stamped from the store's scope so
// the record carries its own provenance.
func (st *Store) Append(run Run) error {
	if st.Dir == "" {
		return fmt.Errorf("runstore: empty Dir")
	}
	ts := run.Start.UnixNano()
	if run.Start.IsZero() || ts < 0 {
		ts = 0
	}
	if run.ID == "" {
		run.ID = strconv.FormatInt(ts, 36)
	}
	if run.Session == "" {
		run.Session = st.Session
	}
	dir := st.scopeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.jsonl", ts, sanitizeID(run.ID))
	tmp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName) //nolint:errcheck
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return err
	}
	return st.prune()
}

// prune deletes the oldest run files beyond historyRuns from this store's
// scope only. Only files matching the store's own naming pattern are touched;
// foreign files, and every other session's subdirectory, are left alone.
func (st *Store) prune() error {
	dir := st.scopeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && isRunFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= st.historyRuns() {
		return nil
	}
	sort.Strings(names) // zero-padded prefix: lexical == chronological
	var firstErr error
	for _, name := range names[:len(names)-st.historyRuns()] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// located pairs a parsed run with the file it came from, for the filename
// tie-break in List.
type located struct {
	run  Run
	name string
}

// List returns the readable runs in this store's scope, newest-first (by
// Start, then filename), and the number of corrupt lines skipped along the
// way. A scoped store lists exactly one session's runs; the unscoped store
// lists every session's plus any flat legacy files. A missing directory is an
// empty history, not an error.
func (st *Store) List() ([]Run, int, error) {
	found, skipped, err := collect(st.scopeDir())
	if err != nil {
		return nil, 0, err
	}
	if st.Session == "" {
		// Whole-store view: fold in each session's subdirectory. One
		// unreadable session directory is not fatal (C9).
		entries, _ := os.ReadDir(st.Dir) //nolint:errcheck // absent/unreadable = nothing to fold in
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), sessionDirPrefix) {
				continue
			}
			sub, bad, err := collect(filepath.Join(st.Dir, e.Name()))
			skipped += bad
			if err != nil {
				continue
			}
			found = append(found, sub...)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].run.Start.Equal(found[j].run.Start) {
			return found[i].run.Start.After(found[j].run.Start)
		}
		return found[i].name > found[j].name
	})
	runs := make([]Run, len(found))
	for i, f := range found {
		runs[i] = f.run
	}
	return runs, skipped, nil
}

// collect reads the run files directly inside dir, never recursing. A missing
// directory yields nothing, not an error.
func collect(dir string) ([]located, int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var found []located
	skipped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		run, bad, ok := readRunFile(filepath.Join(dir, e.Name()))
		skipped += bad
		if ok {
			found = append(found, located{run: run, name: e.Name()})
		}
	}
	return found, skipped, nil
}

// Load returns the run with the given ID, if present.
func (st *Store) Load(id string) (Run, bool, error) {
	runs, _, err := st.List()
	if err != nil {
		return Run{}, false, err
	}
	for _, r := range runs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return Run{}, false, nil
}

// readRunFile scans one JSONL file. The last valid JSON-object line wins;
// non-empty lines that fail to parse are counted, never fatal (C9).
func readRunFile(path string) (run Run, skipped int, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return Run{}, 0, false
	}
	defer f.Close() //nolint:errcheck // read-only
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Run
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &r) != nil {
			skipped++
			continue
		}
		run, ok = r, true
	}
	if sc.Err() != nil && !ok {
		// Unreadable remainder with nothing valid before it: one corrupt file.
		return Run{}, skipped + 1, false
	}
	return run, skipped, ok
}

// isRunFile reports whether name matches %020d-<id>.jsonl.
func isRunFile(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") || len(name) < 20+1+1+len(".jsonl") {
		return false
	}
	for i := 0; i < 20; i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return name[20] == '-'
}

// sanitizeID makes an ID safe as a filename fragment.
func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, id)
}
