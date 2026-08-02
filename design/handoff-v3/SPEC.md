# Build: `agentpane` — a live TUI monitor for Claude Code subagents

**Complete functional + technical specification.** Paste this entire file to Claude Code as the
task. Everything the UI can ever show, every event it consumes, and every action it takes is
enumerated below — if something isn't in here, ask before inventing it.

**Revision 3.** Reconciled with the live data-source investigation of Claude Code 2.1.220:
everything the platform cannot emit is cut or demoted to an explicitly-labelled inference (rule
C8). Adds the away-digest, the escalation ladder, the name-slug rule, the token rule, and the
expansion budget. **PART 12 is the change log** — read it first if you have the previous revision.

**Visual reference — two files sit next to this one in the same folder:**

- `mock-your-scheme.html` — interactive HTML mock of the exact target UI, rendered in my real
  green-on-black iTerm2 scheme. **This is the primary reference.**
- `mock-neutral-palette.html` — the same mock on a neutral dark palette, for checking the design
  doesn't depend on my scheme.

Both are self-contained and interactive: click the pane, then `s` spawn · `j/k` move · `␣` unfurl ·
`⏎` inspect · `esc` close · `f` follow · `o`/`y` · `w` width · `r` idle · `[`/`]` runs · `1–3`
sessions. Drive them before writing code — the interaction model is easier to feel than to read.

Read them for layout, geometry, glyph placement, ordering, and copy. **Do not port their code** —
they're mocks, and a browser has no ANSI palette (see §3.10.5).

---

# PART 0 — Purpose and constraints

## 0.1 What this is

I run Claude Code in iTerm2. When it fans out subagents I can't see which agents exist, how far
along they are, whether one is wedged, or — the expensive one — whether one is silently blocked
waiting for me to approve something.

`agentpane` is a **read-only TUI** that runs in a second iTerm2 pane, opened automatically by my
iTerm2 profile, and stays open for the whole session. It renders the agent tree live.

Three questions it must answer without me reading prose:

1. Where is every agent in its work?
2. Is anything stuck?
3. What is it doing right now?

It is not a chat client. It never sends prompts to Claude. It observes and it points.

## 0.2 Hard constraints

| # | Constraint |
|---|---|
| C1 | **Go**, `charmbracelet/bubbletea` + `lipgloss`. Deviate only after asking. |
| C2 | Single static binary, `go install`-able, no runtime deps. |
| C3 | iTerm2 / macOS. **ANSI 16-color only — inherit the user's scheme, never hardcode hex** (§3.10). **Assume no Nerd Font** — only glyphs in §3.11. |
| C4 | Must work at 44 columns without truncating an agent name. |
| C5 | **Strictly read-only.** Never write to the repo, never write to the Claude session, never terminate anything. Subagents run in-process — there is no kill mechanism, so v1 has no kill action at all (§5.1). |
| C6 | If the event source dies: disconnected state, no crash, auto-reconnect. |
| C7 | Zero-config: bare `agentpane` works. |
| C8 | Never invent data. If a field is unavailable, render the honest absence (§7.9), never a plausible guess. |
| C9 | No panics on malformed/partial event JSON — log to a debug ring buffer and continue. |
| C10 | Steady-state CPU under 1% idle; render coalesced to at most 10 fps. |

---

# PART 1 — Data acquisition

## 1.1 Investigate first — do this before any UI code

Write `docs/DATA-SOURCES.md` covering, for this machine:

- **Hooks** (`~/.claude/settings.json`): every hook that exists — `PreToolUse`, `PostToolUse`,
  `Notification`, `UserPromptSubmit`, `Stop`, `SubagentStop`, `SessionStart`, `SessionEnd`,
  `PreCompact`, and any others. For each: exact stdin JSON shape, whether it identifies the
  subagent, whether it can block/observe permission prompts, and its latency.
- **Transcript files** under `~/.claude/projects/**` (JSONL): per-line schema, whether subagent
  events carry a stable id, whether tool inputs/outputs are present, write latency and whether
  partial lines occur.
- **Any streaming mode** (`--output-format stream-json` or similar).
- **Anything else** you discover.

For each candidate record: identifies subagents (y/n), latency, sees permission requests (y/n),
sees tool results (y/n), survives compaction (y/n). Then pick a primary + fallback and justify.

**Stop and show me this document before continuing to code.**

## 1.2 Source interface

All acquisition sits behind one interface. The UI must never know the source.

```go
type Source interface {
    // Events emits until ctx is done. On transport failure it must emit
    // Event{Kind: SourceDisconnected} and keep retrying with backoff,
    // emitting SourceConnected on recovery. Never close the channel on
    // recoverable errors.
    Events(ctx context.Context) (<-chan Event, error)
    Name() string
}

type Event struct {
    Seq       uint64        // monotonic, assigned on ingest
    At        time.Time     // source timestamp; ingest time if absent
    SessionID string
    AgentID   string        // stable id; "main" for the root agent
    AgentName string        // display name, e.g. "explore-schema"
    Kind      EventKind
    Tool      string        // "Bash" | "Edit" | "Read" | "Grep" | "Task" | "Write" | ...
    Target    string        // file path, command, or pattern (raw; UI shortens)
    Detail    string        // one-line human summary
    Tokens    int           // cumulative for this agent if known, else delta
    LinesAdd  int
    LinesDel  int
    ExitCode  *int          // nil unless a process ended
    Err       string        // error text, empty if none
    Ask       *AskPayload   // non-nil only for PermissionRequested
    Raw       json.RawMessage
}

type AskPayload struct {
    Kind     string // "command" | "file_write" | "network" | "mcp" | "other"
    Command  string // exact command or path being requested
    Options  []string // e.g. ["once","always","no"] if known
}
```

## 1.3 Event taxonomy — the complete set

Every event the state machine understands. Nothing else may reach the UI.

### Session / transport

| Kind | Meaning | Emitted by |
|---|---|---|
| `SourceConnected` | transport up | Source |
| `SourceDisconnected` | transport down, retrying | Source |
| `SessionStart` | Claude session began | hook |
| `SessionEnd` | session ended | hook |
| `SessionIdle` | root agent awaiting user input | hook / inference |
| `UserPromptSubmitted` | I sent a prompt — starts a **run** | hook |
| `CompactStarted` / `CompactFinished` | context compaction (ids may reset) | hook |

### Agent lifecycle

