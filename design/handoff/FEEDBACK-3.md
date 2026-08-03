# Update for the design lab — what the build and live use changed

Revision 3.1 is built, shipped, and has been running against real Claude Code
sessions for a day. Repo: private `simoncoombes/agentpane`. This is the round of
changes that came out of the build itself and out of watching it run, so you can
bring the spec up to date and push further on your side.

Two kinds of content here: **deltas** (where the shipped behavior now differs
from rev 3.1, and why) and **platform facts** (things nobody could have designed
around because we did not know them yet). The open questions at the end are
where your input is most useful.

Current frame, real render at 64 columns:

```
AGENTS 8 · 3f9c                                    ⚑ 2 NEEDS YOU
────────────────────────────────────────────────────────────────
⚑ NEEDS YOU
fix-ts2345-fallout  permission ×3                            52s
run: rm -rf node_modules/.cache && pnpm rebuild
answer in the left pane — ⏎ jumps focus
+2 retry agents folded
typecheck-works…  stalled                                  2m50s
no call open · silent 2m50s
o opens the log · y yanks the command
────────────────────────────────────────────────────────────────
◍ main  refactor auth                                       112k
│ write the migration                                         5s
├─╮
│ ╰⚑ fix-ts2345-fallout  ↺2                         ····· 0.0k/s
│    ⤷ blocked · asked to clear the TS cache           ▪▪▫·  44k
│      │ ⚑ permission request  52s  +6 more
├─╮
│ ╰● run-auth-tests                              ◐ in Bash 3m12s
│    ⤷ Bash pnpm test --watch  ✓ verified   ▪▫·· first tool  38k
├─╮
│ ╰● audit-users-schema                            ⠼▇▇··· 0.1k/s
│    ⤷ Grep "createUser" src/**             ▪▪▫· first edit  52k
├─╮
│ ╰● write-email-index                              ▇▇▇▇▇ 0.3k/s
│    ⤷ Edit db/migrations/0042.sql          ▪▪▫· first edit  86k
├─╮
│ ╰○ document-constr…  queued
├─╮
│ ╰● lint-and-format  returned · 0:46
```

---

## 1. The sparkline is gone (§3.4 replaced)

This is the biggest visual change. The sparkline was seven bars of the trailing
60s, **each agent scaled against its own maximum**. In use it was unreadable:
two rows showing an identical shape could differ by three orders of magnitude,
the shape rescaled every time a tall bucket rolled out of the window, and at
seven cells a burst and a lull look much alike. It was the one element on the
row carrying no transferable meaning.

What replaces it answers the two questions a glance actually asks:

| Question | Answer |
|---|---|
| Is this agent moving? | A braille spinner, stepped on the 1s tick, shown **only** while the agent is producing. A still row is genuinely still, so motion itself is information rather than decoration. |
| How hard, next to the others? | A five-cell bar on a scale **shared by every live agent**, so a longer bar always means more work. Settled agents are excluded from the denominator, since a finished burst should not shrink everything still running. |

So `▇▇▇▇▇` is the busiest agent in the run and `⠼▇▇···` is one doing roughly
40% as much and currently producing. A working agent always shows at least one
filled cell: rounding real work down to nothing errs in the wrong direction.

No history is lost — the exact rate stays on the row and the inspector keeps the
full call log. **If you want the history back in some form, this is the place to
push.** Five cells is deliberately coarse.

## 2. Rows expand by default, in two passes (§3.3 extended)

The spec expanded only the selected agent, plus pins. In a tall pane that left
two-thirds of the pane empty while every agent hid what it was doing — the first
thing the owner objected to.

Now: **breadth first**, every agent that fits gets its activity line, then
**depth**, leftover rows grow each agent's tool-call history (attention agents,
then live, then settled). Every candidate is trial-fitted, so growth can never
push the tree into scrolling, and breadth wins ties — an agent never loses its
activity line so another can gain a call. §3.17's budget still degrades in the
other direction when space is short.

## 3. The second line is the work, not the name again (§3.3, §3.16)

§3.16 put the full description under a row whenever the slug was lossy. Since
the slug is *derived from* the description, the two lines read as the same
sentence twice (`summarize-spec…` above `Summarize SPEC colour rules`) and every
agent cost two rows to say one thing. The description now appears only on the
**selected** row (where resolving a truncated name earns its line) and in the
inspector, which §3.16 always required anyway.

## 4. Activity text uses the tool's own words

Rows described a call by its raw target, so main once read
`Monitor end=$(( $(date +%s) + 45 )); until [ "$(date +%…` — a shell condition
truncated mid-expression. **Claude Code's tools carry a human summary next to
the command** (`"Hold an open 45s foreground call on main"`) and the spec never
used it. Rows now prefer it; the raw command is still kept for yanking,
contention keying, and the inspector.

## 5. The right-hand counter defaults to token burn rate (§2.6, §3.18)

The liveness clock (time since last event) resets to zero on every event, so on
a working agent it reads as a twitching stopwatch. It also restates something
the stall machinery already shouts about. `t` still swaps back, and the clock
still owns the column for a stalled agent, where silence *is* the point.

## 6. The open-call indicator waits 2s (§2.6a)

`◐ in <Tool>` fired for every call including sub-second ones, so the column
flickered between the indicator and the gauge on a busy agent. It now waits
until a call has been open two seconds — long enough that quiet is genuinely
ambiguous, which is the only case the indicator exists for.

## 7. Selected row is a uniform bar (§3.10.2)

Reverse video was applied per text run, so each run's own foreground became its
background: in a green-on-black scheme the selected row rendered as green blocks
with grey gaps rather than one bar. A selected row is one object; it gets one
bar. Bold survives, since after the monochrome rework bold is what carries
needs-you.

