# Build: `agentpane` — a live TUI monitor for Claude Code subagents

**Complete functional + technical specification.** Paste this entire file to Claude Code as the
task. Everything the UI can ever show, every event it consumes, and every action it takes is
enumerated below — if something isn't in here, ask before inventing it.

**Visual reference — two files sit next to this one in the same folder:**

- `mock-your-scheme.html` — interactive HTML mock of the exact target UI, rendered in my real
  green-on-black iTerm2 scheme. **This is the primary reference.**
- `mock-neutral-palette.html` — the same mock on a neutral dark palette, useful for checking the
  design doesn't depend on my scheme.

Both are self-contained (no network needed) and interactive: click the pane, then `s` spawn ·
`j/k` move · `␣` unfurl · `⏎` inspect · `esc` close · `f` follow · `o`/`y`/`x` · `w` width ·
`r` idle · `[`/`]` history. Open them in a browser and drive them before writing code — the
interaction model is easier to feel than to read.

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
| C5 | Read-only. Never write to the repo, never write to the Claude session, never kill anything unless I press `x`. |
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
| `AgentSpawned` | a subagent was created (Task tool invoked) |
| `AgentStarted` | its first activity — leaves `queued` |
| `AgentReturned` | returned normally to main |
| `AgentFailed` | terminated with an error |
| `AgentKilled` | terminated because I pressed `x` |
| `AgentMerged` | its output was accepted into main's work (optional; treat as `AgentReturned` if unavailable) |

### Tool activity

| Kind | Meaning |
|---|---|
| `ToolStart` | tool invocation began (`Tool`, `Target` set) |
| `ToolEnd` | tool invocation ended (`ExitCode`, `Err` may be set) |
| `ToolOutput` | intermediate output line — refreshes the liveness clock |
| `FileRead` | a file was read |
| `FileEdited` | a file was written/edited (`LinesAdd`/`LinesDel`) |
| `VerifyStarted` | a verification command started (§2.4 classification) |
| `VerifyPassed` / `VerifyFailed` | its outcome |
| `RetryStarted` | agent is re-attempting after a failure (increments `↺n`) |
| `TokensUpdated` | token counter moved |
| `ThinkingStarted` / `ThinkingEnded` | extended thinking (optional; used only for the `thinking` activity label) |

### Attention

| Kind | Meaning |
|---|---|
| `PermissionRequested` | agent is blocked awaiting my approval (`Ask` set) |
| `PermissionGranted` / `PermissionDenied` | I answered |
| `NotificationRaised` | Claude raised a notification needing me |
| `ErrorRaised` | non-fatal error the agent is handling |

### Derived internally (never from a Source)

`StallDetected`, `StallCleared`, `ContentionDetected`, `ContentionCleared`, `RunStarted`,
`RunEnded`, `AgentDecayed`.

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

Five stations per subagent, each set by a real event. Monotonic — they never regress.

| # | Station | Set by | Why it matters |
|---|---|---|---|
| 0 | `spawn` | `AgentSpawned` | it exists |
| 1 | `first tool` | first `ToolStart` | it's working, not just thinking |
| 2 | `first edit` | first `FileEdited` | **files are now at risk** |
| 3 | `verify` | `VerifyStarted` | it's checking its work |
| 4 | `return` | `AgentReturned` | done |

Rendered as five pips: `▪` reached · `▫` current · `·` not reached.

`main` is the exception: it has a real todo list, so show **its current todo item text** instead
of pips. If the todo list is unavailable, show its current activity instead — never fake pips.

## 2.3 Agent status — the complete set

| Status | Meaning | Color | Dot | Sort rank |
|---|---|---|---|---|
| `ask` | blocked on a permission prompt | amber | `◍` | 0 |
| `stuck` | no events past threshold | amber | `◍` | 1 |
| `run` | working, producing events | cyan | `●` | 2 |
| `queued` | spawned, not yet started | dim | `○` | 3 |
| `done` | returned / merged | dim | `●` | 4 |
| `failed` | terminated with an error | dim + `✖` prefix on detail | `●` | 4 |
| `killed` | killed by me | dim | `⊘` | 5 |
| `unknown` | source lost track of it | dim | `◌` | 5 |

