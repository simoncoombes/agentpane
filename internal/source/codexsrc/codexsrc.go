package codexsrc

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// defaultPollInterval matches the transcript source: a rollout file is written
// as the turn proceeds, so this is the resolution of everything downstream.
const defaultPollInterval = 400 * time.Millisecond

// DefaultDir is where Codex writes its rollout files.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// Session is one Codex run: a root thread and the subagent threads it spawned,
// all sharing the root's session_id.
type Session struct {
	ID      string // the root thread's id, which every thread reports
	CWD     string
	Started time.Time
	Updated time.Time
	Threads []Thread
}

// Thread is one rollout file: a root agent or one subagent.
type Thread struct {
	ID       string
	Path     string
	Root     bool
	Name     string
	Parent   string
	Nickname string
	Started  time.Time
}

// Scan reads the first line of every rollout file under dir and groups the
// threads into sessions.
//
// The first line is session_meta and it names the thread, its parent and its
// session, so the whole tree is discoverable without opening a database or
// reading a byte of anyone's conversation. Files whose first line is unreadable
// are skipped rather than guessed at.
//
// This form has no cache and is for one-shot callers (the CLI, tests). A poll
// loop wants scanner.scan, which remembers what it has already read.
func Scan(dir string, since time.Duration, now time.Time) ([]Session, error) {
	return (&scanner{}).scan(dir, since, now)
}

// scanner is Scan with a memory of the meta lines it has parsed.
//
// A session_meta line is the first line of the file and is written once, at
// creation: it cannot change while the thread runs. Re-reading 64KB off the
// front of every rollout file in the window, four hundred milliseconds apart,
// for a line that is already known, is the whole cost of the poll — a busy day
// leaves hundreds of files in a twelve-hour window. Only successful reads are
// remembered; a file caught mid-creation, with no complete first line yet, is
// retried on the next pass rather than written off.
type scanner struct {
	meta map[string]cachedMeta
}

type cachedMeta struct {
	m  meta
	at time.Time
}

func (sc *scanner) readMeta(p string) (meta, time.Time, bool) {
	if c, ok := sc.meta[p]; ok {
		return c.m, c.at, true
	}
	m, at, ok := readMeta(p)
	if ok {
		if sc.meta == nil {
			sc.meta = map[string]cachedMeta{}
		}
		sc.meta[p] = cachedMeta{m, at}
	}
	return m, at, ok
}