| Kind | Meaning |
|---|---|
| `AgentSpawned` | a subagent was created (**`Agent` tool** invoked — named `Agent` on 2.1.220, **not** `Task`; wire matchers to `Agent`) |
| `AgentStarted` | its first activity — leaves `queued` |
| `AgentReturned` | returned normally to main |
| `AgentMerged` | its output was accepted into main's work (optional; treat as `AgentReturned` if unavailable) |

**Cut — no source exists (do not implement):** `AgentFailed` (the subagent-finished event carries
no status/error/exit-code — a known upstream gap), `AgentKilled` (nothing to kill, §5.1).

### Tool activity

| Kind | Meaning |
|---|---|
| `ToolStart` | tool invocation began (`Tool`, `Target` set) |
| `ToolEnd` | tool invocation ended (`ExitCode`, `Err` may be set) |
| `ToolOutput` | **foreground commands: does not exist.** Nothing is emitted between `ToolStart` and `ToolEnd`. Only **background tasks** stream to an output file whose growth is a real liveness signal — emit `ToolOutput` for those and nothing else. See §2.6a. |
| `FileRead` | a file was read |
| `FileEdited` | a file was written/edited (`LinesAdd`/`LinesDel`) |
| `VerifyObserved` | **derived, not a platform event** — a Bash command pattern-matched as test/build/lint. Feeds the inferred `✓ verified` marker only (§2.2), never a station. |
| `RetryInferred` | **derived, not a platform event** — the same command re-issued after a failing `ToolEnd`. Feeds the dim `↺n` counter only. |
| `TokensUpdated` | token counter moved |
| `ThinkingEnded` | thinking is only observable **at completion**, so it can label past activity and nothing else. It must **not** affect stall thresholds (§2.5). |

### Attention

| Kind | Meaning |
|---|---|
| `PermissionRequested` | agent is blocked awaiting my approval (`Ask` set) |
| `PermissionGranted` / `PermissionDenied` | I answered |
| `NotificationRaised` | Claude raised a notification needing me |
| `ErrorRaised` | non-fatal error the agent is handling |

### Derived internally (never from a Source)

`StallDetected`, `StallCleared`, `ContentionDetected`, `ContentionCleared`, `RunStarted`,
`RunEnded`, `AgentDecayed`, `VerifyObserved`, `RetryInferred`, `CallOpened`, `CallClosed`,
`AskDeduped`, `GhostAgentFolded`.

Everything derived must be visually marked as inferred wherever it surfaces (§3.14).

## 1.4 Source implementations to ship

- **`FakeSource`** — deterministic replay of §3.6, driven by `agentpane --demo`.
  **Build the entire UI against this first.** Must support `--demo-speed`, `--demo-seek <sec>`,
  and pause/step (§6.4) so any state is reachable for screenshots and tests.
- **`HookSource`** — `agentpane hook` subcommand reads hook JSON on stdin and forwards it over a
  unix socket at `$TMPDIR/agentpane-<session>.sock` to the running TUI. Must be fast (<10ms) and
  must **never block or fail the hook** — if the socket is absent, exit 0 silently. Ship the exact
  `settings.json` snippet in the README.
