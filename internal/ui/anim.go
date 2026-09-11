package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// Motion in the pane.
//
// §3.13 allowed exactly one moving thing — the spinner — because an animated
// monitor is a monitor that draws the eye to whatever moved last rather than to
// whatever matters. That rule was written against decoration. It says nothing
// about the two moments where stillness is the problem: a tree that grows four
// rows between one frame and the next, and an agent that vanishes from one place
// and reappears in another. Both are the pane rearranging itself under a reader
// who has no idea what just moved or why.
//
// So the motion here is strictly transitional: fixed, short, never looping.
//
// Every transition is measured from the frame this pane FIRST PAINTED the fact,
// on the view's own clock (UIState.mark), and never from the timestamp the
// platform put on the event. The two are not the same clock and cannot be: the
// content clock jumps forward to event timestamps, the sources deliver in
// bursts, and a transcript poll that hands over three seconds of events at once
// moves it past the end of every animation window before the frame is drawn.
// Rows then "appeared out of nowhere" with all of this machinery running.
//
// The view clock is real time in a live pane and the content clock under
// --demo, so a replay stays deterministic; and UIState.Anim is off unless the
// Model turns it on, so every snapshot test and one-shot frame draws the
// finished picture.
const (
	// rowEnterFor bounds a block's arrival: past it, nothing is animating and
	// the pane goes back to sleep. It is deliberately longer than the sum of
	// the stagger and the wipe below, so the last line of a tall block finishes
	// drawing rather than snapping to complete when the window closes.
	rowEnterFor = 1100 * time.Millisecond
	// rowEnterLine staggers the block's lines so the strip grows into place
	// instead of appearing whole.
	rowEnterLine = 55 * time.Millisecond
	// rowDescendLine is how fast a block GROWS: one line every this long, each
	// arriving as bare trunk before anything is written on it. That is the
	// vertical stroke — the line leading down to the new agent, drawn — and it
	// is also what keeps the rows below from being shoved down five at once.
	rowDescendLine = 38 * time.Millisecond
	// rowCollapseLine is the same in reverse, as a returning block leaves.
	rowCollapseLine = 32 * time.Millisecond
	// rowEnterFade is how long each arriving line stays dim before it settles.
	rowEnterFade = 220 * time.Millisecond
	// rowWipeFor is how long a line takes to draw itself across the row. At
	// animFrame that is a dozen steps, which is what separates a line being
	// DRAWN from a line appearing in three chunks. The width never changes
	// while it draws — the undrawn part is spaces — so an arriving line cannot
	// move anything already on screen.
	rowWipeFor = 260 * time.Millisecond
	// rowEraseFor is the tail of the return hold: the row un-draws itself, from
	// the right back to the trunk, on its way down to the list.
	rowEraseFor = 240 * time.Millisecond

	// callFlashFor is how long a just-finished tool call reads at full weight
	// before it joins the dim history above it. It is the only mark that says
	// "this line is new" on a strip whose lines otherwise all look alike.
	callFlashFor = 800 * time.Millisecond

	// returnHoldFor is how long an agent that has just returned stays on the
	// tree, fading, before it drops into the list at the foot. Without the hold
	// a row simply disappears from one region and reappears in another between
	// two frames, which reads as two different things happening.
	returnHoldFor = 1200 * time.Millisecond
	// listArriveFor is the dim moment on the other side of that move: the row
	// arrives in the list quietly rather than snapping in.
	listArriveFor = 250 * time.Millisecond

	// askPulse is the period of the heartbeat on an unanswered ask: one beat a
	// second, which is the tick the pane already repaints on, so the loudest
	// thing in the pane costs nothing to run.
	askPulse = time.Second

	// askPressureFull is how long an ask has to go unanswered for its pressure
	// bar to fill. It is the config's notify_after by default (§3.20 level 4:
	// the point at which the pane escalates to a system notification), so the
	// bar fills exactly as the pane runs out of quieter ways to tell you.
	askPressureFull = 60 * time.Second
	// askPressureCells is the bar's width.
	askPressureCells = 5

	// pulseFor is how long a beat takes to travel from main down the trunk to
	// the agent that just reported something. Short on purpose: the pulse says
	// "the work is over there", and a slow one would still be crossing the tree
	// when the next event lands.
	pulseFor = 320 * time.Millisecond

	// animFrame is the repaint interval while any of the above is in flight.
	// 30fps: at 16 a 260ms wipe is four steps and the eye reads four states, not
	// one movement. It costs nothing at rest — the pane schedules these frames
	// only while a transition is running, and there is no loop to run when
	// nothing is arriving.
	animFrame = 33 * time.Millisecond
)

// enterHeight is how TALL a block is at elapsed d: the trunk grows down one
// line at a time, so the vertical leading to a new agent is drawn rather than
// appearing, and the rows below it move a line at a time instead of jumping the
// whole block's height in one frame.
//
// d is measured from the moment the stroke reaches this block, which is not the
// moment the row was first seen: the trunk has to travel down through the block
// ABOVE it first (renderTree.descend), because that block's own trunk column is
// what extends when a new agent is appended.
func enterHeight(d time.Duration, full int) int {
	if d < 0 {
		return 0
	}
	n := 1 + int(d/rowDescendLine)
	if n > full {
		return full
	}
	return n
}

