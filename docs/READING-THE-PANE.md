# Reading the pane

Every mark in the pane means one thing and means it everywhere. This is the
full reference: what a row shows, what moves and why, what the numbers are
counted from, and what the pane does when an agent needs you.

## The main row

The root of the tree is one line:

```
◍ main  Build agentpane — live TUI monitor for Claude Code… 112k
│ write the migration                                         5s
```

The text next to `main` is a short **workstream label**, not your prompt. It
is taken, in order, from:

1. Claude Code's own session title (the one it shows in its tab), read from
   the transcript;
2. the current run's prompt, if no title has been written yet;
3. main's most recent observed activity;
4. nothing, if none of the above has been seen. agentpane never writes a
   summary of its own.

The label is then fitted to whatever space is left after `◍ main` and the
token count, cut on a word boundary with `…`. It is the first thing to give
way as the pane narrows - the token column never moves and the row never
wraps. The full untruncated text is always one keystroke away: select `main`
and press `⏎` to see it in the inspector, or `y` to copy it.

The second line is what main is **waiting on**, in this order: a blocked-on
summary (`holding 2 agents · waiting on you`), else its current todo item, else
the agents still running (`waiting on 3 agents`), else whatever it last did.

That third rule exists because Claude Code fires a Notification hook when its
own prompt goes idle - "Claude is waiting for your input" - which agentpane
records verbatim as main's activity. With a fan-out in progress that line sat
there for minutes flatly contradicting the tree beneath it. Main is waiting, on
its subagents; once they have all landed the notification is the true answer
again, and it is what the line falls through to.

## What an agent row shows

One line of what the agent is **doing right now**, under its name:

```
├─╮
│ ╰● run-auth-tests  opus-5 · high               ◐ in Bash 3m12s
│    ▸ Bash pnpm test --watch  ✖ verify failed         ▪▫··  38k
```

That is the whole row, for every agent. A tree's job is to say what everything
is doing at once, and eight agents saying it now fit in the rows that used to
hold three.

**History fills the rows nothing else wants.** When the tree has room to spare,
every agent grows a strip of its recent tool calls above its activity line, one
call at a time, up to `max_calls_shown`. One agent working alone in a tall pane
is the case this exists for: it used to draw three lines and leave thirty blank.

The strip is the first thing given back. As agents arrive it shrinks for
everybody at once, so no row is singled out, and it reaches zero before any row
loses the line saying what it is doing. Growth also stops three rows short of
the bottom, so the next agent to spawn has somewhere to land without collapsing
every strip on the frame it appears.

`space` pins a row's strip open regardless: an explicit per-row choice that
survives the pressure the shared strip gives way to. Either way the strip shows
up to `max_calls_shown` calls, newest at the bottom, with `+n more` for what was
cut.

For the whole log rather than the last few calls, `⏎` or a click opens the
inspector: every tool call, its output and its errors, for one agent at a time.

A history line says **what the call did**, not how it was spelled. When the tool
wrote a description of its own - the Bash tool's `description`, the same text
the `▸` line uses - that is the line:

```
│      ✔ Find test directories
│      ✔ Read the Cargo manifests
│    ▸ Rank test files by byte size                    ▪▫··  38k
```

Without one it falls back to the tool and its target, with paths cut to their
base name and `cd <dir> &&` preambles removed: `Bash cat pyproject.toml`, not
`Bash cd /Users/you/Dev/project && cat /Users/you/Dev/project/pyproject.toml`.
At 64 columns the absolute path is forty columns of identical text in front of
the only part that differs, so four calls read as one call four times. Nothing
is lost - `y` yanks the exact command, the inspector footer prints the full
path, and `o` opens it.

Every agent names its own model on its row, after the slug:

```
│ ╰● dep-audit  opus-5 · high                              3.8k/s
│ ╰● explore-schema  haiku-4-5 · high                      1.2k/s
```

It is drawn in **bright blue** — the one deliberate break from the single-hue
rule. That rule keeps the pane's *states* on one scale so nothing competes with
needs-you: live, settled and deep are degrees of the same thing. A model id is
not a state at all, and rendered as a degree of the state hue it read as a dim
state and was hard to read besides. A second hue says "different kind of fact"
in the one place there is one. If your accent is already blue the meta hue moves
to cyan; under `NO_COLOR` it is plain like everything else.