## 8. New: suppressed-agent line (extends §1.5)

`+7 agents with no recorded activity` — see platform fact 1. Selectable, expands
to ids and types, and `AGENTS n` counts only drawn rows so the header and the
tree always agree.

## 9. New: panes pin to their own session (§3.8a reshaped)

`agentpane --session <id>`. The session picker is now **conditional**: hidden
when the pane is pinned or when only one session is live, shown only when the
attachment is genuinely ambiguous (a hand-launched pane in a directory with
several sessions). `1`–`9` are inert when pinned. The header still names the
attached session.

## 10. New: auto-open (not in the spec)

`agentpane autopane`, wired as a `SessionStart` hook, splits the pane Claude
started in and runs the TUI at 64 columns (floor 44, always leaving ≥80 for
Claude). This supersedes PART 8's window-arrangement recipe as the recommended
route; the arrangement stays documented as the no-hooks alternative.

## 11. Smaller spec-level corrections

- **Names can change once.** §3.16 said slugs never change. A hooks-derived name
  is a guess (see platform fact 2), so an exact transcript name may replace it
  exactly once, then it settles.
- **main's label** is Claude Code's own session title (platform fact 4), budgeted
  to the pane and flattened to one line.
- Footer copy is `since last event →` (the mock), not §3.12's `since last output →`.
- §3.16's worked examples are not reachable from steps 1–4 alone; three
  adjacent-token rewrites are encoded as contract to reproduce them. `§2.10`
  prints `typecheck-worksp…` where the rule yields `typecheck-works…` (one char).
- All platform text is flattened before measuring or drawing (platform fact 9).

---

## Platform facts worth designing around

1. **Claude Code announces subagents that never do anything.** One real session
   with 5 genuine subagents also emitted `SubagentStart`/`SubagentStop` for ~40
   entities that ran no tool, spent no tokens, and wrote no transcript;
   another produced 4 more that lived ~90s. Independently corroborated by a
   different hook consumer. Both sessions had the experimental agent-teams flag
   on. Hence §8 above: an agent must *earn* a row.
2. **An agent's description and its id arrive on different hook payloads.** The
   spawning call carries the description but no agent id; the id arrives later
   with no description. Hooks can only correlate them heuristically, and a
   phantom start landing between the two can steal a real agent's name. The
   transcript sidecar holds both in one file, so it is the authority and may
   correct a hook guess.
3. **`CLAUDE_CODE_CHILD_SESSION` is injected into every process Claude Code
   spawns**, not just nested sessions — so a hook can never detect nesting from
   its own environment.
4. **There is a short session title** in the transcript (`ai-title`), rewritten
   as the session evolves. Exactly the "workstream description" the root row
   wanted.
5. **`~/.claude/todos/` does not exist** in 2.1.220. main's todo comes only from
   transcript `TodoWrite`, and plenty of sessions have none — so §2.2's fallback
   is the common case, not the exception.
6. **Permission prompts are excellent**: a `PermissionRequest` hook fires ~64ms
   after the blocked call with full tool identity, and carries the raising
   subagent's id. But grant/deny are asymmetric — granting emits an event,
   **denying emits nothing** — and one unanswered prompt can produce duplicate
   requests under fresh agent ids as background agents retry.
7. **There is no failure signal.** `SubagentStop` carries no error, status, or
   exit code, which is why §1.3's `AgentFailed` stayed cut.
8. **Teammates (named agents) emit no subagent hooks at all** — the low-fidelity
   `◇` class remains correct.
9. **Tool calls are routinely multi-line.** A `python3 -c` one-liner contains
   newlines; they measure as zero columns and then break the frame. The spec
   assumed single-line text throughout.
10. **Five or more concurrent sessions in one directory is normal**, which is why
    attach-by-cwd was unusable and pinning had to exist.

## What held up unchanged

Worth saying, because it is most of the spec: the trunk geometry, the alert band
(including `×n` dedupe, folded retry ghosts, and `resolving…`), the away-digest
and its `▌` gutter, the escalation ladder, station pips, decay, contention, the
inspector, the ANSI-16 monochrome palette and its three-cue needs-you rule, the
honest-absence discipline, and the §2.10 demo cast as the canonical scenario.
The colour work in particular has needed no changes at all.

## Open questions for you

1. **Gauge history.** Is five cells plus a spinner enough, or do you want a
   compact history back in some readable form? If so, on what shared scale?
2. **Stalled rows under a rate column.** A stalled agent currently reads
   `0.0k/s` with a flatlined gauge and an amber `⚑`. Loud enough, or should the
   column switch to the silence timer when stuck?
3. **Narrow 44.** The gauge is dropped first, as the sparkline was. Given it is
   now the only comparative element, should the station label go first instead?
4. **Decayed copy.** §3.12 says `merged · <mm:ss>`; §3.5 and the mock say
   `returned`. Shipped follows `returned`. Your ruling.
5. **Glyphs.** `▣` (inspector) and `◍` (main/idle) appear in your diagrams but
   not in §3.11's set; the gauge adds `▇` and reuses the braille spinner.
   Please fold these into the table.
6. **Verify as a station.** Cut to an inferred marker because it could only be a
   pattern match. Now that the tool's human description is available, a
   test/build/lint intent is often stated outright — worth reinstating a fifth
   station?
7. **main's second line.** With the session title on line 1, is the todo line
   still the best use of line 2, given most sessions have no todos?
8. **Suppressed agents.** Should the `+n` line be reachable in the inspector as
   well, with per-agent detail, or is the count enough?