- **`TranscriptSource`** (if §1.1 shows it's viable) — tail JSONL, tolerate partial lines,
  resume from offset.

Ordering, dedupe, and idempotency: events may arrive out of order or twice. Key on
`(AgentID, Kind, Seq)`; make all state transitions idempotent; sort by `At` within a 250ms window.

## 1.5 Honest degradation

If a source cannot distinguish subagents, do **not** synthesize identities. Show a single `main`
row plus a dim line `⚠ subagent detail unavailable — <reason>` and say so loudly in
`docs/DATA-SOURCES.md`. If permission requests aren't visible, tell me — that feature is the
whole point.

---

# PART 2 — Domain model

## 2.1 Topology — exactly two levels

**Subagents in Claude Code cannot spawn subagents, and never declare a plan up front.**
Therefore:

- Depth 0: `main`. Depth 1: subagents. Depth 2: tool calls (display only).
- **Never indent by spawn order.** Indentation means depth; depth is always 1.
- If a source ever reports depth ≥ 2, render it as depth 1 and log a warning.

## 2.2 Observed lifecycle stations

**Four** stations per subagent, each set by a real event. Monotonic — they never regress.

| # | Station | Set by | Why it matters |
|---|---|---|---|
| 0 | `spawn` | `AgentSpawned` | it exists |
| 1 | `first tool` | first `ToolStart` | it's working, not just thinking |
| 2 | `first edit` | first `FileEdited` | **files are now at risk** |
| 3 | `return` | `AgentReturned` | done |

Rendered as four pips: `▪` reached · `▫` current · `·` not reached.

**Verify is not a station.** It could only ever be a pattern match over Bash commands, and C8
forbids dressing a heuristic as an observed fact. Instead, when a test/build/lint command is
observed, append an inferred marker `✓ verified` to that agent's activity line, in the ok colour,
and label it `✓ verified (inferred)` in the inspector and the `?` overlay. It never moves a pip.

`main` is the exception: it has a real todo list, so show **its current todo item text** instead
of pips. If the todo list is unavailable, show its current activity instead — never fake pips.

## 2.3 Agent status — the complete set

| Status | Meaning | Color | Dot | Sort rank |
|---|---|---|---|---|
| `ask` | blocked on a permission prompt | amber | `◍` | 0 |
| `stuck` | no events past threshold | amber | `◍` | 1 |
| `run` | working, producing events | cyan | `●` | 2 |
| `queued` | spawned, not yet started | dim | `○` | 3 |
| `done` | returned (see below) | dim | `●` | 4 |
| `teammate` | a named agent — near-zero telemetry (§3.15) | dim | `◇` | 5 |
| `unknown` | source lost track of it | dim | `◌` | 6 |

**Cut: `failed` and `killed`.**

- `failed` has no source — the finished event carries no status. A returned agent is `done`, and
  its row/inspector shows **its last message**, rendered in the error colour if it matches error
  patterns. Never print a verdict the platform didn't give. If you want to surface a likely
  failure, the only acceptable wording is an explicitly hedged `done · last: <message>`.
- `killed` has no mechanism (§5.1).

**`retry` is not a status.** A retrying agent stays `run` with a **dim, inferred** `↺n` counter
beside its name — dim, never amber, because it is a heuristic (same command re-issued after a
failing `ToolEnd`).

**There is no fourth color.** Errors are shape and text, never hue.

## 2.4 Transitions

```
(none) --AgentSpawned--------------> queued
queued --AgentStarted|ToolStart----> run
run    --PermissionRequested-------> ask
ask    --PermissionGranted|Denied--> run
run    --StallDetected-------------> stuck
stuck  --any agent event-----------> run          (StallCleared)
run|stuck|ask --AgentReturned------> done
any    --source silence > 10m------> unknown
(spawned as a named agent)---------> teammate   (terminal; §3.15)
done|failed|killed --+30s----------> decayed (display collapse only, not a status)
```

Idempotent: repeat events cause no change. Stations advance on their trigger regardless of status.

## 2.5 Stall detection

- Default: **no events for 45s while a tool is open** → `stuck`.
- Long-running commands are normal: if the open tool's command matches `long_running_allow`
  (default `test`, `build`, `install`, `compile`, `bundle`, `migrate`), extend to **180s**.
- **A `--watch` / `-w` / `nodemon` / `tail -f` process that has gone quiet is the canonical stuck
  case and must be caught** — never exempt it regardless of the allowlist.
- **No open call and no events for 45s → `stuck`.** This is the *strong* signal and the one the
  UI should shout about: nothing is running and nothing is happening.
- **An open call that has gone quiet is NOT stuck** until its threshold expires — it gets its own
  rendering instead (§2.6a). Because `ToolOutput` doesn't exist for foreground commands, the timer
  plus the allowlist is the *only* evidence available; the sparkline cannot help here.
- Thinking cannot extend any threshold (only observable at completion).
- Everything configurable (§7).

## 2.6 Liveness clock

**The primary number on every row is time since that agent's last event** — not total elapsed.
Total elapsed appears only in the inspector. Heartbeats never refresh it.
Format: `<60s` → `3s`; `<60m` → `2m50s`; else `1h04m`. Amber past the stuck threshold.

## 2.6a Open-call state — required, because `ToolOutput` doesn't exist

A healthy three-minute build emits nothing between `ToolStart` and `ToolEnd`, so it is
indistinguishable from a wedged agent by events alone. The one fact you *do* have is whether a
call is open. Render it:

| Condition | Row right-hand side | Colour |
|---|---|---|
| call open, within threshold | `◐ in <Tool>` + **time in call** | live |
| call open, past threshold | `◐ in <Tool>` + time in call | needs-you |
| no call open, recent event | sparkline + since-last-event | live / dim |
| no call open, past threshold | `·······` flatline + since | needs-you |

So "quiet with a call open" and "quiet with nothing open" are visually distinct, and §3.4's
"flatline reads as wrong" survives — the flatline now only ever means *nothing is running*.
Background tasks are the exception: their output file growth is real, so they keep a live
sparkline.

## 2.7 File contention

If two agents whose status is not `done`/`killed` have both emitted `FileEdited` for the same
path, raise `ContentionDetected`. One amber row: `⚠ 2 agents editing db/schema.ts`, agent names
on a dim second line. Clear when one finishes. Multiple contentions: show the newest, plus
`+n more`. Paths shortened by stripping the repo root and a leading `src/`.

## 2.8 Runs

A **run** = `UserPromptSubmitted` → all agents settled + `SessionIdle`. Persist the last 20 to
`~/.local/state/agentpane/runs/*.jsonl` (id, start, end, per-agent name/duration/status/tokens,
outcome, contention count, stall count). Survive restart. When a run ends, freeze it as the
summary rather than blanking the pane.

## 2.9 Derived values per agent

`status`, `stations[5]`, `retries`, `lastEventAt`, `sinceLast`, `totalElapsed`, `tokens`,
`linesAdd/Del`, `currentTool`, `currentTarget`, `activityLine`, `openFile`, `lastError`,
`callHistory` (ring buffer, last 20; render last 4), `sparkBuckets` (60 × 1s tool-call counts),
`askPayload`, `spawnIndex`, `doneAt`.

---

# PART 3 — Visual specification

## 3.1 Geometry

Two widths, toggled with `w`, persisted: **wide 62 cols** (default) and **narrow 44 cols**.
Vertical, tall-narrow. Tight leading — one row per line, no blank spacers.

```
┌─ wide 62 ────────────────────────────────────────────────┐
│ AGENTS 7                                  ⚑ 1 NEEDS YOU │  header
├──────────────────────────────────────────────────────────┤
│ ⚑ NEEDS YOU                                              │  alert band
│ api-types  permission                                52s │
│ run: rm -rf node_modules/.cache && pnpm rebuild          │
│ answer in the left pane — ⏎ jumps focus                  │
├──────────────────────────────────────────────────────────┤
│ ◍ main  refactor auth                               112k │  root
│ │ migration                                           6s │
│ ├─╮                                                      │
│ │ ╰◍ api-types  ↺2                       ▃▅▂▁···    52s │  agent (selected)
│ │    ⤷ blocked · asked to clear the TS cache             │
│ │      │ ✔ Edit api/types.ts  +29 −4                     │
│ │      │ ✖ Bash pnpm typecheck  1 err                    │
│ │      │ ⚑ permission request  41s                       │
│ ├─╮                                                      │
│ │ ╰◍ test-runner                         ▄▅▃▁···  2m50s │
│ │    ⤷ pnpm test --watch never exits          first edit │
│ ├─╮                                                      │
│ │ ╰● explore-schema                      ▃▅▆▄▂▃▅     3s │
│ │    ⤷ Grep "createUser" src/**               first tool │
│ ├─╮                                                      │
│ │ ╰● typecheck  merged · 0:22             ·······        │  decayed
│ ╰─╮                                                      │
│   ╰○ docs-writer  queued                                 │  last: trunk ends
├──────────────────────────────────────────────────────────┤
│ ⚠ 2 agents editing db/schema.ts                          │  contention
│   explore-schema + migrate-writer                        │
├──────────────────────────────────────────────────────────┤
│ j/k ␣ ⏎ f o y x                     since last output → │  footer
└──────────────────────────────────────────────────────────┘
```

**Narrow 44 — degradation order.** Real display names are long (§3.16), so at 44 columns drop
right-hand elements in this exact order and never truncate the slug:

1. sparkline (the `◐ in <Tool>` indicator stays — it is load-bearing)
2. station label text (pips remain)
3. tool-call `meta` suffixes
4. the inferred `✓ verified` marker

If the slug plus the timer still cannot fit, wrap the **activity** line, never the name.

## 3.2 The trunk

- `main` owns one **continuous** vertical line. Each subagent leaves it on a curved branch
  (`├─╮` then `│ ╰●`).
- **Every subagent sits at the same column.** No stepping by spawn order.
- The branch of the **last rendered row** terminates at its elbow (`╰─╮` / `  ╰`) — derive "last"
  from **render order**, not static data; ordering changes as statuses change.
- The trunk must be unbroken at every width and every row count, with no stub dangling below the
  final row. Verify at 44, 62, 100, 200 cols and 1, 2, 8, 40 agents.

## 3.3 Row anatomy

**Line 1:** `<dot> <name>[  ↺n][  <collapsed tail>]` … right-aligned `<sparkline> <sinceLast>`
**Line 2** (expanded only): `⤷ <activity>` … right-aligned `<current station>`
**Lines 3–6** (expanded only): last 4 tool calls, `  │ <glyph> <text><meta>`, dim

Expansion: only the selected agent auto-expands; `space` pins others open. Decayed and `queued`
agents never expand.

## 3.4 Sparkline

7 cells from `▁▂▃▄▅▆▇`, scaled to **`ToolStart`/`ToolEnd` cadence** over the trailing 60s, one
cell per ~8.5s bucket. `·` for an empty bucket. Scale per-agent against its own max.

- **Only shown when no call is open** — an open call shows `◐ in <Tool>` instead (§2.6a). This is
  what keeps a long healthy build from flatlining like a wedged agent.
- A flatline `·······` therefore means *nothing is running and nothing has happened* — and in that
  state it **must read as wrong before the timer does**.
- `done`/`queued` show all dots. `teammate` shows nothing at all (§3.15).
- **Dropped first at narrow width** (§3.1).

## 3.5 Decay

A settled agent (`done`/`failed`/`killed`) older than 30s collapses to one dim line:
`● typecheck  merged · 0:22`. Live work must always dominate. Decayed rows stay selectable and
expand on `⏎` into the inspector.

## 3.6 Ordering

`ask` → `stuck` → `run` → `queued` → `done`/`failed` → `killed`, stable by spawn order within
each group. **Only attention reorders rows** — nothing else may shuffle them. Animate nothing;
a reorder is a single-frame jump.

## 3.7 The alert band — most important feature

When an agent is blocked on a permission prompt, **the prompt itself lives in the left pane** —
that's where I type. agentpane's job is to make it impossible to ignore:

1. Float the agent into an amber `⚑ NEEDS YOU` band pinned above the tree: agent name, kind
   (`permission` / `stalled`), what it wants (**the exact command**), and a counting-up wait timer.
2. Action line: permission → `answer in the left pane — ⏎ jumps focus`; stall → `x kills it · o opens the log`.
3. Set the **iTerm2 pane title and badge** to `⚑ <agent> needs you` — `ESC]1;<title>BEL` and
   iTerm2's `OSC 1337;SetBadgeFormat=<base64>BEL` — so it reads with the window unfocused or the
   pane collapsed. Clear both when resolved.
4. Ring the terminal bell **once per event**, never repeatedly, respecting `bell = false`.
5. Header right side becomes `⚑ n NEEDS YOU` in amber; the pane's title dot turns amber.
6. Multiple alerts: list all, oldest first, cap at 3 with `+n more`.
7. **Dedupe — required.** One logical ask can fire several `PermissionRequested` events under
   fresh agent ids while it sits unanswered (a background subagent retrying). Key on
   **tool + normalised input**; show one band entry with a `×3` count. Do **not** render the
   retry agents as tree rows — fold them into the band as
   `+2 retry agents folded`.
8. **Resolution is asymmetric.** Granting emits an event within ~1s. **Denying emits nothing** —
   it only appears in the transcript. So the band must not promise instant clearing: on any
   resolution signal show `resolving…` and clear on the agent's next event or after a short
   grace period. Never claim an outcome you didn't observe.
9. On resolution: band disappears, title/badge restored, agent returns to normal ordering.

**Timing is good news:** the hook fires ~64ms after the blocked call and carries the tool, the
full command, and the raising subagent's id and type — so the band can name the agent, print the
exact command, and keep a wait timer accurate to well under a second.

## 3.8 Idle state

The pane opens with iTerm2 and must look deliberate, never broken:

```
AGENTS                                              idle
◍ claude ready · ~/src/api
╰─ no subagents

RUN 3f9c                                        [ ] 1/3
8 subagents · 4m12s · 611k tok · 1 stalled
migrate-writer   ██████████ 2m32s
api-types        ████████   2m00s
explore-schema   ██████     1m28s
test-runner      ████████████ 3m04s
typecheck        ██         0m36s

⠹ watching for activity                  [ ] browse runs
```

Braille spinner cycles slowly so it's visibly alive. `[` / `]` browse persisted runs.

### 3.8a Multiple concurrent sessions — required

Several Claude sessions per directory is normal. **Attach rule: the newest live session whose cwd
matches.** The idle screen lists them and `1`–`9` switches:

```
SESSIONS                                     attach: newest live
▸ 3f9c  ~/src/api   live · 8 agents (attached)
  2a71  ~/src/api   live · idle
  1c08  ~/src/web   ended 12m
```

When more than one session exists, the header shows the attached session's short id
(`AGENTS 8 · 3f9c`) so it is never ambiguous which one you are looking at.

## 3.9 Other states — all of them

| State | Rendering |
|---|---|
| First run, no history | idle, `no previous runs — waiting for your first prompt` |
| Source disconnected | header right `⚠ disconnected`, tree dimmed 50%, footer `retrying in 3s…`, keys still work |
| Source reconnected | brief footer `✓ reconnected`, tree restored |
| Session ended | frozen final summary + `session ended · [ ] browse runs` |
| Compaction in progress | footer `⠿ compacting context…`, agent rows held, no reorder |
| Pane too narrow (<40 cols) | single column: dot + name + since, footer `widen for detail` |
| Pane too short (<12 rows) | root + alert band + `n more agents ↓`, scroll with j/k |
| 40+ agents | scroll with sticky root + alert band; footer shows `12–20 / 43` |
| Agent detail unavailable | `⚠ subagent detail unavailable — <reason>` under root |
| All agents settled, session live | `all clear · 7 merged` + decayed list |
| Malformed events | silent; `?` overlay shows `n dropped events` and where to find the log |
| Agent in a long open call | `◐ in Bash` + time in call, live colour (§2.6a) |
| Teammate present | `◇` row, no pips/sparkline/clock, tail `limited telemetry` (§3.15) |
| Ask being retried | one band entry with `×3`, `+n retry agents folded`, no ghost rows |
| Ask resolving | band shows `resolving…` until the next event confirms |
| Several live sessions | header shows attached session id; idle lists all (§3.8a) |
| Returned agent that looks failed | `done · last: <message>`, message in error colour, no verdict |

## 3.10 Color — inherit the user's iTerm2 scheme, never hardcode

**This is a correctness requirement, not a preference.** I run a green-on-black scheme (bright
green body text, pure black background, red/magenta for deletions, blue/purple for identifiers,
grey for dim). Any hardcoded hex palette will clash with it, and with every other user's scheme.

Rules:

1. **Emit ANSI 16-color indices only** — never truecolor, never 256-color, never hex. iTerm2 maps
   indices 0–15 to the user's scheme, so the TUI inherits it for free and follows theme changes
   live with no restart.
2. **Never set a background color.** Let the terminal's own background show through everywhere.
   The only exceptions are §3.10.2.
3. **Never set the default foreground.** Primary text uses `SGR 39` (default fg) so it is
   whatever the user's body text is — in my case bright green.
4. `lipgloss.Color("3")` style ANSI indices, or `lipgloss.ANSIColor`. Set
   `lipgloss.SetColorProfile(termenv.ANSI)` explicitly so nothing can silently upgrade to
   truecolor.

### 3.10.1 Semantic roles → ANSI indices

| Role | ANSI | Notes |
|---|---|---|
| primary text (names, activity) | **default fg (39)** | inherits the user's body color |
| live / working | **2** (green) or **6** (cyan) — see 3.10.3 | must differ from primary |
| needs you (ask, stuck) | **3** normal / **11** bright (yellow) | the one loud role |
| error text | **1** normal / **9** bright (red) | text only, never a status color |
| settled / dim (done, queued, decayed) | **8** (bright black / grey) | |
| structure: trunk, branches, separators | **8** (grey) | must stay legible — never use 0 |
| deepest chrome (footer hints, station labels) | **8** with dim attribute (SGR 2) | |
| ok marks on tool calls (`✔`) | **2** (green) | |
| additions in diff stats | **2** (green) | matches how the session already renders `+27` |
| deletions in diff stats | **1** (red/magenta per scheme) | matches `-21` |
| bold emphasis | **bold attribute**, not a color | |

Exactly **three semantic states**: live, needs-you, settled. Errors are text color + glyph, never
a fourth status color.

### 3.10.2 The only permitted background usage

- **Selected row: reverse video** (`SGR 7`). Works in every scheme, needs no color choice.
- **Alert band: reverse video on the `⚑ NEEDS YOU` label line only** (index 3 fg reversed), not
  the whole band. The band is otherwise delimited by a rule of `─` in grey.

That's it. No fills, no tinted rows, no gradient anything.

### 3.10.3 Collision avoidance — required

Because primary text is the user's own color, the live indicator can collide with it (my body
text IS green, so green "live" would be invisible as a signal).

