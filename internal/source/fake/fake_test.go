package fake

import (
	"context"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

func closedTimeCh() <-chan time.Time {
	ch := make(chan time.Time)
	close(ch)
	return ch
}

// replayAll drains the whole script with a zero-duration virtual clock.
func replayAll(t *testing.T, opts Options) []event.Event {
	t.Helper()
	src := New(opts)
	src.after = func(time.Duration) <-chan time.Time { return closedTimeCh() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	want := len(script())
	out := make([]event.Event, 0, want)
	for ev := range ch {
		out = append(out, ev)
		if len(out) == want {
			cancel()
		}
	}
	if len(out) != want {
		t.Fatalf("replayed %d events, want %d", len(out), want)
	}
	return out
}

func TestDeterministicReplay(t *testing.T) {
	// Two replays through the two instant paths (seek past the end vs
	// infinite speed) must produce identical event slices.
	a := replayAll(t, Options{SeekSeconds: SnapshotSeconds + 60})
	b := replayAll(t, Options{Speed: math.Inf(1)})
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			t.Fatalf("event %d differs:\n a=%+v\n b=%+v", i, a[i], b[i])
		}
	}
	// Pointer fields must be freshly allocated per replay so no consumer
	// can mutate another replay's state.
	for i := range a {
		if a[i].Ask != nil && a[i].Ask == b[i].Ask {
			t.Fatalf("event %d: Ask payload aliased between replays", i)
		}
		if a[i].ExitCode != nil && a[i].ExitCode == b[i].ExitCode {
			t.Fatalf("event %d: ExitCode aliased between replays", i)
		}
	}
}

