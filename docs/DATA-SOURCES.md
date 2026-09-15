# DATA-SOURCES.md — agentpane

Data-source survey and selection for agentpane: a read-only Go TUI that observes a running
Claude Code **v2.1.220** interactive session and renders its subagent tree live.

**Machine-specific.** Everything here was verified (or explicitly marked unverified) on this
machine: macOS (Darwin 25.5.0), user `simoncoombes`, Claude Code 2.1.220 at
`/Users/alex/.local/share/claude/versions/2.1.220`. Evidence provenance:

| Tag | Meaning |
|---|---|
| `[captured]` | Real payload/file captured live on this machine (experiment artifacts in `/private/tmp/claude-501/-Users-alex-Dev/3e30d225-2fb5-4c3b-b03f-e69f50dd19f0/scratchpad/hookexp/`, or parsed from existing hook scripts / on-disk transcripts) |
| `[binary]` | Extracted from `strings`/decompilation of the 2.1.220 binary; write path verified in code but not triggered live |
| `[docs]` | From https://code.claude.com/docs/en/hooks.md only; **not** verified on this machine |
| `[inferred]` | Derivable from other verified signals; not a direct event |

Where docs and captured evidence conflict, **captured wins**; each conflict is called out inline.

---

## 1. Summary & recommendation

**Primary source: hooks (command-type) + the live session registry `~/.claude/sessions/<pid>.json`.**
Hooks are the only channel that delivers per-tool events with subagent attribution in real time:
a live experiment on this machine (CC 2.1.220) proved that `PreToolUse`/`PostToolUse` fire for
tools run *inside* subagents and that those payloads carry `agent_id` (17-hex) + `agent_type` —
the presence of `agent_id` is the main-vs-subagent discriminator, and its value keys the agent
tree. `SubagentStart`/`SubagentStop` bracket each agent's life, and `SubagentStop` carries the
agent's own transcript path and final message. Hook latency vs. the transcript is <60 ms
(measured). Hooks are appended as an additional array entry per event in `~/.claude/settings.json`
(arrays demonstrably coexist — the user already runs two `PreToolUse` groups) and are re-read per
invocation, so installation applies to already-running sessions without a restart. Critically,
hooks may only *emit breadcrumbs* (write a line to a FIFO/append-log): anything a hook backgrounds
is killed when Claude Code reaps the hook's process tree, so the agentpane TUI must be a
standalone long-lived process that hooks write *to*. The sessions registry
(`~/.claude/sessions/<pid>.json`, one JSON file per live CLI process) supplies session discovery
(pid → sessionId, cwd, name) and a `status` field whose enum includes `"waiting"` with
`waitingFor:"permission prompt"` — the best zero-setup blocked-on-user signal for sessions
agentpane has no hooks in; once hooks are installed, the verified `PermissionRequest` hook (§3.3)
supersedes it as the primary signal.

**Fallback source: transcript tailing under `~/.claude/projects/<munged-cwd>/`.** The transcript
tree is the only place tool *outputs*, token usage, thinking blocks, and permission-denial records
live, and it works with zero installation. Each subagent writes its **own** JSONL file
(`<session>/subagents/agent-<id>.jsonl`, every line `isSidechain:true` + `agentId`), with a
sidecar `.meta.json` carrying the description and the spawning `toolUseId` — subagent activity is
*not* interleaved in the main session file, and the main file goes silent for minutes while
subagents run, so a tailer must watch the whole session directory. Caveats: lines are not strictly
timestamp-ordered (order by file position), a torn final line must be tolerated, and file-append
is the only granularity (no intra-message streaming).

**Headline caveats:**

1. **Permission prompts are fully visible in real time — live-verified 2026-08-01.** The
   `PermissionRequest` hook fires on 2.1.220 interactive sessions within ~50 ms of the dialog
   rendering (~64 ms after the blocked `PreToolUse`) and carries full tool identity
   (`tool_name`, complete `tool_input`, `permission_suggestions`) **plus `agent_id`/`agent_type`
   when the prompt was raised by a subagent** — subagent prompts are fully attributable
   `[captured]`. Remaining caveats: the payload has **no `tool_use_id`** (correlate to the
   PreToolUse via `tool_name`+`tool_input`+`prompt_id`+`agent_id`); the `Notification
   permission_prompt` signal is a delayed nudge firing **~6.1 s after the dialog is already on
   screen**, with no tool or agent identity — confirmer only. Grant/deny still have **no
   dedicated hook event**: grant = the eventual `PostToolUse` (~0.9 s after the keypress);
   manual deny (Esc) emits **nothing** — observable only as the absence of a PostToolUse, plus
   the transcript's `is_error` tool_result. Headless (`-p`) mode auto-denies with no prompt.
   While a prompt sits unanswered, a background subagent may retry the action under **new
   `agent_id`s**, producing duplicate PermissionRequests for one logical ask `[captured]` — the
   state machine must dedupe on `tool_name`+`tool_input`.
2. **Subagent attribution is solid for unnamed Task/Agent-tool subagents — and absent for named
   agents (teammates).** A verified trace on this machine showed an `Agent` call with a `name`
   becomes an in-process teammate and emitted **zero** SubagentStart/SubagentStop events. Hook
   coverage of teammates is limited to `TeammateIdle` plus `~/.claude/teams/session-<id>/config.json`.