Implement `--accent auto|2|3|4|5|6` (default `auto`):

- On start, query the terminal's palette via `OSC 4 ; <index> ; ? BEL` for indices 2, 6, and the
  default foreground (`OSC 10 ; ? BEL`).
- Compute perceptual distance between the default fg and each candidate. Pick the first candidate
  ≥ a minimum distance; prefer 6 (cyan) when the default fg is green, 2 (green) when it is not.
- If the terminal doesn't answer the query (timeout 200ms), default to **6** (cyan) — safe against
  the common white/grey-on-dark and green-on-black schemes alike.
- Record the choice in `agentpane doctor` output so I can see what it picked and why.

Never rely on distinguishing live from primary by **color alone**: live rows also carry a filled
dot `●` and a moving sparkline; settled rows are dimmed and decayed. The UI must remain readable
in a monochrome terminal (test with `TERM=xterm-mono` or `--no-color`).

### 3.10.4 `--no-color` / `NO_COLOR`

Honor `NO_COLOR` and `--no-color`: no SGR color at all. Differentiation then comes from glyphs,
the dim attribute, reverse video for selection, and bold for the alert band. This must be a
usable mode, not a broken one — cover it with a snapshot test.

### 3.10.5 On the HTML mocks

The mocks use concrete hex **only because a browser has no ANSI palette**. `v3 - your scheme` maps
each role to how it resolves in my scheme (default-fg green body, ANSI 6 live, ANSI 3 needs-you,
ANSI 8 settled, ANSI 1 errors); `v2` shows the same layout on a neutral palette. Treat the mocks
as authoritative for **layout, geometry, glyphs, ordering, and copy**, and §3.10 as authoritative
for color. Where they disagree, §3.10 wins.

