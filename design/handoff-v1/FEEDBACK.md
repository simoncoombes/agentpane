# Feedback on SPEC.md + mocks, from the data-source investigation

Step 1 of the build (docs/DATA-SOURCES.md) is done. We instrumented a real Claude Code 2.1.220
session on my machine, captured live hook payloads (including interactive permission prompts
driven through a pty), and characterized the transcript files. Most of the design survives
contact with reality, and the headline feature is fully supported. But several events the spec
consumes don't exist on the platform, and a few things the mocks show can't be rendered honestly.
Spec rule C8 (never invent data) is what forces most of these changes.

## What holds up (no changes needed)

- **The alert band works better than specced.** A `PermissionRequest` hook fires within ~64ms of
  the blocked tool call, carrying the tool name and the full command, and when a subagent raised
  the prompt it carries that agent's id and type. The band can name the agent, show the exact
  command, and the wait timer is accurate to well under a second.
- Two-level topology confirmed. Subagents cannot nest; depth is always 1.
- Contention detection is real: per-agent file-edit events with paths exist.
- Per-agent tokens, diff stats (+29 -4), and the inspector's log tail all have real sources.
- Runs bracket cleanly (prompt-submitted and turn-ended events both exist), and there is a
  zero-setup "session is idle / waiting on a permission prompt" signal.

## Events the platform cannot produce - spec changes needed

1. **`ToolOutput` doesn't exist for foreground commands.** Nothing is emitted between ToolStart
   and ToolEnd; output lands only when the command finishes. So the liveness clock (§2.6) counts
   up through any long command, and the sparkline flatlines during a healthy 3-minute build
   exactly like a stalled one, which breaks §3.4's "flatline must read as wrong before the timer
   does". The mock shows test-runner with a moving sparkline during `pnpm test --watch`; in
   reality that row flatlines immediately. Background tasks are the exception (they stream to an
   output file whose growth is a real liveness signal). Suggestion: give "inside a long-running
   call" its own visual (the ◐ glyph is already in the set) so open-tool-and-quiet reads
   differently from stalled-quiet, and accept that stall detection rests on the timer plus the
   allowlist, not the sparkline.

2. **`VerifyStarted/Passed/Failed` has no source.** Station 4 (`verify`, §2.2) can only be a
   pattern-match over Bash commands (test/build/lint), which is a heuristic, and C8 forbids
   invented semantics. Either cut to four stations (spawn, first tool, first edit, return) or
   keep verify and explicitly bless it as a derived, pattern-matched station. Pick one and update
   every five-pip rendering in the mocks.

3. **`RetryStarted` / the `↺n` counter has no source.** Nothing signals a retry. Drop `↺n` from
   the row anatomy (the mock shows `api-types ↺2`) or redefine it as a documented heuristic
   (same command re-issued after a failing ToolEnd).

4. **`AgentFailed` has no source.** The subagent-finished event carries no error/status/exit-code
   field (confirmed live; it's a known upstream gap). Failure is only inferable from the agent's
   transcript tail. Either drop the `failed` status (fold into done, show the last message) or
   render an explicitly-marked "likely failed" inference. The ✖ prefix as specced overpromises.

5. **`x` (kill) is probably unimplementable.** Subagents run in-process inside the main claude
   process: no per-agent process, no documented external kill mechanism, nothing in the binary.
   Recommend removing `x` for v1 (which also makes agentpane purely read-only) and rewording the
   stall action line ("x kills it · o opens the log").

6. **Thinking is observed only at completion**, so `ThinkingStarted` cannot extend the stall
   threshold (§2.5). Replace that rule with the plain threshold.

## New UI surface the mocks don't cover

7. **Teammates (named agents) are nearly invisible to every event source** (no subagent
   lifecycle events, no per-tool events). They need a visibly lower-fidelity node class: no pips,
   no sparkline, no liveness clock, an honest "limited telemetry" tail. Design that state; don't
   render them as peers of regular subagents.

8. **Real agent names are much longer than the mock's.** Display names come from the spawn
   call's description field, a 3-to-5-word phrase ("Verify PermissionRequest hook live" is 34
   chars), not `api-types`. At 44 cols, name plus sparkline plus timer cannot share one line, and
   the spec forbids truncating names. Decide the rule: wrap the name, drop right-side elements at
   narrow width, or derive a slug (and if a slug, where does the full description live?). The
   mock's short kebab names hide this problem entirely.

9. **One blocked prompt can generate several permission requests.** While a prompt sits
   unanswered, a background subagent retries the action under fresh agent ids; we captured
   duplicates for a single logical ask. The band needs a dedupe rule (same tool + same input =
   one entry) and the tree needs a stance on the short-lived ghost agent rows those retries
   create.

10. **Denial is silent.** Granting produces an event within about a second; denying produces no
    event at all (the record appears only in the transcript). Band clearing on deny will lag a
    beat. Fine to accept, but the design should not promise instant resolution.

11. **Multiple concurrent sessions per directory are my daily reality.** The idle state assumes
    one session. Specify the attach rule (newest live session for the cwd) and consider a minimal
    session picker on the idle screen.

## Spec-internal fixes

12. §4 inspector copy: "muted red `#c98d78`" is hardcoded hex, contradicting §3.10 (should be
    ANSI 1/9), and "max 320px" is a pixel unit in a terminal spec (use rows).
13. The tool that spawns subagents is named `Agent` on 2.1.220, not `Task`. Update §1.3/§2.1 so
    nobody wires a matcher or a mental model to the wrong name.
14. Main's todo line: the standalone todos directory no longer exists in 2.1.220; todo text is
    recoverable only from the transcript. The specced fallback (show current activity) stands,
    just know it triggers whenever the transcript source is unavailable.

Nothing here weakens the core concept. The permission story came back stronger than the spec
assumed; the honest-absence rules (C8, §7.9) just need to be applied to verify, retry, failure,
and kill, which were designed before we knew what the platform actually emits.