func TestSeekEquivalentToPlaying(t *testing.T) {
	const horizon = 90 * time.Second

	// Seek side: burst until the source asks for its first sleep.
	seekSrc := New(Options{SeekSeconds: 90})
	sleepReq := make(chan time.Duration)
	seekSrc.after = func(d time.Duration) <-chan time.Time {
		sleepReq <- d
		return nil // block until ctx cancel
	}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	sch, err := seekSrc.Events(sctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var burst []event.Event
seekLoop:
	for {
		select {
		case ev := <-sch:
			burst = append(burst, ev)
		case <-sleepReq:
			break seekLoop
		}
	}
	scancel()

	// Play side: run from t=0 on a virtual clock and stop watching once the
	// next sleep would cross the horizon.
	playSrc := New(Options{Speed: 1})
	req := make(chan time.Duration)
	fire := make(chan time.Time)
	playSrc.after = func(d time.Duration) <-chan time.Time {
		req <- d
		return fire
	}
	pctx, pcancel := context.WithCancel(context.Background())
	defer pcancel()
	pch, err := playSrc.Events(pctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var played []event.Event
	virtual := time.Duration(0)
playLoop:
	for {
		select {
		case ev := <-pch:
			played = append(played, ev)
		case d := <-req:
			if virtual+d > horizon {
				break playLoop
			}
			virtual += d
			fire <- time.Time{}
		}
	}
	pcancel()

	if len(burst) == 0 {
		t.Fatal("seek burst is empty")
	}
	if !reflect.DeepEqual(burst, played) {
		t.Fatalf("seek 90 emitted %d events, playing 90s emitted %d; sequences differ", len(burst), len(played))
	}
	// Both must be exactly the script prefix at or before the horizon.
	var want []event.Event
	for _, ev := range script() {
		if !ev.At.After(epoch.Add(horizon)) {
			want = append(want, ev)
		}
	}
	if !reflect.DeepEqual(burst, want) {
		t.Fatalf("burst has %d events, want the %d-event script prefix <= 90s", len(burst), len(want))
	}
}

func TestPacingHonorsSpeed(t *testing.T) {
	all := script()
	total := all[len(all)-1].At.Sub(epoch)
	cases := []struct {
		name  string
		speed float64
		want  time.Duration
	}{
		{"zero defaults to 1x", 0, total},
		{"explicit 1x", 1, total},
		{"2x halves the wall time", 2, total / 2},
		{"infinite speed sleeps zero", math.Inf(1), 0},
		{"NaN defaults to 1x", math.NaN(), total},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := New(Options{Speed: tc.speed})
			var mu sync.Mutex
			var slept time.Duration
			src.after = func(d time.Duration) <-chan time.Time {
				mu.Lock()
				slept += d
				mu.Unlock()
				return closedTimeCh()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := src.Events(ctx)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			n := 0
			for range ch {
				n++
				if n == len(all) {
					cancel()
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if slept != tc.want {
				t.Fatalf("total virtual sleep %v, want %v", slept, tc.want)
			}
		})
	}
}

func TestPauseStepResume(t *testing.T) {
	src := New(Options{Paused: true, SeekSeconds: SnapshotSeconds + 60})
	src.after = func(time.Duration) <-chan time.Time { return closedTimeCh() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	all := script()

	// Paused: no send is possible, so a non-blocking read must find nothing.
	select {
	case ev := <-ch:
		t.Fatalf("paused source emitted %v", ev.Kind)
	default:
	}

	// Each Step releases exactly the next scripted event.
	for i := 0; i < 3; i++ {
		src.Step()
		got := <-ch
		if !reflect.DeepEqual(got, all[i]) {
			t.Fatalf("step %d: got %+v, want %+v", i, got, all[i])
		}
		select {
		case ev := <-ch:
			t.Fatalf("step %d released a second event %v", i, ev.Kind)
		default:
		}
	}

	// Pause again is idempotent; Resume drains the rest.
	src.Pause()
	src.Resume()
	got := 3
	for range ch {
		got++
		if got == len(all) {
			cancel()
		}
	}
	if got != len(all) {
		t.Fatalf("drained %d events after resume, want %d", got, len(all))
	}
}

func TestStepWhileRunningPauses(t *testing.T) {
	all := script()
	// Count the events at t=0: they are emitted without any sleep.
	zero := 0
	for _, ev := range all {
		if ev.At.Equal(epoch) {
			zero++
		}
	}
	if zero == 0 || zero >= len(all) {
		t.Fatalf("script needs a t=0 prefix and later events, got %d/%d", zero, len(all))
	}

	src := New(Options{})
	fire := make(chan time.Time)
	src.after = func(time.Duration) <-chan time.Time { return fire }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for i := 0; i < zero; i++ {
		got := <-ch
		if !reflect.DeepEqual(got, all[i]) {
			t.Fatalf("event %d: got %+v, want %+v", i, got, all[i])
		}
	}
	// The runner is now blocked in its first sleep. Step while running must
	// pause, so releasing the sleep may not emit anything.
	src.Step()
	fire <- time.Time{}
	select {
	case ev := <-ch:
		t.Fatalf("paused-by-step source emitted %v", ev.Kind)
	default:
	}
	// A second Step releases exactly the held event.
	src.Step()
	got := <-ch
	if !reflect.DeepEqual(got, all[zero]) {
		t.Fatalf("after step: got %+v, want %+v", got, all[zero])
	}
}

func TestEventsSecondCallFails(t *testing.T) {
	src := New(Options{Paused: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := src.Events(ctx); err != nil {
		t.Fatalf("first Events: %v", err)
	}
	if _, err := src.Events(ctx); err == nil {
		t.Fatal("second Events call succeeded, want error")
	}
}

func TestChannelClosesOnCancel(t *testing.T) {
	src := New(Options{SeekSeconds: SnapshotSeconds + 60})
	src.after = func(time.Duration) <-chan time.Time { return closedTimeCh() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	n := 0
	for range ch {
		n++
		if n == len(script()) {
			cancel()
		}
	}
	// Reaching here means the channel closed after cancel.
	if n != len(script()) {
		t.Fatalf("read %d events, want %d", n, len(script()))
	}
}

func TestSourceName(t *testing.T) {
	if got := New(Options{}).Name(); got != "fake" {
		t.Fatalf("Name() = %q, want %q", got, "fake")
	}
}

func TestDemoSessions(t *testing.T) {
	sessions := DemoSessions()
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	attached := 0
	for _, s := range sessions {
		if s.Attached {
			attached++
			if !s.Live || s.ShortID != "3f9c" || s.ID != sessionID {
				t.Errorf("attached session must be the live demo session, got %+v", s)
			}
		}
		if s.ShortID == "" || s.Dir == "" {
			t.Errorf("incomplete session %+v", s)
		}
	}
	if attached != 1 {
		t.Fatalf("%d attached sessions, want exactly 1", attached)
	}
	last := sessions[2]
	if last.Live || last.EndedAgo != 12*time.Minute {
		t.Errorf("third session must be ended 12m per §3.8a, got %+v", last)
	}
}