## 3.11 Glyphs — the complete set, nothing outside it

```
status    ● ◍ ○ ◇ ◌
tools     ✔ ✖ ⚠ ⚑ ◐ ↺ ⤷
stations  ▪ ▫ ·
trunk     │ ├ ╰ ╮ ─
gutter    ▌            (away-digest change mark, §3.19)
spark     ▁ ▂ ▃ ▄ ▅ ▆ ▇ ·
bars      █
spinner   ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠿
```

No emoji, ever. No Nerd Font glyphs.

## 3.12 Copy strings — exact

```
"⚑ NEEDS YOU"                              alert band header
"answer in the left pane — ⏎ jumps focus"  permission action
"o opens the log · y yanks the command"    stall action
"⚑ WHILE YOU WERE AWAY"                    digest band header
"resolving…"                               ask answered, outcome unconfirmed
"+n retry agents folded"                   deduped ask
"limited telemetry"                        teammate tail
"read-only — subagents cannot be killed"   x pressed
"+n more"                                  truncated call list / digest
"✓ verified (inferred)"                    inspector
"esc close view — agent keeps running"     inspector footer  ← required wording
"⚠ 2 agents editing <file>"                contention
"watching for activity"                    idle
"no subagents"                             idle tree
"merged · <mm:ss>"                         decayed tail
"queued"                                   queued tail
"since last output →"                      footer hint
"⚑ <agent> needs you"                      pane title / badge
"⚠ disconnected"                           header, source down
"all clear · <n> merged"                   everything settled
```

## 3.13 Rendering rules

- Coalesce to ≤10 fps; only repaint on state change or the 1s clock tick.
- No animation except the spinner and the alert band's slow pulse.
- Never scroll the viewport implicitly; selection moves, view follows only when off-screen.
- Preserve selection across reorders by agent id, never by index.

## 3.14 Marking inference — required everywhere