It rides in the collapsed tail, so a narrow row drops it before it touches the
slug or the number.

The four marks before it are the **stations**: the milestones the pane has
actually observed, in order - spawn, first tool, first edit, return. `▪` reached,
`▫` the one it is on, `·` not yet. They are facts, not a progress estimate;
nothing here infers how far through its work an agent is, because nothing can.
The row carries the marks alone - the word that used to name them ("first tool")
restated the mark beside it and cost ten columns of the activity text, and it
survives in the inspector where there is room for a gloss.

The right-hand column is the row's measurement and nothing else: the token burn
rate, or how long the row has been silent, or `◐ in Bash 3m12s` while a call is
open. The five-cell activity bar that used to sit in front of it is gone - it
was the loudest thing in the pane, repeated once per agent, and the exact
number beside it already said more. The inspector still plots an agent's
activity across the whole run.

Under pressure the activity line is what gives way, taken from the least urgent
rows first, so asks and stalls keep theirs longest. Only when all of that is
gone does the tree start scrolling.

## Below the tree: nothing

The tree owns every row between the header and the inspector. There is no
narration, no alert band and no contention block under it - those regions were
removed, and what was a negotiation over the leftover rows is now a subtraction.
A pane that used to spend ten or more rows on prose spends them on agents.

The alerts themselves are not gone, only their region. Three surfaces still
carry them:

- the header's `⚑ n NEEDS YOU` count, which is the first thing read;
- the `⚑` glyph and `blocked · <what it asked for>` line on the agent's own
  tree row;
- the inspector, which states the exact command the agent is waiting on.

Under 12 rows the condensed screen still draws a single one-line alert directly
under the header (`⚑ <slug> permission ×3`), because at that height the tree
cannot say it. `⏎` there jumps focus to the left pane.

## How wide the pane draws

The pane draws at whatever width it has, and redraws when you drag the divider.
There is nothing to switch on.

The extra columns go to text. The activity line, the tool-call labels and main's
workstream label are all budgeted against the width, so above 64 columns they
stop being truncated and things that had to be dropped to fit, like
`✖ verify failed` on the row it belongs to, come back.

It stops at 100 columns, because a row much wider than that is scanned twice
rather than read once. `max_width` in `config.toml` moves that ceiling.

`w` cycles to the two fixed budgets and back:

| Layout | Width |
|---|---|
| fill (default) | the pane, up to `max_width` (100) |
| wide | 64 columns |
| narrow | 44 columns |

Those budgets are ceilings, never floors, so every layout narrows with the
pane. Only widening past 64 or 44 is what they refuse, which is the point: the
tree was drawn to be read at a glance, and a fixed width means the row you
learned yesterday is the row you read today. The choice is remembered in
`~/.local/state/agentpane/ui.json`.

Below 40 columns the pane becomes a single-column screen whatever the layout,
and under 12 rows it becomes the condensed screen.

## Motion

§3.13 allowed one moving thing, the spinner, because an animated monitor draws
the eye to whatever moved last rather than to whatever matters. That rule is
about decoration. It says nothing about the moments where stillness is the
problem: a tree that grows four rows between one frame and the next, an agent
that disappears from one region and reappears in another, and a tree of
identical-looking rows where one of them is where the work is happening.

So there are four pieces of motion, all short, none of them a loop:

- **an agent arrives** - the trunk is drawn DOWN to it, starting in the block
  ABOVE it. Appending an agent re-draws that block too, whose trunk column goes
  from absent (it was the last one) to present; that column used to flip in a
  single frame, which reads as "it just appeared" however carefully the new block
  below is drawn. So the stroke travels: down through the previous block a line
  at a time, un-drawing its extension ahead of itself, then into the arriving
  block, which grows one line every ~38ms with each row arriving as bare trunk
  before anything is written on it.
  Then a pen writes them, each line ~55ms after the one above, drawn left to
  right over ~260ms with a shimmering `▏▎▍` tip at the frontier and the ten
  columns behind it still wet - in the pen's own colour, settling to their real
  style as it moves on. The agent's name wears the accent while it lands, its
  status dot lands as a spark, and both settle out of it; a beat runs down the
  trunk to meet it, so what you see is main dispatching an agent and the row
  appearing at the end of that. The undrawn part of a line is spaces, never a
  shorter line, so nothing to the right of the pen moves while a row is written -
  and because the block grows rather than appearing whole, the rows below move
  one row per frame instead of being shoved down five at once. A batch spawned in
  one message cascades, one agent every ~55ms, so eight agents arrive one after
  another instead of in one jump - you can see how many there were.