3. **There is no subagent failure signal.** The captured `SubagentStop` payload has no
   `error`/`is_error`/`status`/`exit_code` (corroborated by anthropics/claude-code#27755 and by
   this machine's own hook scripts, which probe those fields and always find them empty).
   `AgentFailed` is inferable only from the subagent's transcript.
4. **The tool that spawns subagents is named `Agent`, not `Task`, on 2.1.220** (live-verified;
   the model refused to spawn via `Task`, which is the task-list tool). Docs and older material
   that say "Task tool" are stale.
5. **`~/.claude/todos/` does not exist on 2.1.220.** TodoWrite state persists only inside the
   transcript; the native task list lives at `~/.claude/tasks/`. Compaction behavior is
   **entirely unverified** — no compaction record exists in any of the 75 session files on this
   machine, and the PreCompact/PostCompact hooks never fired during the experiments.

---

## 2. Candidate comparison table

| Channel | Identifies subagents | Latency | Sees permission requests | Sees tool results | Survives compaction | Works attached to already-running session |
|---|---|---|---|---|---|---|
| **Hooks (command)** | **Y** — `agent_id`/`agent_type` on Pre/PostToolUse + SubagentStart/Stop + PermissionRequest `[captured]`. Named agents/teammates: **N** `[captured]` | <60 ms vs transcript; PermissionRequest ≤64 ms after the blocked PreToolUse `[captured]` | **Y** — PermissionRequest hook `[captured]`: full `tool_name`+`tool_input`, `agent_id` on subagent prompts; grant = PostToolUse, manual deny = no event (absence) | Y — PostToolUse `tool_response` `[captured]` | Unverified — hooks are per-invocation so presumably yes; PreCompact/PostCompact never fired `[docs]` | **Y** — settings re-read per invocation `[captured]`; long-lived watcher must live outside the hook |
| **Transcript tailing** | **Y** — per-agent files `subagents/agent-<id>.jsonl` + `.meta.json` (description, spawning toolUseId) `[captured]` | Seconds; append-time; main file silent while subagents run `[captured]` | **Partial** — pending prompt writes **nothing** (dangling tool_use only) `[captured]`; deny leaves `is_error` tool_result `[captured]` | **Y** — full `toolUseResult` incl. stdout/stderr/patches `[captured]` | Unverified — no compaction specimen exists on this machine | **Y** — pure file reads |
| **Sessions registry** `~/.claude/sessions/<pid>.json` | N (session-level; subagent work folds into `status:"busy"`) `[binary]` | Event-driven on status transition (not a heartbeat; stamped only on change) `[captured]` | **Y** — `status:"waiting"` + `waitingFor:"permission prompt"` `[binary]`, not caught live | N | Presumably (session-level file); unverified | **Y** — zero setup, pure file watch |
| **stream-json** (`--output-format stream-json --include-hook-events`) | Y — `parent_tool_use_id` `[captured]` | Streaming, immediate | Y — `permission_denials` in result; hook events forwarded `[captured]` | Y | Unverified | **N** — print-mode only; cannot attach to an interactive session `[captured]` |
| **Todos dir** `~/.claude/todos/` | — | — | — | — | — | **Does not exist on 2.1.220** `[captured]`; zero todos-path strings in the binary `[binary]` |
| **Tasks dir** `~/.claude/tasks/<dir>/N.json` | N (per-session; no agent field) `[captured]` | mtime on task change `[captured]` | N | N | Unverified | Y |
| **OTel (logs/metrics/traces)** | Y — `subagent.spawn` metric, `parent_tool_use_id` `[binary]` | 5–60 s export intervals `[docs, unverified]` | **Y** — `claude_code.tool.blocked_on_user` span, `tool_decision` log `[binary]` | Y — `tool_result` log event `[binary]` | Unverified | **N** — env-gated at process start; not enabled on this machine |
| **IDE socket** `~/.claude/ide/<port>.lock` | Unverified | ws-live | Unverified | Unverified | Unverified | Only if an IDE is attached; **empty on this machine** `[captured]` |
| **Statusline** | N | ~sub-second when it fires; cadence unverified | N — payload has `permission_mode`, not prompt state `[binary]` | N | Unverified | Unverified (requires settings change) |
| **history.jsonl** | N | On prompt submit | N | N | Y (independent file) | Y |
| **teams/config.json** | Teammates only (the hook-invisible population) `[captured]` | On join/leave | N | N | Y | Y |
| **`/tmp/agent-panes/state-*`** (user's existing hook infra) | Y — pre-digested per-agent metrics `[captured]` | ~1 s throttle | N | Last tool only | N/A | Y, but only while the user's it2 hooks stay installed — do not build on it |

---

## 2.1 Sessions-registry rows: tabs and background jobs `[captured 2026-09-15]`

A session's work does not always run under the session id its tab started
with. Claude Code can park it on a background job process, which gets its own
registry row and its own session id. Both rows are live at once:

```json
{"pid":18643,"sessionId":"10e378ef-…","kind":"interactive","status":"idle","parkedJobId":"e9e4ae8e","name":"dev-3d"}
{"pid":26106,"sessionId":"e9e4ae8e-…","kind":"bg","status":"busy","jobId":"e9e4ae8e","name":"Pull latest tradefloor and tradefloor-design"}
{"pid":26062,"sessionId":"98f87989-…","kind":"bg","status":"idle","jobId":"98f87989","spare":true,"name":"98f87989"}
```

| Field | Tag | Notes |
|---|---|---|
| `kind` | `[captured]` | `"interactive"` for a terminal tab, `"bg"` for a job process |
| `jobId` | `[captured]` | the job a `bg` row runs; the first 8 chars of its own session id in every specimen |
| `parkedJobId` | `[captured]` | on the INTERACTIVE row: the job it has handed its work to. The only link between the two rows |
| `spare` | `[captured]` | a warmed `bg` process holding no work. Whether it is cleared when the process is given a job is **unverified** |
| `name` | `[captured]` | a bg row doing real work carries the tab's ai-title; a spare carries its own job id |

While the park lasts, the tab is `idle` and every agent, token and permission
prompt belongs to the bg session. A pane attached to the tab's id sees none of
it: hook payloads carry the job's `session_id`, so they go to the job's socket
(`agentpane-<session>.sock`) and its transcript, both of which the pane is not
watching. Observed live on 2026-09-15: a pane pinned at 08:51 still drew the
tab's idle row at 14:44 with three agents running under `e9e4ae8e`.

agentpane resolves this in `registry.Follow`: the attach target is the pinned
row, or the row whose `jobId` matches its `parkedJobId`. `followSession`
re-reads the registry every second, so the hop happens mid-session and
reverses when the work returns to the tab. The pane's state file and run
history stay filed under the TAB's id, because that is what `--oneline` and
the auto-opened pane are named after.

---

## 3. Hooks in detail

### 3.0 Registration mechanics on this machine

- `~/.claude/settings.json` is a **symlink into `/Users/alex/Dev/dotfiles`**
  (installed by that repo's `install.sh`). Any installer edit lands in that repo's working
  tree — agentpane's installer must know this and must not break the symlink.
- Every hook event maps to an **array** of `{matcher, hooks:[...]}` groups. Multiple groups per
  event coexist (two `PreToolUse` groups run today). agentpane appends its own group and never
  touches existing ones.
- Hooks are **re-read per invocation** — edits apply live to running sessions, no restart.
- All 11 event names tested (`PreToolUse`, `PostToolUse`, `Notification`, `UserPromptSubmit`,
  `Stop`, `SubagentStart`, `SubagentStop`, `SessionStart`, `SessionEnd`, `PreCompact`,
  `TaskCompleted`) registered and validated on 2.1.220 `[captured]`. `PermissionRequest` was
  verified separately in a follow-up interactive experiment (see its section below) — it
  registers, fires, and an exit-0/no-output handler is a pure observer.
- **Process-tree reaping** `[captured, hard-won]`: anything a hook backgrounds is killed when the
  hook returns. Hooks must only append a line to a log/FIFO that the standalone agentpane process
  reads. (The user's own pane tooling learned this: a hook-backgrounded pane close gets reaped.)
- A read-only observer hook must **always exit 0 and never write stderr with exit 2** — exit 2 on
  `PreToolUse` denies the tool (that is how the user's `danger-guard.sh` works, by design).
- Timeout budget: the user's `SubagentStart` hook already has a 10 s budget and its locking code
  anticipates hooks killed mid-flight under fan-out. agentpane's hook must be near-instant
  (append + exit).

### 3.1 The user's existing hooks (must not be disturbed)

From `~/.claude/settings.json` `[captured]`:

| Event | Matcher | Hook | Timeout | Notes |
|---|---|---|---|---|
| PreToolUse | `""` | `~/.claude/fetch-usage.sh` (file **does not exist**; silent no-op) | none | leave as-is |
| PreToolUse | `"Bash"` | `danger-guard.sh` | 5 s | exit-2 deny semantics; runs even in auto mode |
| SessionStart | `"startup\|resume"` | `session-brief.sh` | 10 s | emits `hookSpecificOutput.additionalContext` |
| TaskCompleted | — | prompt hook (`claude-haiku-4-5-20251001`, `$ARGUMENTS`) | 30 s | |
| TeammateIdle | — | `teammate-idle.sh` | 5 s | only teammate-signal hook seen firing |
| Stop | — | `fetch-usage.sh` (missing; no-op) | none | |
| SubagentStart | — | `it2-pane-hook.sh start` | 10 s | drives iTerm2 panes |
| SubagentStop | — | `it2-pane-hook.sh stop` | 10 s | |

`~/.claude/settings.local.json` has permissions only, no hooks. No project-level
`/Users/alex/Dev/.claude/settings.json` exists. Note `permissions.defaultMode` is
`"auto"` with `Bash(*)` allowed — permission prompts are **rare on this machine**, which is why
interactive-prompt evidence is scarce.

### 3.2 Fields common to (nearly) all captured payloads

| Field | Type | Tag | Notes |
|---|---|---|---|
| `session_id` | string (UUID) | `[captured]` | identical for main and subagent events |
| `transcript_path` | string | `[captured]` | **always the MAIN session jsonl**, even on subagent events |
| `cwd` | string | `[captured]` | |
| `prompt_id` | string (UUID) | `[captured]` | absent on SessionStart |
| `permission_mode` | string | `[captured]` | **unreliable**: reported `"default"` even under `--permission-mode manual`; `"auto"` on UserPromptSubmit but `"default"` on later events in the same session |
| `hook_event_name` | string | `[captured]` | |

### 3.3 Per-event schemas (core events, with captured payloads)

#### SessionStart

```json
{"session_id":"292ef2e2-...","transcript_path":"/Users/alex/.claude/projects/<munged-cwd>/292ef2e2-....jsonl","cwd":"<dir>","hook_event_name":"SessionStart","source":"startup"}
```

| Field | Tag | Notes |
|---|---|---|
| `source` | `[captured]` `"startup"`; matcher values `startup\|resume` proven by the user's config; `clear`/`compact`/`fork` `[docs]` |
| `permission_mode` | `[docs]` | **conflict: docs list it; the captured payload lacks it** |

#### SessionEnd

```json
{"session_id":"...","transcript_path":"...","cwd":"...","prompt_id":"...","hook_event_name":"SessionEnd","reason":"other"}
```

- `reason:"other"` captured even when the session was killed with Ctrl-C ×2 + SIGTERM; `Stop` did
  **not** fire for that interrupted turn `[captured]`. Full reason enum
  (`clear|resume|logout|prompt_input_exit|bypass_permissions_disabled|other`) `[docs]`.
- Docs claim a shared 1.5 s timeout budget for SessionEnd hooks `[docs]` — keep the agentpane
  hook trivial.

#### UserPromptSubmit

```json
{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"auto","hook_event_name":"UserPromptSubmit","prompt":"Use the Agent tool to spawn..."}
```

- **Conflict: docs name the field `user_prompt`; the captured field is `prompt`.** Captured wins.
- Blocking-capable (exit 2) `[docs]` — agentpane must always exit 0.

#### PreToolUse — main agent

```json
{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":"Execute bash, read file, report done","prompt":"Run the bash command `echo subagent-was-here`...","subagent_type":"general-purpose","run_in_background":false},"tool_use_id":"toolu_01FzZb..."}
```

#### PreToolUse — subagent (the attribution mechanism)

```json
{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo subagent-was-here","description":"..."},"tool_use_id":"toolu_01AE7J..."}
```

**Subagent attribution `[captured]`:** field-by-field diff against the main-agent payload shows
identical `session_id`, `transcript_path` (both = the MAIN jsonl), `cwd`, `prompt_id`,
`permission_mode`. The **only** difference: subagent events add `agent_id` (17-hex) and
`agent_type`. There is no `isSidechain` field in hook payloads. **Presence of `agent_id` is the
discriminator; its value keys the tree.** (Docs marked this "unverified"; the live experiment
settles it.) PreToolUse fires in every permission mode including auto/bypass `[docs, consistent
with danger-guard running under auto mode on this machine]`.

#### PostToolUse

Same fields as PreToolUse (including `agent_id`/`agent_type` on subagent events) plus:

| Field | Tag | Notes |
|---|---|---|
| `tool_response` | `[captured]` | Read → string; Bash/Agent → object. **Conflict: docs call this field `tool_output`. Captured name is `tool_response`; captured wins.** |
| `duration_ms` | `[captured]` | int |

#### SubagentStart

```json
{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"SubagentStart"}
```

- **No `permission_mode`, no `tool_use_id`** `[captured]` — the payload does NOT carry the
  spawning `Agent` call's tool_use_id. Correlation to the spawn requires the transcript sidecar
  `.meta.json` (which has `toolUseId`) or event ordering.
- **No `agent_transcript_path` at start** `[captured — both the live experiment and the user's
  it2-pane-hook, which probes for it and always reconstructs by glob]`. No description field.
- `agent_name` was read by the user's scripts and may be empty for unnamed agents
  `[captured via script source]`; it did not appear in the experiment's captured payload —
  treat as optional.

#### SubagentStop

```json
{"session_id":"292ef2e2-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"fa4d298b-...","permission_mode":"default","agent_id":"ada0620f224b32d63","agent_type":"general-purpose","hook_event_name":"SubagentStop","stop_hook_active":false,"agent_transcript_path":"/Users/alex/.claude/projects/<munged-cwd>/292ef2e2-.../subagents/agent-ada0620f224b32d63.jsonl","last_assistant_message":"done","background_tasks":[],"session_crons":[]}
```

| Field | Tag | Notes |
|---|---|---|
| `agent_transcript_path` | `[captured]` | present at **stop** (absent at start) |
| `last_assistant_message` | `[captured]` | agent's final text |
| `stop_hook_active`, `background_tasks`, `session_crons` | `[captured]` | |
| `stop_reason`, `effort` | `[docs]` | **conflict: docs list them; the captured payload lacks both.** Do not depend on them. |
| `error`, `is_error`, `status`, `exit_code` | — | **absent** `[captured + #27755 + this machine's pane-finalize.py, which probes and always finds them empty]`. **No failure signal.** |

#### Stop (main agent turn end)

```json
{"session_id":"...","transcript_path":"...","cwd":"...","prompt_id":"...","permission_mode":"default","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"finished","background_tasks":[],"session_crons":[]}
```

Fires when the main agent finishes a response turn; did NOT fire when the session was interrupted
(SessionEnd fired instead) `[captured]`. Subagents never fire Stop — they fire SubagentStop
`[docs, consistent with capture]`.

#### Notification — the (delayed) permission nudge

```json
{"session_id":"56d4684f-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"79eb4307-...","hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"}
```

**Permission-request visibility mechanism `[captured, interactive pty]`:**

- Sequence `[captured, three interactive pty runs]`: `PreToolUse` → dialog on screen (+36 ms) →
  `PermissionRequest` (+64 ms) → **Notification (+6.1 s, consistent across runs)** with
  `notification_type:"permission_prompt"`.
- The Notification payload has **no `tool_name`, no `tool_use_id`, and no `agent_id`** — even
  when the prompt was raised by a subagent — so it cannot attribute the prompt. Docs claim a
  `severity` field; the captured payload lacks it (conflict; captured wins).
- Notification is therefore a delayed *nudge* that fires while the dialog has already been
  visible ~6 s — not the appearance signal. The instant, attributable signal is
  `PermissionRequest` (next section); keep Notification and the sessions-registry `waitingFor`
  as confirmers.
- **On grant:** no dedicated event — tool simply runs; `PostToolUse` fires normally `[captured
  headless-allow path; interactive-grant path inferred]`.
- **On deny:** no PostToolUse ever; **no dedicated hook event for manual denial** `[docs]`. The
  only record is the transcript: user line with `tool_result {is_error:true, content:"The user
  doesn't want to proceed with this tool use. ..."}` `[captured from transcripts]`.
- **Headless (`-p`):** non-allowlisted tool is auto-DENIED with **no prompt and no Notification**;
  the only hook trace is a PreToolUse with no matching PostToolUse `[captured]`.
- Other `notification_type` values (`idle_prompt`, `agent_needs_input`, `agent_completed`, ...)
  `[docs]` — untested here.

#### PermissionRequest — the primary blocked-on-user signal `[captured, verified 2026-08-01]`

Live-verified in two interactive pty experiments (main-agent prompt; subagent-raised prompt).
Fires ~64 ms after the blocked `PreToolUse` and ~28 ms after the dialog renders — real time.

Main-agent prompt (Test A):

```json
{"session_id":"0d04849b-...","transcript_path":"...jsonl","cwd":"<dir>","prompt_id":"9a63c0ff-...","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /private/tmp/permreq-probe-a","description":"..."},"permission_suggestions":[{"type":"addDirectories","directories":["/private/tmp"],"destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]}
```

Subagent-raised prompt (Test B) — identical shape plus the attribution pair:

```json
{"session_id":"fcd32602-...","...":"...","agent_id":"aff1225aad226c58e","agent_type":"general-purpose","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /private/tmp/permreq-probe-b","description":"..."}}
```

| Fact | Tag | Notes |
|---|---|---|
| Full `tool_name` + `tool_input` | `[captured]` | both tests |
| `agent_id`/`agent_type` on subagent prompts | `[captured]` | **subagent prompts are fully attributable**; the dialog itself renders "from the general-purpose agent" |
| **No `tool_use_id`** | `[captured, docs agree]` | correlate to the PreToolUse via `tool_name`+`tool_input`+`prompt_id`(+`agent_id`) |
| `permission_suggestions` entry types are context-dependent | `[captured]` | `addDirectories`+`setMode` captured; docs' `addRules` example is just one shape |
| Observe-only handler is safe | `[captured]` | exit 0, no stdout → dialog renders normally with all options and the human choice is honored; the hook neither allows nor denies |
| Grant path | `[captured]` | `PostToolUse` for the tool ~0.9 s after the keypress, `agent_id` preserved |
| Manual deny path (Esc) | `[captured]` | **no hook event at all** — deny = a PermissionRequest never followed by its PostToolUse; the transcript records the `is_error` tool_result |
| Headless caveat | `[docs]` | in non-interactive sessions, if no hook returns a decision the call is auto-denied — a property of headless mode, not of an observer hook |
| Retry duplication | `[captured]` | while a prompt sat unanswered ~2.5 min, the background subagent retried under **different `agent_id`s** (plus a transient `SubagentStop` with empty `agent_type`) — expect duplicate PermissionRequests per logical ask; dedupe on `tool_name`+`tool_input` |

`PermissionDenied` `[docs]` fires only for auto-mode classifier denials (which do occur on this
machine — 6 `automode-blocked` transcript records found), not manual Esc denials; still untested.

#### TeammateIdle

Fires on 2.1.220 `[captured — user's teammate-idle.sh receives it]`; at least `agent_type`
(script default `"teammate"`). Docs add `teammate_name`, `team_name` (deprecated) `[docs]`. This
is the **only** hook signal observed for teammates/named agents.

#### TaskCompleted

Exists and is wired on this machine as a prompt hook (`$ARGUMENTS` = task metadata); exact field
names unverified `[captured registration only]`. `TaskCreated` `[docs]`.

#### PreCompact / PostCompact — `[docs only]`

Registered without error but **never fired** in any experiment (no compaction occurred). Docs:
`trigger:"manual"|"auto"`, `custom_instructions` (Pre), `compact_summary` (Post). Untested.

### 3.4 Remaining 2.1.220 events (docs-only catalog, none live-verified here)

| Event | Key payload fields `[docs]` | Blocking | Relevance to agentpane |
|---|---|---|---|
| StopFailure | `error` (rate_limit/overloaded/...), `error_details`, `last_assistant_message` | No | Optional ErrorRaised source |
| PostToolUseFailure | `tool_name`, `tool_input`, `tool_use_id`, `error`, `is_interrupt`, `duration_ms` | Feedback only | **High value if real** — would give ToolEnd(failure) directly; verify |
| PostToolBatch | `tool_calls[]` | Yes | Low |
| UserPromptExpansion | `user_input`, `expanded_prompt` | Yes | Low |
| MessageDisplay | message content (fields unverified) | No | Low; 10 s timeout |
| PreCompact/PostCompact | see above | Pre: yes | CompactStarted/Finished, unverified |
| ConfigChange | `source`, `file_path` | Yes | Detect settings edits mid-session |
| InstructionsLoaded | `source`, `file_path` | No | Low |
| CwdChanged | `cwd`, `old_cwd` (unverified) | No | Low |
| FileChanged | `file_path` | No | Low |
| WorktreeCreate/Remove | `worktree_path`, `branch` (unverified) | Yes | Low |
| TaskCreated | `task_id`, `task_subject`, `task_description`, `teammate_name` | Yes | Task display |
| Elicitation/ElicitationResult | `mcp_server_name`, `message`, `elicitation_id` | Partial | MCP input dialogs — a second "blocked on user" class; untested |
| Setup | `trigger` | No | N/A |
| DirectoryAdded | (added v2.1.219) | — | Untested |

### 3.5 Hook execution model

- Command hooks are synchronous; multiple hooks per event run in parallel, each with its own
  stdin copy `[docs; parallel-stdin corroborated by two coexisting PreToolUse groups both reading
  stdin fully]`.
- Measured latency `[captured]`: PreToolUse fires **+26 to +58 ms after** the assistant
  `tool_use` transcript line's embedded timestamp; PostToolUse fires **4–8 ms before** the
  `tool_result` line's timestamp. Hooks and transcript are effectively simultaneous (<60 ms).
- Default timeouts `[docs]`: 600 s for tool/subagent events, 30 s UserPromptSubmit, 10 s
  Notification/MessageDisplay, 1.5 s shared for SessionEnd. Per-hook `timeout` override in config.
- Env: hooks inherit the settings.json `env` block + terminal env; `${CLAUDE_PROJECT_DIR}`
  expands in command paths `[docs]`.

---

## 4. Transcripts in detail

### 4.1 Layout and discovery

```
~/.claude/projects/<munged-cwd>/
  <session-uuid>.jsonl                          # main session transcript
  <session-uuid>/                               # per-session dir (created on demand)
    subagents/agent-<17hex>.jsonl               # one file PER subagent (Agent tool)
    subagents/agent-<17hex>.meta.json           # {agentType, description, toolUseId, spawnDepth}
    subagents/workflows/wf_<id>/agent-<17hex>.jsonl + .meta.json   # Workflow agents
    subagents/workflows/wf_<id>/journal.jsonl   # {"type":"started"|"result", agentId, ...}
    workflows/wf_<id>.json, workflows/scripts/  # workflow defs + script source
    tool-results/toolu_*.txt                    # spilled oversized tool outputs
```

- **Munging** `[captured]`: cwd with `/` (and presumably other non-alphanumerics) → `-`:
  `/Users/alex/Dev` → `-Users-alex-Dev`. Reverse mapping is ambiguous — confirm
  by reading `.cwd`/`.sessionId` from the candidate file's first line.
- **Better discovery**: `~/.claude/sessions/<pid>.json` gives pid → `sessionId` + `cwd` directly
  (check liveness with `kill(pid,0)`; stale PID files persist) `[captured]`.
- **"Active now"** ≈ main jsonl mtime within seconds **OR** any `subagents/**/agent-*.jsonl`
  currently growing — the main file can be minutes stale while subagents run `[captured]`.
- Environment quirk `[captured]`: an interactive session spawned from *inside* another Claude
  session inherits `CLAUDE_CODE_CHILD_SESSION` and writes **no transcript at all** (hooks still
  fire). Top-level terminal sessions should persist normally (that specific case unverified).
- **`CLAUDE_CODE_CHILD_SESSION` is injected into EVERY process Claude Code spawns** — hooks and
  Bash tool calls alike — not only into nested sessions `[captured 2026-08-01]`. Proof: a
  top-level `claude` on ttys005 had no such variable in its own environment (`ps eww`), while the
  hook it spawned reported `CLAUDE_CODE_CHILD_SESSION=1`. **A hook therefore cannot detect nesting
  by reading its own environment** — the answer is always "nested". The meaningful test is whether
  the *claude process itself* (the hook's parent) carries the variable, which is what nesting
  actually inherits. This silently disabled agentpane's auto-open until it was traced.

### 4.2 Line schema (top-level `.type` values, union over 4 sessions incl. one live) `[captured]`

| type | Key fields | Notes |
|---|---|---|
| `assistant` | `uuid`, `parentUuid`, `sessionId`, `timestamp`, `message` (full API message: `model`, `content[]`, `usage`), `requestId`, `cwd`, `gitBranch`, `version`, `isSidechain` | usage on **every** assistant line |
| `user` | same commons + `message` (tool_results or human prompt), `promptId`, `toolUseResult`, `toolDenialKind`, `permissionMode`, `isMeta` | carries both tool results and prompts |
| `system` | `subtype` (`turn_duration`, `stop_hook_summary`, `away_summary`), `durationMs`, `pendingWorkflowCount`, `pendingBackgroundAgentCount`, hook counters | `pending*Count` = direct running-agents count at each turn end |
| `attachment` | `attachment.type`: agent_listing_delta, command_permissions, queued_command, hook_success, ... | agent_listing_delta lists agent *types*, not instances |
| `queue-operation`, `file-history-snapshot`/`-delta`, `ai-title`, `last-prompt`, `mode`, `permission-mode`, `pr-link` | small metadata records | rewritten per turn |

`ai-title` is the only metadata record agentpane consumes: `{"type":"ai-title","aiTitle":"Build
agentpane — live TUI monitor for Claude Code subagents","sessionId":"…"}` — three fields, no
timestamp, no uuid. It is Claude Code's own short title for the session and the honest source of
main's row label (§6 SessionTitled); nothing else in any channel carries a short workstream
description, and the alternative (the raw user prompt) runs to whole sentences.

No `summary`, `progress`, or `compact_boundary` type exists in any of the 75 files on this machine.

Content blocks inside `message.content[]`: `text`, `thinking`, `tool_use {name, input, id}`,
`tool_result {tool_use_id, is_error, content}` `[captured]`.

### 4.3 Subagent representation `[captured]`

- Subagent activity is **NOT interleaved** in the main jsonl: all 3,128 message lines across 4
  main files have `isSidechain:false`/absent and no `agentId`. Each subagent gets its own file.
- Spawn: assistant `tool_use {name:"Agent", input:{description, prompt, subagent_type?, model?,
  name?}}`. The paired user line's `toolUseResult` = `{agentId, status:"async_launched", isAsync,
  outputFile:"/private/tmp/claude-501/<proj>/<session>/tasks/<agentId>.output", resolvedModel,
  description, prompt, canReadOutputFile}` — **this is where agentId first appears**, before the
  subagent file exists.
- Subagent file: every line carries `agentId` (= filename), `sessionId` (parent),
  `isSidechain:true`, `userType:"external"`, its own uuid/parentUuid chain (root
  `parentUuid:null`); assistant lines add `attributionAgent`.
- Sidecar `.meta.json` = `{agentType, description, toolUseId, spawnDepth}` — the **agentId ↔
  spawning tool_use_id join**, and it appears at spawn time before first agent output (good
  early-detection signal). Workflow-agent meta lacks description/toolUseId
  (`{"agentType":"workflow-subagent","spawnDepth":1}`); display name recoverable only from the
  workflow script filename slug or the first user line's prompt text.
- Stable id: **yes** (`agentId`, 17-hex, everywhere). Display name: **yes** for Agent-tool
  subagents (`input.description` + meta); `subagent_type` often null for unnamed agents.

### 4.4 Tool I/O, tokens, thinking `[captured]`

- `tool_use.input` is verbatim and complete (Bash `{command, description}`, Edit `{file_path,
  old_string, new_string}`, ...).
- Results appear **twice** on the paired user line: `message.content[].tool_result` (API view)
  and top-level `toolUseResult` (rich view: Bash → `{stdout, stderr, interrupted, ...}`; Edit →
  `{filePath, structuredPatch, userModified, ...}`).
- Oversized outputs spill to `tool-results/toolu_*.txt`; no path reference found inside the
  transcript (linkage mechanism unverified); spill-file mtime is a "tool just finished" signal.
- `message.usage` on every assistant line (input/output/cache tokens, model), including inside
  agent files — the only per-agent token source found.
- **What agentpane counts:** `input_tokens + output_tokens +
  cache_creation_input_tokens`. `cache_read_input_tokens` is excluded: it is the
  whole prompt re-read from cache each turn, it is far the largest of the four,
  and it grows with the length of the conversation rather than with anything the
  agent did (a 30-turn agent on a 300k context reports ~10M of it while having
  written a few thousand tokens). Every number in the pane derives from this sum
  — the row's k/s burn rate, the token column, the run summary — and a rate that
  tracks context size answers nothing. See `usage.total` in
  `internal/source/transcript/parse.go`.
- **Token granularity (SPEC §3.4 asked):** usage arrives **once per assistant message, at
  message completion** — never incrementally during generation. Per-agent token *deltas* are
  therefore available (each agent file carries its own usage stream), but each message's tokens
  land in a single sparkline bucket. That is the "coarse but still informative" case §3.4
  anticipates: keep `spark = "tokens"` as the default; the automatic fallback to `calls` applies
  only if an agent's token stream is entirely absent.
- `thinking` blocks present in main and agent files.

### 4.5 Permission traces in transcripts `[captured]`

| Situation | Transcript signature |
|---|---|
| Prompt pending (unanswered) | **Nothing is written.** Only signature: dangling `tool_use` (no matching `tool_result`) + no subsequent `turn_duration` system line + stalled mtime |
| User denies interactively | user line `tool_result {is_error:true, content:"The user doesn't want to proceed with this tool use. ..."}`, `toolDenialKind:null` |
| Auto-mode classifier denies | `toolDenialKind:"automode-blocked"`, tool_result text "Permission for this action was denied by the Claude Code auto mode classifier. Reason: ..." |
| User grants | no record beyond the eventual normal tool_result |

Per-subagent permission blocks inside `agent-*.jsonl`: **unverified — no instance found** (this
user runs Auto Mode; prompts are rare).

### 4.6 Write latency, ordering, integrity `[captured]`

- Freshness: seconds, per-append. Live main-file mtime matched max internal timestamp to the
  second; live agent files grew within 1–20 s of "now".
- **Not strictly timestamp-ordered**: 33 regressions in a 12.7 MB file (mostly 1 ms inversions,
  worst ~68 s on a `queue-operation`). **Order by file position, never by timestamp.**
- Last lines of all inspected files parsed as complete JSON, but atomicity is **unverified** — a
  tailer must tolerate a torn final line (buffer until newline).
- uuid/parentUuid: single root per file; 916/917 chained in the largest file.

### 4.7 Compaction — **unverified, no specimen exists**

No compaction record of any kind in any of the 75 session files on this machine (searched
`"type":"summary"`, `isCompactSummary`, `compactMetadata`, `subtype:"compact*"`, `microcompact`,
continuation prose). Whether uuid chains reset, whether sidechain files survive, and what
PreCompact/PostCompact actually deliver: **all unverified**. agentpane must treat a
`SessionStart(source=compact)` hook or any future compact marker as a "resync from disk" trigger
rather than assuming continuity.

---

## 5. Todo files

**`~/.claude/todos/` does not exist on this machine's 2.1.220** despite heavy daily usage
`[captured]`, and the binary contains zero todos-directory path strings (while still containing
the `TodoWrite` tool and its `{content, status, activeForm}` shape) `[binary]`. The legacy
`<sessionId>-agent-<agentId>.json` channel is gone. **Do not build against it.**

How agentpane gets "main's current todo item":

1. **Transcript**: `TodoWrite` tool_use blocks in the main jsonl carry the full todo array
   (`{content, status, activeForm}`); the current item is the one with `status:"in_progress"`
   `[captured tool shape via binary + transcript tool_use mechanics; specific TodoWrite lines
   inferred]`. This is the only per-todo source.
2. **Native task list**: `~/.claude/tasks/<dir>/N.json` with
   `{id, subject, description, status, blocks, blockedBy}`; dir named by full session UUID or
   `session-<first8>`; mtimes track transitions live `[captured]`. Only populated when the
   session actually uses the task system (current interactive sessions' dirs are empty).
   `in_progress`/`pending` status values are implied but unverified (only `completed` observed).

If neither source has data, render honest absence.

---

## 6. Mapping to the agentpane event taxonomy

| Event Kind | Source | Field(s) / mechanism | Tag |
|---|---|---|---|
| SessionStart | SessionStart hook | `source`, `session_id`, `transcript_path` | `[captured]` |
| SessionEnd | SessionEnd hook | `reason` (fired even on Ctrl-C/SIGTERM kill) | `[captured]` |
| SessionIdle | sessions registry | `status:"idle"` in `~/.claude/sessions/<pid>.json` | `[binary]` enum; schema `[captured]` |
| UserPromptSubmitted | UserPromptSubmit hook | `prompt` (NOT `user_prompt`), `prompt_id`; corroborate `history.jsonl` | `[captured]` |
| SessionTitled | **Transcript tail only — no hook carries this.** Main jsonl line `{"type":"ai-title","aiTitle":"…","sessionId":"…"}`: exactly those three fields, **no timestamp**, so ingest time is used. Claude Code's own short session title ("Locate panel config in Teradata connector code"), rewritten per turn — take the LAST one seen. Present in 75/75 transcripts on this machine; observed lengths 40–65 chars, one distinct title per session in this corpus. Never appears in an `agent-*.jsonl`; a copy there would name the session, not that agent, so it is ignored rather than misattributed | | `[captured 2026-08-02]` |
| CompactStarted | PreCompact hook | `trigger` | `[docs]` — registered OK, **never observed firing**; render as best-effort |
| CompactFinished | PostCompact hook / SessionStart(source=compact) | `compact_summary` / `source` | `[docs]` — unverified |
| AgentSpawned | **Transcript only.** `agent-<id>.meta.json` sidecar (`agentType` + `description` + `toolUseId`), or the Agent call's `toolUseResult.agentId` join. The parent's PreToolUse (`tool_name=="Agent"`, `tool_input.description/subagent_type`, `tool_use_id`) has **no `agent_id`** — it identifies the PARENT, so an AgentSpawned built from it is keyed to `main`: it names no agent and credits main with a spawn. hooksrc holds that description in a per-session queue for the SubagentStart instead (see `hooksrc.Mapper`) | `[captured]` |
| AgentStarted | SubagentStart hook | `agent_id`, `agent_type`; **no description and no tool_use_id**, so a strict id join to the spawn is impossible from hooks alone. A bare SubagentStart therefore claims **nothing** — most of them are phantoms (see below), and a claiming starter handed real descriptions to phantoms. hooksrc defers the claim until the id **corroborates**: its own PreToolUse/PostToolUse/PermissionRequest, or a SubagentStop carrying `agent_transcript_path`. Then it claims the oldest unclaimed spawn issued at or before that id's start, with a compatible `subagent_type`; a start with an EMPTY `agent_type` never claims; when several candidates differ (many Agent calls in one turn) all are discarded and the agent stays **unnamed** rather than risk a crossed name (C8). Unnamed rows fall back to `<agent_type>-<digest(id)>` (e.g. `explore-k3f9dz`) — a digest, not a prefix, because announced ids share long heads — never `agent-N` | `[captured]` |
| AgentReturned | SubagentStop hook | `agent_id`, `last_assistant_message`, `agent_transcript_path`; PostToolUse of the Agent call gives `tool_response` + `duration_ms` | `[captured]` |
| AgentFailed | **NOT AVAILABLE as an event — render honest absence or mark "inferred".** SubagentStop carries no error/status (`[captured]`, #27755). Inferrable from: subagent transcript tail (`tool_result.is_error` on final exchange), or PostToolUseFailure on the `Agent` call `[docs, untested]` | | `[inferred]` |
| AgentKilled | **NOT AVAILABLE.** No kill signal captured anywhere. Weak inference: SubagentStart with no SubagentStop by SessionEnd; note stop-without-start also occurs (teammate edge / hook timeout) | | `[inferred, weak]` |
| ToolStart | PreToolUse hook | `tool_name`, `tool_input`, `tool_use_id`; `agent_id` present ⇒ subagent's tool | `[captured]` |
| ToolEnd | PostToolUse hook | `tool_use_id`, `duration_ms`; failure path: PostToolUseFailure `[docs, untested]`; deny path: no PostToolUse at all | `[captured]` |
| ToolOutput | PostToolUse `tool_response`; richer: transcript `toolUseResult` (stdout/stderr/patch); oversized: `tool-results/toolu_*.txt` | | `[captured]` |
| FileRead | PreToolUse | `tool_name=="Read"`, `tool_input.file_path` | `[captured]` mechanism |
| FileEdited | PostToolUse | `tool_name` in Edit/Write, `tool_input.file_path`; transcript `toolUseResult.structuredPatch` for the diff | `[captured]` mechanism |
| VerifyStarted/Passed/Failed | **NOT AVAILABLE — render honest absence.** No "verify" concept exists in any channel. Anything shown would be a heuristic over Bash test commands, i.e. invented semantics | | — |
| RetryStarted | **NOT AVAILABLE — render honest absence.** No retry signal in hooks or transcript; `rate_limit_event` exists only in stream-json, which cannot attach | | — |
| TokensUpdated | Transcript tail | `message.usage` on every assistant line, per agent file (main + each `agent-*.jsonl`) — NOT available via hooks | `[captured]` |
| ThinkingStarted/Ended | Transcript tail | `thinking` content blocks; **degraded**: visible only when the containing line is appended, so "started" is really observed-at-completion | `[captured, degraded]` |
| PermissionRequested | **PermissionRequest hook** — `tool_name`, full `tool_input`, `permission_suggestions`; `agent_id`/`agent_type` on subagent prompts; fires ≤64 ms after the blocked PreToolUse. Confirmers: dangling-PreToolUse heuristic `[captured]`, Notification `permission_prompt` +6.1 s `[captured]`, sessions registry `waitingFor` `[binary]`. Dedupe retries on `tool_name`+`tool_input` | | `[captured]` |
| PermissionGranted | Inferred, reliably: `PostToolUse` for the requested tool ~0.9 s after the keypress (`agent_id` preserved). No dedicated event | | `[captured]` mechanism |
| PermissionDenied | Manual deny (Esc) emits **no hook event** `[captured]` — detect as a PermissionRequest never followed by PostToolUse; transcript records `tool_result.is_error` + "The user doesn't want to proceed..." `[captured]`. Classifier denials: `toolDenialKind:"automode-blocked"`; PermissionDenied hook covers classifier only `[docs, untested]` | | `[captured]` |
| NotificationRaised | Notification hook | `notification_type`, `message` (no severity captured) | `[captured]` |
| ErrorRaised | Transcript `tool_result.is_error:true`; hooks: PostToolUseFailure / StopFailure `[docs, untested]` | | `[captured]` transcript path |

**SubagentStart/Stop is not proof an agent exists in any useful sense**
`[captured 2026-08-02, session 6dbbfaa6-71d1-412d-88ba-60f3ddf574bc]` — that session ran **5**
real subagents and the hook stream carried **40 further** `SubagentStart`/`SubagentStop` pairs,
each settling instantly with no tool call, no tokens, no files, and no `last_assistant_message`.
None of the 40 ids matched any of the 5 transcript agents, none had a `.meta.json` sidecar or an
`agent-*.jsonl`, and the session also had an agent TEAM (`~/.claude/teams/session-6dbbfaa6`,
experimental teams flag on). agentpane therefore requires a row to be earned — a name, a spawn
record, one unit of observed work (tool call, file, token, error), or something that needs the
user (a permission ask, a stall) — and reports the remainder as a count (`+40 agents with no
recorded activity`) rather than as rows or as silence (`state.Machine.quiet`, `World.Quiet`).
Merely being alive is NOT an earner: an earlier grace-period rule gave any unsettled agent a row
after a few seconds, which rendered the four ~90 s phantoms of session `6d53f05d` as unnamed rows
and then deleted each row the moment it settled. The decision is also **latched** — once earned, a
row is kept for the rest of the run, so nothing the user has watched can disappear.

**Teammates (named agents): the entire Agent* row family is NOT AVAILABLE via hooks**
`[captured trace 2026-07-28]` — zero SubagentStart/Stop. Coverage limited to
`~/.claude/teams/session-<id>/config.json` membership (`{agentId, name, agentType, tmuxPaneId,
...}`) + TeammateIdle. Render teammates as a distinct, explicitly lower-fidelity node class.

---

## 7. Rejected / secondary channels

| Channel | Why not primary | Keep? |
|---|---|---|
| **stream-json** (`--output-format stream-json --include-hook-events --forward-subagent-text`) | Print-mode only; **cannot attach to an already-running interactive session** `[captured]` — disqualifying for agentpane's core use case. Otherwise excellent (hook lifecycle events, `parent_tool_use_id`, `permission_denials` in result) | Yes, optional: a future "agentpane launches the session itself" mode would prefer it |
| **OTel** | Env-gated at process start — **cannot attach**; 5–60 s export latency; not enabled on this machine. But it is the only channel with a first-class blocked-on-user primitive (`claude_code.tool.blocked_on_user` span) and `tool_decision` on resolution `[binary]` | Yes, optional opt-in for users willing to relaunch with OTLP → local collector |
| **IDE socket** (`~/.claude/ide/<port>.lock`, ws + authToken) | Only exists when an IDE extension is attached; empty on this machine; protocol capabilities unverified | No for MVP |
| **Statusline** | Session-level heartbeat only; payload has no prompt state and no subagent data `[binary]`; requires a settings change; cadence unverified. Sessions registry is strictly better | No |
| **`~/.claude/todos/`** | Does not exist on 2.1.220 | No — see §5 |
| **`/tmp/agent-panes/state-*`** (user's it2 hook infra) | Pre-digested per-agent metrics at ~1 s, but it is the user's personal tooling, coupled to his hooks staying installed, and misses teammates | Read-if-present at most; never a dependency |
| **mcp-logs / history.jsonl / shell-snapshots / process table** | Connection lifecycle, last-prompt, session-start signals only | history.jsonl optionally for "last prompt" display |
| **Peer UDS protocol** | Present in binary (`uds:<socket>`, `findLivePeerBySessionId`) but the socket-path function is compiled to a stub and no sessions file carries `sock` — **inactive in this build**; no LISTEN sockets on any CLI pid `[captured lsof + binary]` | No; re-check on future versions |

---

## 8. Open questions for the owner

**Resolved 2026-08-01 by follow-up interactive experiments** (originally questions 1–2): the
`PermissionRequest` hook fires on 2.1.220 interactive sessions with full tool identity, an
observe-only handler is safe, and subagent-raised prompts carry `agent_id`/`agent_type` — fully
attributable. It is now the designated primary PermissionRequested source (§3.3). New findings
folded in: no `tool_use_id` on the payload, ~6.1 s Notification lag confirmed as a nudge, and
unanswered prompts can spawn duplicate retried PermissionRequests under fresh `agent_id`s.

1. **Compaction is a complete evidence void.** No specimen among 75 sessions; PreCompact/
   PostCompact never observed firing; transcript behavior across a compact boundary unknown.
   Acceptable to ship with "resync on suspected compaction" and mark CompactStarted/Finished
   best-effort?
2. **AgentFailed/AgentKilled cannot be rendered truthfully from events.** Given SubagentStop
   carries no failure data, the choices are: (a) render only "returned" with the last message,
   (b) add an inferred "likely failed" badge from transcript `is_error` tails, clearly marked as
   inference. Which does the spec prefer?
3. **Teammate (named-agent) fidelity.** Hooks are blind to teammates. Is teams/config.json
   membership + TeammateIdle enough, or should agentpane also tail teammate transcripts (location
   for teammate transcripts on 2.1.220: unverified)?
4. **`stop_reason`/`effort` on SubagentStop and `severity` on Notification**: docs list them,
   captures lack them. Treat docs as aspirational for these fields, or re-test across more
   spawn shapes (background agents had `background_tasks:[]` — payloads may vary)?
5. **Installer target**: `~/.claude/settings.json` is a symlink into the dotfiles repo.
   Should agentpane's installer write through the symlink (dirtying the repo working tree) or
   propose the hook block for manual addition?
6. **Session registry staleness**: stale PID files persist after exit and `statusUpdatedAt` is
   event-driven (490+ s untouched while busy). Liveness = `kill(pid,0)`. Confirm acceptable as
   the SessionIdle/waiting source given no heartbeat exists.