Anything derived rather than observed must **look** derived. The rule: inferred values are rendered
in the dim/settled role (never the needs-you role), and their label carries `(inferred)` wherever
there is room for it — the inspector and the `?` overlay always, the row where it fits.

| Value | How it's marked |
|---|---|
| `↺n` retry counter | dim, never amber |
| `✓ verified` | ok colour on the activity line; `✓ verified (inferred)` in the inspector |
| `done · last: <message>` | the message is quoted platform text; no verdict word is added |
| stall | not inferred — it is a real timer, and may use the needs-you role |

## 3.15 Teammates — a lower-fidelity node class

Named agents emit almost nothing: no subagent lifecycle events, no per-tool events. Rendering them
as peers of real subagents would be a lie, so they get their own class:

- glyph `◇`, dim, always sorted last
- **no pips, no sparkline, no liveness clock**
- tail `limited telemetry`
- inspector shows `no station data — limited telemetry` plus whatever the transcript reveals
- they never appear in the alert band and never trigger escalation

## 3.16 Names — description → slug, deterministically

The platform gives you `subagent_type` (a class, e.g. `general-purpose`) and `description` (a 3–5
word phrase, e.g. `Fix the TS2345 fallout in api handlers`). The description is the name, and it is
far too long for a row.

**Slug rule** — pure, local, no model call, same input always gives the same output:

1. Lowercase; split on non-alphanumerics (keep digits attached to their token: `TS2345`).
2. Drop stopwords: `the a an in on of for to and or with into from at by`.
3. Keep the leading verb plus the first two significant tokens.
4. Join with `-`; hard cap `slug_max` (18); if longer, truncate at 17 and append `…`.
5. **Collisions:** append the next distinguishing token from the description; if still equal,
   append `-2`, `-3`. Never renumber an existing agent — slugs are stable for the life of the run.

```
Fix the TS2345 fallout in api handlers      → fix-ts2345-fallout
Run the auth test suite in watch mode       → run-auth-tests
Audit the users schema for missing indexes  → audit-users-schema
Document the new email constraint           → document-constr…
Port the auth routes / Port the user routes → port-auth-routes / port-user-routes
```

Determinism matters because the slug is the identity you see in a notification, in `--oneline`, and
in run history hours later.

**The full description is never lost:** it appears on the expanded row (**skipped when the slug is
not lossy** — no duplicate line), in the inspector wrapped in full with `subagent_type`, and `Y`
yanks it verbatim. **Never truncate the slug to make room for anything else** (§3.1).

## 3.17 Expansion budget — required

An expanded agent is 3–7 rows (slug · description if lossy · activity · station · up to
`max_calls_shown` calls). A real subagent makes 20–80 tool calls, so 4 is a *window*, not a list.

- Ring buffer 20, render `max_calls_shown` (4), and when there are more, the last call line ends
  with a dim `+37 more` so the truncation is visible rather than silent. The full tail lives in the
  inspector, which scrolls.
- **The tree must never scroll because of pins.** Compute
  `available = paneRows − chrome − collapsedRows` and degrade expansion in order:
  4 calls → 2 calls → 0 calls (activity + station only) → collapse the pin with `␣ pinned (no room)`.
  Drop pins oldest-first; **the selected agent is never degraded below activity + station.**
- Chrome is ~12 rows (header, band, root, contention, footer, separators) — budget accordingly.

## 3.18 Tokens

A number that only grows cannot tell you something is wrong, so tokens do **not** own the row's
right column — the clock does (§2.6).

- **Run total** in the header, always: `4:32 · 412k tok`.
- **Per agent** on the expanded station line, right-aligned, dim: `▪▪▫· first edit        44k`.
- **Burn rate** behind `t`, which swaps the right column to `2.1k/s` — the only diagnostic form.
- Inspector always shows the agent's total.

## 3.19 The away-digest — focus reporting

iTerm2 reports focus in/out (`CSI ?1004h`), so the pane knows when you stopped looking. Use it:

- While unfocused, record every row that changes. Each such row gets a needs-you `▌` tick in a
  one-column gutter left of the trunk. **Settled agents get marked too** — "it finished while you
  were gone" is news.
- On focus-in, the band becomes a past-tense digest headed `⚑ WHILE YOU WERE AWAY` with the away
  duration, listing changes ranked **asks → stalls → returns**, capped at `digest_cap` (3) with
  `+n more`. A live ask stays at the top of the band and stays actionable.
- Marks clear on focus-in; the band clears on `esc`.
- If the terminal doesn't support focus reporting, skip the whole feature silently — never fake it
  with a timer.

**This is the same band as §3.7**, not a second one: it accumulates while you're away and reads as
a digest when you return.

## 3.20 The escalation ladder

One state, four intrusion levels, chosen by whether you can actually see the pane:

| Level | Surface | Condition |
|---|---|---|
| 1 | the band in the pane | always |
| 2 | `--oneline`: `⚑1 ●4 ◐1 ✔2 4m02s fix-ts2345` | pane closed, tmux/iTerm2 status bar |
| 3 | amber pane title + badge, one dock bounce | window unfocused |
| 4 | one system notification (`OSC 9`) | ask unanswered for `notify_after` (60s) |

**Escalation fires only for `⚑` asks — never for a stall, never for a completion.** That restraint
is the whole reason the notification keeps meaning something. Every level names the agent by the
same slug so the notification and the row are obviously the same thing.

---

# PART 4 — The inspector

Opens with `⏎` as a split at the bottom of the pane, **max 14 rows** (never a pixel measure).
Closing it is **view-only** — the agent keeps running. This is a hard requirement, tested (§10).

```
▣ fix-ts2345-fallout ASK           elapsed 2:17 · 44k · +35 −6
Fix the TS2345 fallout in api handlers      general-purpose
▪▪▫·  first edit                        ✓ verified (inferred)
Edit src/api/types.ts (+29 −4)
Bash pnpm typecheck → 1 error
  TS2345 handlers.ts:88
  'string | null' not assignable to 'string'
retry 2/3 · suspects a stale tsbuildinfo
⚑ needs permission: rm -rf node_modules/.cache
▌
esc close view — agent keeps running          o src/api/handlers.ts
```

Contents, in order:

1. slug + status + `⟳ FOLLOW` when following · total elapsed, tokens, diff stat
2. **the full untruncated description**, wrapped, plus `subagent_type` right-aligned (§3.16)
3. four station pips + current station name, plus `✓ verified (inferred)` when applicable
4. scrollable log tail (last 200 lines) — needs-you colour for `⚑`/`!` lines, **ANSI 1/9** for
   error text (never a hex value — §3.10), ok colour for inferred-verify lines