// collapseHeight is the same in reverse: a block that has finished shrinks a
// line at a time, so what is below it rises smoothly.
func collapseHeight(left time.Duration, full int) int {
	if left <= 0 {
		return 0
	}
	n := 1 + int(left/rowCollapseLine)
	if n > full {
		return full
	}
	return n
}

// enterLines is how many of a block's lines have started being WRITTEN at
// elapsed d — the trunk arrives first (enterHeight) and the ink follows.
func enterLines(d time.Duration) int {
	if d < 0 {
		return 0
	}
	return 1 + int((d-rowDescendLine)/rowEnterLine)
}

// lineEnterAt is how long line lineIdx has been arriving: negative before its
// turn, growing after it.
func lineEnterAt(d time.Duration, lineIdx int) time.Duration {
	start := rowDescendLine + time.Duration(lineIdx-1)*rowEnterLine
	if start < 0 {
		start = 0
	}
	return d - start
}

// enterFading reports whether an arriving line is still dim.
func enterFading(d time.Duration, lineIdx int) bool {
	return lineEnterAt(d, lineIdx) < rowEnterFade
}

// revealFrac is how much of an arriving line has been drawn: 0 at its turn, 1
// when it is complete. The line draws itself left to right over rowWipeFor, on
// a smoothstep — a pen sets off, runs, and settles, where a linear ramp starts
// and stops dead and reads mechanical.
func revealFrac(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	if d >= rowWipeFor {
		return 1
	}
	t := float64(d) / float64(rowWipeFor)
	return t * t * (3 - 2*t)
}

// mark is the view's stopwatch: how long ago this pane first painted `key`,
// stamping it on first sight. ok is false when the view is not animating at all
// (a snapshot, a one-shot frame), and callers then draw the finished thing.
func (v *UIState) mark(key string) (time.Duration, bool) {
	return v.markAt(key, 0)
}

// markAt is mark with a delay before the stopwatch starts — the arrival
// cascade, and nothing else. A stopwatch that starts in the future reports a
// negative elapsed, which every caller reads as "not yet".
func (v *UIState) markAt(key string, delay time.Duration) (time.Duration, bool) {
	if v == nil || !v.Anim {
		return 0, false
	}
	if v.Marks == nil {
		v.Marks = map[string]time.Time{}
	}
	at, seen := v.Marks[key]
	if !seen {
		at = v.Wall.Add(delay)
		v.Marks[key] = at
	}
	return v.Wall.Sub(at), true
}

// marked reads a stopwatch without starting one — for the Model, which asks
// "is anything animating" outside the render.
func (v *UIState) marked(key string) (time.Duration, bool) {
	if v == nil || !v.Anim || v.Marks == nil {
		return 0, false
	}
	at, seen := v.Marks[key]
	if !seen {
		return 0, false
	}
	return v.Wall.Sub(at), true
}

// cascade staggers ROWS first painted in the same frame, so a fan-out of eight
// arrives one after another rather than in one jump — you can count them. It is
// capped so forty agents still finish arriving in well under a second.
//
// Only arrivals cascade. A beat or a call flash stamped in the same frame must
// start at once: delaying those was what left the beatever waiting for a turn that
// was not its own.
func (v *UIState) cascade() time.Duration {
	n := 0
	for key, at := range v.Marks {
		if strings.HasPrefix(key, "enter:") && !at.Before(v.Wall) {
			n++
		}
	}
	if n > enterCascadeMax {
		n = enterCascadeMax
	}
	return time.Duration(n) * enterCascade
}

const (
	// enterCascade is the delay between two rows first painted in the same
	// frame; enterCascadeMax caps how far that cascade may run.
	enterCascade    = 55 * time.Millisecond
	enterCascadeMax = 10
)

// The three stopwatches. The call key carries the agent's total call count, so
// a new call is a new key and gets its own flash.
//
// The enter key carries whether the agent has a NAME yet, which is what makes
// an arrival visible where it matters. Claude Code announces a subagent before
// it says what the subagent is for: the row exists, correctly, as
// `general-purpose-a1b2c3` — and the arrival animation ran there, on a row the
// reader had no reason to look at. The description lands seconds later over the
// hook correlation or the transcript sidecar, the row becomes `slow-walk`, and
// THAT is the moment a person calls "a new subagent started". So the row is
// drawn again when it gets its name.
func enterKey(a state.Agent) string {
	if a.Name == "" {
		return "enter:" + a.ID
	}
	return "enter:" + a.ID + ":named"
}
func doneKey(id string) string { return "done:" + id }
func callKey(a state.Agent) string {
	return "call:" + a.ID + ":" + itoa(a.TotalCalls)
}

// beatKey is one beat per thing the agent reported, not one per repaint.
func beatKey(a state.Agent) string {
	return "beat:" + a.ID + ":" + itoa(int(a.LastEventAt.UnixNano()/1e6))
}

