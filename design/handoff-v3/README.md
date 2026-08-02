# agentpane — handoff (revision 3)

Drop this folder into the repo (or anywhere Claude Code can read) and point it at `SPEC.md`.

- `SPEC.md` — full functional + technical specification. **PART 12 is the change log** answering
  every point from the data-source investigation; read it first.
- `mock-your-scheme.html` — interactive visual reference in my iTerm2 green-on-black scheme.
- `mock-neutral-palette.html` — same mock, neutral palette (proves the design isn't
  scheme-dependent).

Both mocks are single self-contained HTML files. Open in a browser, click the pane, then:

    s  spawn the demo run        j/k  move            SPACE  unfurl tool calls
    ENTER  inspector             ESC  dismiss digest / close inspector
    f  follow mode               o  open file          y  yank error
    t  right column -> tokens    w  toggle 64/44 cols  r  idle state
    [ ]  run history             1-3  switch session

Everything the platform cannot emit has already been removed from both the spec and the mocks:
no `verify` station, no `failed` status, no kill action, no `ToolOutput`-driven liveness.

## What changed in revision 3

- open-call state `◐ in <Tool>` so a long healthy build no longer looks stalled
- four stations, not five; verify is an inferred marker
- `failed` and `x` (kill) removed — agentpane is strictly read-only
- `teammate` node class for named agents (near-zero telemetry)
- deterministic description → slug rule, with the full description preserved
- permission-ask dedupe (`×n`) and folded retry ghosts; `resolving…` on answer
- multi-session attach rule + session picker
- away-digest via focus reporting, and a four-level escalation ladder
- token placement rule (header total / station line / `t` for burn rate)
- expansion budget so pinning never scrolls the tree

## Suggested opening message

> Read SPEC.md in this folder, then open mock-your-scheme.html and drive it (press s, then j/k,
> space, enter, t, w). PART 12 is the change log from our data-source investigation — the spec is
> already reconciled with what the platform actually emits.
>
> Start at PART 9 step 2 (the domain model and its tests); step 1 is done. Do not write UI code
> until the state machine and its table-driven tests pass.