5. blinking cursor while the agent is live
6. footer: `esc close view — agent keeps running` and the open file path (an OSC 8 link)

For a `teammate` (§3.15) the station row is replaced by `no station data — limited telemetry`.

Follow mode (`f`): inspector auto-tracks the agent with the most recent event; header shows
`⟳ FOLLOW`; any manual `j`/`k` cancels it.

---

# PART 5 — Actions

## 5.1 Keymap — complete

| Key | Action | Edge cases |
|---|---|---|
| `j` / `↓` | select next | stops at end, no wrap; cancels follow |
| `k` / `↑` | select previous | stops at start; cancels follow |
| `g` / `G` | first / last row | |
| `space` | pin/unpin the selected agent's tool calls | no-op on decayed/queued |
| `⏎` | open inspector; **on the alert band, focus the left pane instead** | if already open, refocus it |
| `esc` / `q` | close inspector — **never kills** | if closed, `q` is a no-op (never quits the pane) |
| `f` | follow mode | opens inspector if closed |
| `o` | open the selected agent's current file in `$EDITOR` (new iTerm2 tab) | no file → toast `<agent> has no open file` |
| `y` | yank the agent's error, else its failing command, else its activity line | nothing → toast `nothing to yank` |
| `Y` | yank the whole inspector log | |
| `w` | toggle 64/44 layout | persisted |
| `[` / `]` | previous / next run in idle or history view | wraps |
| `1`–`9` | switch attached session (idle screen, §3.8a) | no-op if that slot is empty |
| `t` | cycle the right-hand column: since-last-event → **token burn rate** → back (§3.18) | persisted |
| `esc` (on the digest) | dismiss the away-digest band | the live ask entry stays |

**`x` (kill) is removed.** Subagents run in-process inside the `claude` process: there is no
per-agent process and no documented termination mechanism. Pressing `x` shows
`read-only — subagents cannot be killed` so the absence is explained rather than silent. This makes
agentpane strictly read-only, which is a feature (C5).
| `?` | key help overlay + dropped-event count + source name | any key closes |
| `ctrl-c` | quit agentpane | never touches agents; warn if agents are live: `agents keep running · ctrl-c again to quit` |

Mouse: click selects, double-click opens the inspector, scroll scrolls. No drag.

## 5.2 Toasts

Footer-left, 3s, cyan `✓ …` for success and amber `⚠ …` for refusal. Never stack; newest wins.
Full list: `opened <file> in $EDITOR`, `<agent> has no open file`, `yanked: <text…>`,
`nothing to yank`, `read-only — subagents cannot be killed`, `✓ reconnected`,
`⚠ disconnected — retrying`, `layout: narrow 44`, `n dropped events — see log`,
`attached 2a71`, `right column: token rate`.

## 5.3 Side effects — the only ones permitted

| Action | Effect | Guard |
|---|---|---|
| `o` | spawn `$EDITOR` in a new iTerm2 tab | never in the agentpane pane itself |
| `y` / `Y` | write to clipboard via **OSC 52** (works over SSH/tmux), `pbcopy` fallback | |
| `⏎` on band | iTerm2 focus escape / AppleScript fallback | must not steal focus otherwise |
| ask raised | title + badge escapes, one bell, `RequestAttention` | respects config (§3.20) |
| ask unanswered 60s | one system notification (`OSC 9`) | once per ask, never repeated |
| run ends | append JSONL to state dir | |

**Nothing else touches the outside world.** No repo writes, no session writes, no network, and
**no process signals — agentpane cannot terminate anything.**

## 5.4 Terminal capabilities to use

| Capability | Escape | Used for |
|---|---|---|
| Focus reporting | `CSI ?1004h`, then `CSI I` / `CSI O` | the away-digest (§3.19) |
| Hyperlinks | `OSC 8 ;; <uri> ST` | **every path and `file:line` in the pane** — ⌘-click opens it, no keybinding needed |
| Clipboard | `OSC 52` | `y` / `Y` through the terminal |
| Pane title | `OSC 1 ; <text> BEL` | `⚑ <slug> needs you` |
| iTerm2 badge | `OSC 1337 ; SetBadgeFormat=<base64> BEL` | same, visible when collapsed |
| Attention | `OSC 1337 ; RequestAttention=1 BEL` | one dock bounce on a new ask |
| Notification | `OSC 9 ; <text> BEL` | escalation level 4 (§3.20) |

All optional: probe once, degrade silently, and report support in `agentpane doctor`. Never let a
missing capability break a render.

---

# PART 6 — CLI

```
agentpane                       attach to the current session, render live
agentpane --demo                replay the §3.6 scenario (no Claude needed)
agentpane --demo-speed 2.0
agentpane --demo-seek 90        jump to t=90s
agentpane hook                  read one hook JSON on stdin, forward, exit 0 always
agentpane doctor                check hooks installed, socket reachable, iTerm2 escapes, font glyphs
agentpane runs                  list persisted runs
agentpane runs show <id>        print one run summary
agentpane --width narrow
agentpane --oneline             single-row status output for a tmux/iTerm2 status bar (§3.20)
agentpane --no-bell --no-badge --no-notify --no-links
agentpane --log <path>          debug log
agentpane --source hook|transcript|fake
agentpane version
```

`--demo` must support pause/step so any state in §3.9 is reachable for screenshots and tests.

---

# PART 7 — Config

`~/.config/agentpane/config.toml`, every key optional:

```toml
width = "wide"              # wide | narrow
stuck_after = "45s"
long_running_after = "3m"
long_running_allow = ["test", "build", "install", "compile", "bundle", "migrate"]
watch_never_exempt = true   # a quiet --watch is always stuck
decay_after = "30s"
bell = true
badge = true
title = true
time_mode = "since"         # since | absolute
editor = ""                 # falls back to $EDITOR
max_calls_shown = 4        # window into the ring buffer; overflow shows "+n more" (§3.17)
history_runs = 20
notify = true             # OSC 9 system notification for an unanswered ask
notify_after = "60s"
attention = true          # dock bounce on a new ask
links = true              # OSC 8 hyperlinks on paths
focus_digest = true       # away-digest via focus reporting (§3.19)
digest_cap = 3            # changes listed before "+n more"
slug_max = 18             # name slug budget (§3.16)
accent = "auto"           # auto | 2 | 3 | 4 | 5 | 6  (ANSI index, see §3.10.3)
color = true              # false == NO_COLOR
```