**`retry` is not a status.** A retrying agent stays `run` with a `↺n` counter beside its name.
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
run|stuck|ask --AgentFailed--------> failed
any    --AgentKilled---------------> killed
any    --source silence > 10m------> unknown
done|failed|killed --+30s----------> decayed (display collapse only, not a status)
```

Idempotent: repeat events cause no change. Stations advance on their trigger regardless of status.

## 2.5 Stall detection

- Default: **no events for 45s while a tool is open** → `stuck`.
- Long-running commands are normal: if the open tool's command matches `long_running_allow`
  (default `test`, `build`, `install`, `compile`, `bundle`, `migrate`), extend to **180s**.
- **A `--watch` / `-w` / `nodemon` / `tail -f` process that has gone quiet is the canonical stuck
  case and must be caught** — never exempt it regardless of the allowlist.
- No open tool and no events for 45s → also `stuck` (the agent is wedged thinking).
- `ThinkingStarted` extends the threshold by 60s once.
- Everything configurable (§8).

## 2.6 Liveness clock

**The primary number on every row is time since that agent's last event** — not total elapsed.
Total elapsed appears only in the inspector. `ToolOutput` refreshes it; heartbeats do not.
Format: `<60s` → `3s`; `<60m` → `2m50s`; else `1h04m`. Amber past the stuck threshold.

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

**Narrow 44:** drop the station label from line 2, drop cost-class extras, sparkline shrinks to
5 cells, tool-call `meta` suffixes drop, agent names never truncate. If a name still exceeds the
row, ellipsize the *activity* line instead, never the name.

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

7 cells wide (5 in narrow) from `▁▂▃▄▅▆▇`, scaled to tool-call rate over the trailing 60s,
one cell per ~8.5s bucket. `·` for an empty bucket. A stalled agent flatlines to `·······` —
**this must read as wrong before the timer does.** `done`/`queued` show all dots. Scale per-agent
against its own max so a quiet agent still shows shape.

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
7. On resolution: band disappears, title/badge restored, agent returns to normal ordering.

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

The mocks use concrete hex **only because a browser has no ANSI palette**. `mock-your-scheme.html`
maps each role to how it resolves in my scheme (default-fg green body, ANSI 6 live, ANSI 3 needs-you,
ANSI 8 settled, ANSI 1 errors); `mock-neutral-palette.html` shows the same layout on a neutral palette. Treat the mocks
as authoritative for **layout, geometry, glyphs, ordering, and copy**, and §3.10 as authoritative
for color. Where they disagree, §3.10 wins.

## 3.11 Glyphs — the complete set, nothing outside it

```
status    ● ◍ ○ ⊘ ◌
tools     ✔ ✖ ⚠ ⚑ ◐ ↺ ⤷
stations  ▪ ▫ ·
trunk     │ ├ ╰ ╮ ─
spark     ▁ ▂ ▃ ▄ ▅ ▆ ▇ ·
bars      █
spinner   ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠿
```

No emoji, ever. No Nerd Font glyphs.

## 3.12 Copy strings — exact

```
"⚑ NEEDS YOU"                              alert band header
"answer in the left pane — ⏎ jumps focus"  permission action
"x kills it · o opens the log"             stall action
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

---

# PART 4 — The inspector

Opens with `⏎` as a split at the bottom of the pane, max 320px / ~14 rows. Closing it is
**view-only** — the agent keeps running. This is a hard requirement, tested (§9).

```
▣ api-types ASK                    elapsed 2:17 · 44k · +35 −6
▪▪▪▫·  verify
Edit src/api/types.ts (+29 −4)
Bash pnpm typecheck → 1 error
  TS2345 handlers.ts:88
  'string | null' not assignable to 'string'
retry 2/3 · suspects a stale tsbuildinfo
⚑ needs permission: rm -rf node_modules/.cache
▌
esc close view — agent keeps running          o src/api/handlers.ts
```

Contents: name + status + `⟳ FOLLOW` when following · total elapsed, tokens, diff stat · station
pips + current station name · scrollable log tail (last 200 lines, amber for `⚑`/`!`, muted red
`#c98d78` for error text) · blinking cursor while live · footer with `esc` copy and the open file.

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
| `x` | kill the selected agent — inline confirm, one keypress | `main` → refuse, toast `won't kill main`; already settled → no-op |
| `w` | toggle 62/44 layout | persisted |
| `[` / `]` | previous / next run in idle or history view | wraps |
| `t` | toggle absolute time vs since-last-output | persisted |
| `?` | key help overlay + dropped-event count + source name | any key closes |
| `ctrl-c` | quit agentpane | never touches agents; warn if agents are live: `agents keep running · ctrl-c again to quit` |

Mouse: click selects, double-click opens the inspector, scroll scrolls. No drag.

## 5.2 Toasts

Footer-left, 3s, cyan `✓ …` for success and amber `⚠ …` for refusal. Never stack; newest wins.
Full list: `opened <file> in $EDITOR`, `<agent> has no open file`, `yanked: <text…>`,
`nothing to yank`, `killed <agent>`, `won't kill main`, `kill <agent>? press x again`,
`✓ reconnected`, `⚠ disconnected — retrying`, `layout: narrow 44`, `n dropped events — see log`.

## 5.3 Side effects — the only ones permitted

| Action | Effect | Guard |
|---|---|---|
| `o` | spawn `$EDITOR` in a new iTerm2 tab | never in the agentpane pane itself |
| `y` / `Y` | write to clipboard (`pbcopy`) | |
| `x` | send the documented kill signal to that agent only | requires confirm; never `main` |
| `⏎` on band | iTerm2 focus escape / AppleScript fallback | must not steal focus otherwise |
| alert raised | title + badge escapes, one bell | respects `bell`/`badge` config |
| run ends | append JSONL to state dir | |

**Nothing else touches the outside world.** No repo writes, no session writes, no network.

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
agentpane --no-bell --no-badge
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
max_calls_shown = 4
history_runs = 20
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
- `esc` never kills an agent — covered by a test.
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