- **a call lands** - the new history line is drawn the same way and reads at full
  weight for ~0.8s before it settles into the dim history above.
- **an agent returns** - it stays on the tree for ~1.2s, and in the last ~240ms
  of that the pen runs backwards and un-draws it while the block collapses a line
  at a time, so what is below it rises smoothly; then it appears in the list at
  the foot, drawn in there. It is never in both places and never in neither.
- **a beat runs down the trunk** to an arriving agent, or failing that to
  whichever agent just reported something: one lit cell, travelling from `main`
  to that agent's branch in ~320ms. Exactly one exists at a time - the newest - because eight lit at once is a light show, not
  a signal. It rides the trunk that is already drawn, so nothing moves and
  nothing is added; it just says *the work is over there* before you have read a
  word.

A subagent is announced before anyone knows what it is **for**, so the row first
exists as `general-purpose-a1b2c3` - correctly, but on a row you have no reason
to be looking at, and that is where the arrival used to happen. The description
lands seconds later over the hook correlation or the transcript sidecar, the row
becomes `slow-walk`, and *that* is the moment a person calls "a new subagent
started". So the row is drawn again when it is named.

**Opening the pane draws the whole tree in**, cascading agent by agent, using
the same arrival every new row gets. `--render-once` skips it: one frame of a
tree caught mid-arrival is not a picture of anything.

**Every** transition is measured from the frame this pane first *painted* the
fact, on the view's own clock - never from the timestamp the platform put on the
event. The two are not the same clock and cannot be: the content clock jumps
forward to event timestamps and the sources deliver in bursts, so a transcript
poll handing over three seconds of events at once steps straight over every
animation window before a frame is drawn. That is why rows used to appear out of
nowhere with all of this machinery running.

The view clock is real time in a live pane and the content clock under `--demo`,
so a replay stays deterministic; and the transitions are off entirely unless the
Model turns them on, so every snapshot test and `--render-once` draws the
finished picture.

The motion is eased rather than linear - a pen sets off, runs and settles - and
runs at 30fps: at half that, a 260ms line is four steps and the eye reads four
states rather than one movement. While a transition is in flight the pane
repaints at that rate; the moment the last one lands it is back to one frame a
second.

## When something needs you

**How you action it:** in the session pane, not here. A subagent has no prompt
UI of its own - when its tool call needs permission, Claude Code surfaces the
request in the session you type in, and the subagent blocks until you answer.
agentpane is read-only by construction (`x` exists only to say so): it cannot
approve, deny, interrupt or kill anything. What it can do is make sure you know,
and get you there.

- `y` on the row yanks the exact command it is asking to run.
- `⇥` jumps focus to the session pane, where you answer it.
- The `⚑` clears itself a second or two later.

The other `⚑` is a **stall**, and there is no prompt behind that one: the agent
has gone quiet past `stuck_after`. Nothing to approve - the pane is telling you
to go and look, and what to do about it is yours to choose.

None of this happens in a permission mode that never asks (`bypassPermissions`),
where the only `⚑` you will ever see is a stall.


An unanswered permission request is the expensive failure mode - the run stops
and nothing tells you unless you are looking - so it is the one row that does
not hold still. Its `⚑` beats on the second, and the right-hand column carries
how long you have kept it waiting as pressure rather than as a number you have
to compare against a threshold you cannot see:

```
│ ╰⚑ fix-ts2345-fallout  ↺2                            ▇▇▇▇· 52s
```

The bar fills by `notify_after` (60s by default), which is the point at which
the pane escalates to a system notification - so it fills exactly as the pane
runs out of quieter ways to tell you. A stalled agent gets no bar: nobody is
blocking it, so there is no pressure to apply.

The header carries the run's own progress beside the session id: `▇▇▇··· 3/8`,
subagents returned over subagents that exist, teammates excluded.

## What "tokens" means here