Invalid values: warn in the footer once, fall back to the default, never exit.

---

# PART 8 — iTerm2 integration

Ship `docs/ITERM2.md` with exact steps:

- a **Claude Code** profile that opens the session left and `agentpane` in a ~62-column right pane,
  automatically, on window open
- the profile/arrangement JSON or the `New Window with Profile` + `Split Vertically with Profile`
  recipe — whichever is actually reliable, with the reasoning
- the `settings.json` hook snippet for `agentpane hook`
- title/badge escape sequences used, and how to verify they work
- verification: `agentpane --demo` in the right pane reproduces §3.6
- `agentpane doctor` output explained line by line

---

# PART 9 — Build order

1. `docs/DATA-SOURCES.md` — investigate and report. **Stop and show me.**
2. Domain: `Event`, `Source`, agents, stations, status machine, stall detection, contention, runs
   — table-driven tests covering §2.4 transitions, §2.5 thresholds (including quiet `--watch`),
   station monotonicity, idempotent/out-of-order/duplicate events.
3. `FakeSource` + `--demo` + seek/pause.
4. UI against FakeSource: trunk, ordering, decay, sparklines, both widths, all §3.9 states.
5. Alert band + title/badge/bell + `⏎` focus jump.
6. Inspector, follow mode, `o` / `y` / `x` / toasts.
7. Idle state + run persistence + `[` `]` + `agentpane runs`.
8. `HookSource` + socket + `agentpane hook` + reconnect/backoff.
9. `agentpane doctor`, `docs/ITERM2.md`, README, `go install`.

---

# PART 10 — Definition of done

- `agentpane --demo` reproduces §3.6 exactly, including contention and the decayed `typecheck`.
- The trunk is one unbroken line at 44/62/100/200 cols and 1/2/8/40 agents, with **no stub
  dangling below the last row**.
- No agent name is ever truncated; the contention row is never clipped.
- A permission request paints the band, title, and badge within one render tick and rings once.
- `esc` closes the inspector and nothing else; **no code path anywhere sends a signal to any
  process** — covered by a test.
- Slug generation is pure and deterministic: table-driven tests including the five §3.16 examples,
  the truncation case, and the collision case.
- Pinning agents never makes the tree scroll: the §3.17 budget degrades instead.
- The away-digest marks exactly the rows that changed while unfocused, and clears on focus-in.
- Escalation fires for asks only — a stall and a completion must produce no notification and no
  dock bounce (test).
- A deduped ask renders one band entry with `×n` and no ghost tree rows.
- Ordering only changes when attention changes; selection survives reorder by id.
- Killing the source shows the disconnected state and reconnects without a restart.
- Malformed JSON never panics; dropped count is visible under `?`.
- Idle at <1% CPU; 40 agents at ≥30fps headroom with ≤10fps painted.
- **Renders correctly in my green-on-black scheme, in a light scheme, and with `NO_COLOR=1`** —
  three snapshot tests. No hardcoded hex anywhere in the render path (grep the source for `#` in
  color positions as part of CI).
- The accent auto-pick reports its reasoning in `agentpane doctor`.
- `agentpane doctor` passes on a clean machine.

---

# PART 11 — Ask me before deciding

- a real source can't distinguish subagents
- hooks can't see permission requests (**this feature is the whole point — I need to know**)
- you want to deviate from Go/bubbletea
- anything needing write access to my Claude Code session or repo
- any glyph, color, or status outside §2.3 / §3.10 / §3.11 — in particular, **any wish to use truecolor or a hardcoded palette**
- the two-level topology assumption turns out to be wrong
- focus reporting or OSC 8 turn out to be unavailable in my setup
- the slug rule produces a collision or a truncation you think is unreadable in practice
- you find a documented way to terminate a single subagent (then we can reconsider `x`)

---

# PART 12 — Change log vs revision 2 (the data-source investigation)

Point-by-point response to the findings. Where you offered a choice, the decision is made — build
it as stated.

| # | Finding | Decision |
|---|---|---|
| 1 | no `ToolOutput` for foreground commands | **New open-call state** (§2.6a): `◐ in <Tool>` + time-in-call replaces the sparkline while a call is open. Sparkline now only encodes ToolStart/End cadence, so a flatline means *nothing is running* and stays alarming. Background tasks keep a real sparkline. |
| 2 | `verify` has no source | **Cut to four stations.** Verify becomes an inferred `✓ verified` marker on the activity line, never a pip (§2.2, §3.14). |
| 3 | `RetryStarted` has no source | `↺n` **kept as a documented heuristic**, rendered dim, never amber (§3.14). |
| 4 | `AgentFailed` has no source | **`failed` status dropped.** Returned agents are `done` and show `done · last: <message>`, message in error colour, no invented verdict. |
| 5 | kill is unimplementable | **`x` removed entirely.** agentpane is strictly read-only (C5, §5.1); the keypress explains itself. |
| 6 | thinking observed only at completion | Threshold-extension rule deleted (§2.5). |
| 7 | teammates are near-invisible | **New `teammate` node class** (§3.15): `◇`, no pips/sparkline/clock, `limited telemetry`, never escalates. |
| 8 | real names are long | **Deterministic slug rule** (§3.16) + narrow-width degradation order (§3.1). Slug never truncated to fit other elements. |
| 9 | one ask → several requests | **Dedupe on tool + normalised input**, `×n` count, retry ghosts folded into the band, no ghost rows (§3.7). |
| 10 | denial is silent | Band shows `resolving…` and clears on the next event or a grace period; no promise of instant resolution (§3.7). |
| 11 | several sessions per directory | **Attach = newest live session for the cwd**, session list + `1`–`9` on idle, session id in the header (§3.8a). |
| 12 | hex + px in the spec | Inspector now specifies ANSI 1/9 and 14 **rows** (PART 4). |
| 13 | tool is `Agent`, not `Task` | Corrected in §1.3; matchers must use `Agent`. |
| 14 | main's todo only in the transcript | Accepted; the activity-line fallback stands and is expected whenever the transcript source is unavailable. |

**Added since revision 2** (not from the findings — new design): away-digest via focus reporting
(§3.19), the four-level escalation ladder and `--oneline` (§3.20), the token placement rule
(§3.18), the expansion budget and `+n more` (§3.17), OSC 8 hyperlinks and OSC 52 clipboard (§5.4).
