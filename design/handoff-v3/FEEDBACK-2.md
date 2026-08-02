# Feedback round 2, on revision 3

Revision 3 lands. All 14 decisions in PART 12 check out against the captured data, and the new
features (away-digest, escalation ladder, open-call state, slug rule, expansion budget, OSC
8/52/9) are all feasible on iTerm2 as specced. The mocks are correct and internally consistent -
we verified the footer, the widths, and the new states directly against the mock source.

The problem is that the spec prose wasn't fully swept: several sections still show revision-2
content that PART 12 itself cut, including three places that render actions or states the
platform cannot produce. Since "the mocks are authoritative" only covers layout and copy, the
state-machine text needs to be right too. Please issue a 3.1 that fixes the following. No new
design work is needed, every item already has a decided answer.

## Impossible things still depicted

1. **`x` survives in three places** after PART 12 #5 removed it: the §3.1 diagram footer
   (`j/k ␣ ⏎ f o y x`), the §3.7 item-2 stall action line (`x kills it · o opens the log`, which
   §3.12 already corrects to `o opens the log · y yanks the command`), and PART 9 step 6
   (`o / y / x / toasts`). The mock footer is already right (`j/k ␣ ⏎ f o y · t tokens`).
2. **The §3.1 diagram shows a sparkline on an open `--watch` call** (test-runner row,
   `▄▅▃▁···  2m50s`). That contradicts §2.6a, which exists precisely because this rendering is
   impossible: an open call shows `◐ in Bash` plus time-in-call. Redraw that row.
3. **`failed` and `killed` still appear in the state machine text** after PART 12 #4/#5 cut them:
   §2.4 (`done|failed|killed --+30s--> decayed`), §3.5 (settled = `done`/`failed`/`killed`), and
   §3.6 ordering (`done`/`failed` → `killed`). Ordering should also say where `teammate` and
   `unknown` sort (§3.15 says teammate is always last): `ask → stuck → run → queued → done →
   teammate → unknown`.
4. **The PART 4 inspector example shows `retry 2/3`.** There is no retry budget anywhere in the
   platform, so the denominator is invented (C8). The retry count is the inferred `↺n`; the line
   should read `↺2 (inferred) · suspects a stale tsbuildinfo` or similar.

## Internal contradictions

5. **62 vs 64.** §3.1 says "wide 62" and PART 8 says "~62-column pane", but §5.1, the README, and
   the mock all say 64. Pick 64 everywhere.
6. **`stations[5]`** in §2.9 should be `stations[4]` (§2.2 cut to four).
7. **The "§3.6 scenario" reference is dangling** (and has been since revision 1): §1.4, PART 6,
   and PART 10 tell `--demo` to replay "§3.6", but §3.6 is Ordering. The demo scenario is the
   §3.1 diagram cast. Point the references at it, or give the scenario its own numbered section.
8. **§5.1's keymap table is split in two** by the x-removal paragraph, orphaning the `?` and
   `ctrl-c` rows. Keep the table contiguous and put the note after it.
9. **§3.10.5 refers to the mocks as "v3 - your scheme" and "v2"** instead of the actual
   filenames (`mock-your-scheme.html`, `mock-neutral-palette.html`).

## Two clarifications to fold into the text

10. **Where station pips live.** §3.3 (row anatomy) and §3.18 (tokens on "the expanded station
    line") read as if they disagree. The mock already answers it: tree line 2's right side is
    pips + station label at wide width, pips only at narrow, and the full station row
    (pips + name + `✓ verified (inferred)`, or the teammate variant) belongs to the inspector.
    Write that into §3.3 so nobody re-derives it from the mock source.
11. **How `--oneline` gets its state.** It runs outside the TUI process, so it either reads the
    sources itself or reads something the TUI publishes. Say which. My preference: the TUI
    maintains a small state file under `~/.local/state/agentpane/` and `--oneline` renders from
    it, falling back to "no agentpane running" when absent.

We logged all of the above with our chosen resolutions in `ERRATA.md` next to the spec, so if
anything here is unclear, that file shows exactly what we'd build in each case. A 3.1 that
absorbs it (or corrects it, if we guessed your intent wrong anywhere) closes the loop.