func (sc *scanner) scan(dir string, since time.Duration, now time.Time) ([]Session, error) {
	paths, err := rolloutFiles(dir, since, now)
	if err != nil {
		return nil, err
	}
	sc.forget(paths)
	bySession := map[string]*Session{}
	for _, p := range paths {
		m, at, ok := sc.readMeta(p)
		if !ok {
			continue
		}
		s := bySession[m.SessionID]
		if s == nil {
			s = &Session{ID: m.SessionID}
			bySession[m.SessionID] = s
		}
		root := m.ThreadSource != "subagent" && (m.ParentThreadID == "" || m.ID == m.SessionID)
		if root {
			s.CWD, s.Started = m.CWD, at
		}
		if at.After(s.Updated) {
			s.Updated = at
		}
		s.Threads = append(s.Threads, Thread{
			ID: m.ID, Path: p, Root: root, Name: agentName(m),
			Parent: m.ParentThreadID, Nickname: m.Nickname, Started: at,
		})
	}
	out := make([]Session, 0, len(bySession))
	for _, s := range bySession {
		sort.Slice(s.Threads, func(i, j int) bool { return s.Threads[i].Started.Before(s.Threads[j].Started) })
		out = append(out, *s)
	}
	// Newest first: the session a pane opened without being told which one to
	// watch is the one being worked in.
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// forget drops cache entries for files no longer in the window, so a
// long-running pane's map tracks the window rather than the whole history.
func (sc *scanner) forget(live []string) {
	if len(sc.meta) <= len(live) {
		return
	}
	keep := make(map[string]struct{}, len(live))
	for _, p := range live {
		keep[p] = struct{}{}
	}
	for p := range sc.meta {
		if _, ok := keep[p]; !ok {
			delete(sc.meta, p)
		}
	}
}

// rolloutFiles lists the rollout files touched within `since`. Codex files them
// under yyyy/mm/dd, so a walk is bounded by the day directories that can hold a
// recent file rather than by the whole history.
func rolloutFiles(dir string, since time.Duration, now time.Time) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner of the tree is not fatal (C9)
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if since > 0 {
			info, err := d.Info()
			if err != nil || now.Sub(info.ModTime()) > since {
				return nil
			}
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

func readMeta(p string) (meta, time.Time, bool) {
	f, err := os.Open(p)
	if err != nil {
		return meta{}, time.Time{}, false
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	n, _ := f.Read(buf) //nolint:errcheck // a short read is handled by the split below
	line, _, found := strings.Cut(string(buf[:n]), "\n")
	if !found {
		return meta{}, time.Time{}, false
	}
	var raw rawLine
	if json.Unmarshal([]byte(line), &raw) != nil || raw.Type != "session_meta" {
		return meta{}, time.Time{}, false
	}
	var m meta
	if json.Unmarshal(raw.Payload, &m) != nil || m.ID == "" {
		return meta{}, time.Time{}, false
	}
	return m, lineTime(raw.Timestamp, time.Time{}), true
}

// Source tails one Codex session's rollout files and implements event.Source.
type Source struct {
	dir      string
	session  string // "" = follow the newest session in the directory
	interval time.Duration
	clock    func() time.Time
	window   time.Duration

	mu    sync.Mutex
	files map[string]*tailState
	scan  scanner
	seq   uint64
}

type tailState struct {
	offset int64
	st     *threadState
}

// Option configures a Source.
type Option func(*Source)

// WithInterval sets the poll interval.
func WithInterval(d time.Duration) Option { return func(s *Source) { s.interval = d } }

// WithClock injects the clock (tests).
func WithClock(f func() time.Time) Option { return func(s *Source) { s.clock = f } }

// New builds a Source over dir, following session (or the newest when "").
func New(dir, session string, opts ...Option) *Source {
	s := &Source{
		dir: dir, session: session,
		interval: defaultPollInterval,
		clock:    time.Now,
		window:   12 * time.Hour,
		files:    map[string]*tailState{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements event.Source.
func (s *Source) Name() string { return "codex" }

// Events implements event.Source: one goroutine polling the session's files,
// closing the channel when ctx is done.
func (s *Source) Events(ctx context.Context) (<-chan event.Event, error) {
	out := make(chan event.Event, 256)
	go func() {
		defer close(out)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			for _, ev := range s.poll() {
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return out, nil
}

// poll reads whatever has been appended since the last pass, in thread order.
func (s *Source) poll() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	sessions, err := s.scan.scan(s.dir, s.window, now)
	if err != nil || len(sessions) == 0 {
		return nil
	}
	sess := sessions[0]
	if s.session != "" {
		found := false
		for _, c := range sessions {
			if c.ID == s.session {
				sess, found = c, true
				break
			}
		}
		if !found {
			return nil // pinned to a session that has not appeared yet
		}
	}

	var out []event.Event
	for _, th := range sess.Threads {
		out = append(out, s.readThread(th, now)...)
	}
	for i := range out {
		s.seq++
		out[i].Seq = s.seq
		out[i].SessionID = sess.ID
	}
	return out
}

func (s *Source) readThread(th Thread, now time.Time) []event.Event {
	ts := s.files[th.Path]
	if ts == nil {
		ts = &tailState{st: newThreadState()}
		s.files[th.Path] = ts
	}
	f, err := os.Open(th.Path)
	if err != nil {
		return nil
	}
	defer f.Close()

	if _, err := f.Seek(ts.offset, io.SeekStart); err != nil {
		return nil
	}
	buf, err := io.ReadAll(f)
	if err != nil || len(buf) == 0 {
		return nil
	}
	text := string(buf)
	// A trailing partial line is left for the next poll: half a JSON object is
	// not an event, and re-reading it costs one file offset.
	cut := strings.LastIndexByte(text, '\n')
	if cut < 0 {
		return nil
	}
	ts.offset += int64(cut + 1)

	var out []event.Event
	for _, line := range strings.Split(text[:cut], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, mapLine(ts.st, []byte(line), now)...)
	}
	return out
}
