# agentpane — handoff (revision 3.1)

Drop this folder into the repo and point Claude Code at `SPEC.md`.

- `SPEC.md` — full functional + technical specification.
  - **PART 12** is the change log: the 14 data-source findings and the decision for each, plus the
    revision-3.1 sweep.
- `mock-your-scheme.html` — interactive visual reference, my iTerm2 green-on-black scheme.
- `mock-neutral-palette.html` — same mock, neutral palette (proves it isn't scheme-dependent).

Open a mock in a browser, click the pane, then:

    s  spawn the demo run        j/k  move            SPACE  unfurl tool calls
    ENTER  inspector             ESC  dismiss digest / close inspector
    f  follow mode               o  open file          y  yank error
    t  right column -> tokens    w  toggle 64/44 cols  r  idle state
    [ ]  run history             1-3  switch session

## Revision 3.1 — what changed

**Colour.** Needs-you no longer has a hue. The whole pane is one hue (yours) with four luminance
steps: grey settled · ANSI 2 live · default-fg names and body · bright + reverse video + `⚑` for
needs-you. Live and primary must never resolve to the same index (§3.10.2a). No yellow, no red,
no second hue — error text in the inspector is the only non-green thing in the pane.

**Feedback round 2 absorbed.** Every impossible thing is now gone from the prose as well as the
tables: `x`, `failed`, `killed`, the sparkline-on-an-open-call diagram, `retry 2/3`. Fixed
62-vs-64 (it's 64), `stations[4]`, the split keymap table, the mock filenames, and the dangling
"§3.6 scenario" reference — **the demo cast is now its own numbered section, §2.10.**

**Two clarifications written into the text:**

- **§3.3** — where station pips live: pips + label on the tree row at wide width, pips only at
  narrow, the full station row (with `✓ verified (inferred)`) in the inspector, nothing anywhere
  else. Teammates render no pips at all.
- **§3.20a** — `--oneline` renders from a debounced JSON state file the TUI writes to
  `~/.local/state/agentpane/current.json`, and prints `no agentpane running` (exit 0) when it's
  missing or stale. It never attaches to a Source.

## Suggested opening message

> Read SPEC.md in this folder, then open mock-your-scheme.html and drive it (s, then j/k, space,
> enter, t, w). PART 12 is the change log from our data-source investigation and the 3.1 sweep —
> the spec is reconciled with what the platform actually emits, and §2.10 is the demo cast.
>
> Step 1 of PART 9 is done. Start at step 2: the domain model and its table-driven tests. Do not
> write UI code until the state machine passes.