The transcript is the only token source — no hook carries a count — and each
assistant message reports four numbers. agentpane sums three of them:
`input + output + cache_creation`. **`cache_read` is excluded**: it is the whole
prompt re-read from cache on every turn, it is far the largest of the four, and
it grows with the length of the conversation rather than with anything the agent
did. Counting it made a thirty-turn agent that had written a few thousand tokens
report 10M, and made the `k/s` burn rate track context size instead of output.

So the number on a row is *work done*: tokens read for the first time plus
tokens written. It is exact as far as the transcript reports it, with one
honest gap — a pane that attaches to a file already in progress cannot know the
total that came before, so it reports per-message deltas rather than inventing
a cumulative count.

The total on the right of the header is the **session's**: main plus every agent
on the tree and in the returned list. It is not the run's own counter, which only
starts at the prompt that began the run - a pane attached to a session already in
flight would then head a screenful of rows with a total smaller than they add up
to, which is the one thing a total may not do. The clock beside it is still the
run's: that is what "how long has this been going" means.

## What an agent is running on

Every agent names its own on its row, after the slug:

```
│ ╰● dep-audit  opus-5 · high                              3.8k/s
│ ╰● explore-schema  haiku-4-5 · high                      1.2k/s
```

It is drawn in **bright blue** — the one deliberate break from the single-hue
rule. That rule keeps the pane's *states* on one scale so nothing competes with
needs-you: live, settled and deep are degrees of the same thing. A model id is
not a state at all, and rendered as a degree of the state hue it read as a dim
state and was hard to read besides. A second hue says "different kind of fact"
in the one place there is one. If your accent is already blue the meta hue moves
to cyan; under `NO_COLOR` it is plain like everything else.

It rides in the collapsed tail, so a narrow row drops it before it touches the
slug or the number. The header carries the session's the same way
(`AGENTS 8 · 3f9c · opus-5 · high`), which is what `main` is running on.

The inspector has the full ids for the selected agent:

```
model claude-haiku-4-5 · effort high  (session: claude-opus-5)
```

Both come from the transcript (`message.model`, and the session's `effort`
field) - **no hook payload carries either**, so a pane running on hooks alone
says nothing here rather than guessing. The `(session: …)` suffix appears only
when a subagent is on a different model than the session it belongs to, which
is the one thing about that line a reader could be surprised by.

## `✖ verify failed`

That marker on an agent's activity line means: **this agent ran something
that looks like a check, and it exited non-zero.**

Only the failure is on the row. A check that PASSED is not news — it is the
expected thing, it is only ever an inference, and a `✓` next to the `✔` a
successful tool call gets says nothing a reader can act on. It cost row width to
report that nothing was wrong. The inspector carries both, labelled
`(inferred)`, because that is exactly what they are.

The inference is deliberately narrow. It fires only on a completed `Bash` call
whose command text matches `tests?|lint|type-check|vet|build`, or whose own
description says it was *running* one - `run the auth suite`, `check the types`.
The verb matters: "fix the failing tests" and "write a test for the parser"
describe work *about* tests and never earn the marker. The exit code alone
decides which of the two you get, and the latest outcome wins rather than the
first, so an agent that fixes a failing suite stops saying it failed.

What it is **not**: a claim that the agent's work is correct, or that any test
covers what it changed. agentpane never renders a verdict of its own (C8) - it
saw a command that named a check, and it saw the exit code.

## Returned agents move to a list

A subagent that has returned answers none of the three questions the pane
exists for, and on a run that fans out to forty the settled rows are what push
the live ones off the bottom. So a returned agent leaves the tree the moment it
lands and joins a list at the foot of it, newest first:

```
├─╮
│ ╰○ document-constructor  queued
╰─╮
  ╰◇ reviewer  limited telemetry

RETURNED 3
  quick-count                                               3:40
  readme-scan                                               3:33
  test-survey                                               2:31
```

An agent that returned without doing anything - no description, no tool call, no
tokens - is not listed. The list is what the run *finished*, and Claude Code
announces subagents that never do a thing; those are counted on the header
(`RETURNED 3 · 2 did nothing`) rather than dropped or given a row beside agents
that did some.

Those rows are still rows: `j`/`k` reach them, `⏎` or a click opens the log the
agent left behind, and the cursor follows an agent into the list when it
returns mid-read.

**The list holds ten rows and scrolls.** However tall the pane, it stops at ten:
the pane is for what is running now, and a receipt that grows with the run ends
up owning the screen. A longer list says where its window sits, on the right of
the heading, in the same form the tree's own scroll uses:

```
RETURNED 18                                              1-10 / 18
```

`j` at the bottom row moves the window down by one, `k` at the top moves it back
up; it holds still otherwise, so a return landing at the top never shifts the row
you are reading. A short pane gives the list less than ten - the live agents get
their rows first - and where there is no room for a list at all (the condensed
screen under 12 rows) it collapses to `+3 returned agents · a shows them`. Under
40 columns the count rides on the header (`AGENTS 8 ✓3`).

`a` puts them back on the tree as full rows - and so does `⏎` on the heading.
That is not a one-way door: with the rows on the tree the foot carries the way
back (`3 returned agents on the tree · ⏎ lists them`), in the same place and
still selectable, because a toggle whose only exit is a key you have to already
know is a trap.

## The idle screen

When the session goes quiet the pane summarises the run it just watched:

```
LAST RUN  add a unique email index                          3f9c-r1
8 subagents · 4m12s · 611k tok · 1 stalled            ended 14m ago
migrate-writer                                      █████████ 2m32s
api-types                                             ███████ 2m00s
```

Named by **what it was for** - the prompt that started it - because nobody
recognises a run by its record id. The id keeps a dim place on the right,
because it is the key `agentpane runs show <id>` takes and must never be the
part that gets elided.

Only that one run: the earlier ones are on disk and `agentpane runs` prints
them.

## Status bar (`--oneline`)

Each running TUI writes a small state file to
`~/.local/state/agentpane/current-<session-id>.json` on every change
(debounced to 1/s). The file is per session because panes are pinned to one:
several panes are several writers, and a single shared file would make the
status bar flip between unrelated sessions second to second.
`agentpane --oneline` reads one, prints one row, and exits:

```
⚑1 ●4 ◐1 ✔2 4m02s fix-ts2345
```

asks, live agents, stalls (`○n`, only when present), open calls, settled, and
the oldest ask's age and slug. If nothing is fresh (no file, or all of them
older than 15s) it prints `no agentpane running` and still exits 0, so a
status bar never shows an error.

With no `--session`, `--oneline` reports the most recently updated pane and
prefixes the row with that session's short id whenever more than one pane is
live, so the row is never anonymous:

```
2a71 ⚑1 ●4 ✔2 4m02s fix-ts2345
```

`agentpane --oneline --session <id>` reports exactly that pane instead, with
no fallback: if that session is not running, the row is `no agentpane
running`, even while other panes are.

tmux (whole machine, newest pane):

```
set -g status-interval 5
set -g status-right '#(agentpane --oneline)'
```

tmux (one pinned session, e.g. per window):

```
set -g status-right '#(agentpane --oneline --session "$CLAUDE_CODE_SESSION_ID")'
```

`CLAUDE_CODE_SESSION_ID` is the variable Claude Code exports into its own
session's shell; it holds the same id the registry (and `agentpane autopane`)
uses. Quote it: an unquoted expansion of an unset variable feeds `--session`
the next argument, or nothing at all. Either way `--oneline` answers with one
short line and exit 0 — it never prints a usage block into a status bar — but
the row is then a parse error rather than your session's status, so if that is
what you see, check the variable is set in the shell tmux runs the command in.
An empty `--session` is refused rather than quietly falling back to guessing
this directory's newest session.

iTerm2: Settings > Profiles > Session > enable "Status bar enabled" >
Configure Status Bar, drag in the **Command** component, set the command to
the absolute binary path (for example `/Users/you/go/bin/agentpane --oneline`;
the status bar does not use your login `PATH`) and an update cadence of a few
seconds.

## What it won't tell you

* **Named teammates report almost nothing.** No lifecycle events, no per-tool
  events. They get a dim row saying `limited telemetry` and nothing else.
* **Refusals are silent.** Approving a permission produces an event. Pressing
  esc produces nothing, so the row says `resolving…` until the agent does
  something else.
* **`↺2` and `✖ verify failed` are guesses** based on the commands an agent ran.
  The inspector labels them `(inferred)`, because that is what they are.
* **Token counts arrive one message at a time**, so the sparkline is coarse.
* **Only a blocked permission request escalates** to a notification or a dock
  bounce. Nothing else beeps, which is why the beep still means something.