// entering reports how far into its arrival a row is, from the frame this pane
// first drew it.
func entering(a state.Agent, v *UIState) (time.Duration, bool) {
	d, ok := v.markAt(enterKey(a), v.cascade())
	if !ok || d >= rowEnterFor {
		return 0, false
	}
	return d, true
}

// holding reports that an agent has returned and is still being shown on the
// tree, on its way down to the list at the foot.
func holding(a state.Agent, v *UIState) bool {
	if a.Status != state.StatusDone {
		return false
	}
	d, ok := v.mark(doneKey(a.ID))
	return ok && d < returnHoldFor
}

// leaving is how much of a returning row is still drawn as the pen runs back to
// the trunk at the end of the hold.
func leaving(a state.Agent, v *UIState) (float64, bool) {
	if !holding(a, v) {
		return 0, false
	}
	d, _ := v.marked(doneKey(a.ID))
	left := returnHoldFor - d
	if left >= rowEraseFor {
		return 1, false
	}
	if left < 0 {
		left = 0
	}
	t := float64(left) / float64(rowEraseFor)
	return t * t * (3 - 2*t), true
}

// arriving is how far a returned agent's row has been drawn in the foot list.
func arriving(a state.Agent, v *UIState) (float64, bool) {
	d, ok := v.marked(doneKey(a.ID))
	if !ok {
		return 1, false
	}
	if d -= returnHoldFor; d < 0 || d >= rowWipeFor {
		return 1, false
	}
	return revealFrac(d), true
}

// flashing reports that this call is the newest on the agent's strip and has
// just landed: how far its line has been drawn, and whether it still reads at
// full weight.
func flashing(a state.Agent, c state.Call, v *UIState) (draw float64, fresh bool) {
	n := len(a.CallHistory)
	if n == 0 {
		return 1, false
	}
	if last := a.CallHistory[n-1]; last.At != c.At || last.Tool != c.Tool {
		return 1, false
	}
	d, ok := v.mark(callKey(a))
	if !ok || d >= callFlashFor {
		return 1, false
	}
	return revealFrac(d), true
}

// askBeat is the heartbeat phase of an unanswered ask: true on the loud half of
// each second. Squarewave on the 1s tick, deliberately — a smooth pulse would
// need a repaint loop running for as long as the ask does, and the ask can
// outlive your lunch.
func askBeat(now time.Time) bool {
	return (now.UnixNano()/int64(askPulse))%2 == 0
}

// askPressure is how full the waiting bar is: 0 the instant an ask is raised,
// 1 by the time the pane has escalated as far as it can. It answers the
// question a bare `52s` cannot — is this new, or have I been ignoring it.
func askPressure(waited, full time.Duration) float64 {
	if full <= 0 {
		full = askPressureFull
	}
	if waited <= 0 {
		return 0
	}
	if waited >= full {
		return 1
	}
	return float64(waited) / float64(full)
}

// treePulse is the beat the tree carries down to an agent that is ARRIVING.
//
// It used to beat for activity too — one per thing any agent reported — and on a
// busy fan-out that is a cell running down the trunk more or less continuously.
// A signal that fires constantly is decoration: the rows already say who is
// working, second by second, and they say it where the work is. An arrival is
// the rare event a tree cannot show any other way, because the thing that
// changed is the shape of the tree itself.
func treePulse(w state.World, v *UIState) (string, float64, bool) {
	best, at := "", time.Duration(-1)
	for _, a := range w.Agents {
		d, ok := entering(a, v)
		if !ok || d < 0 || d >= pulseFor {
			continue
		}
		if at < 0 || d < at {
			best, at = a.ID, d
		}
	}
	if best == "" {
		return "", 0, false
	}
	return best, float64(at) / float64(pulseFor), true
}

// animActive reports whether anything on screen is mid-transition, so the Model
// knows to keep repainting at animFrame instead of waiting for the 1s tick.
// False is the steady state and costs nothing: the pane goes back to one frame
// a second the moment the last transition lands.
func animActive(w state.World, v *UIState) bool {
	if v == nil || !v.Anim {
		return false
	}
	for _, a := range w.Agents {
		// A row this pane has not drawn yet will animate on the next frame, so
		// keep the frames coming.
		if d, ok := v.marked(enterKey(a)); !ok || d < rowEnterFor {
			return true
		}
		if a.Status == state.StatusDone {
			if d, ok := v.marked(doneKey(a.ID)); !ok || d < returnHoldFor+rowWipeFor {
				return true
			}
		}
		// A flash and a beat are stamped BY the render, so an unstamped one is
		// not a transition waiting to happen — it is one that will start on the
		// frame the event itself causes. Treating "unstamped" as active here
		// meant a pane whose rows carry no history strip (the default) had a
		// call key it never stamped, and repainted at 30fps for ever.
		if d, ok := v.marked(callKey(a)); ok && d < callFlashFor {
			return true
		}
		if d, ok := v.marked(beatKey(a)); ok && d < pulseFor {
			return true
		}
	}
	return false
}
