# SPEC.md revision 3 — errata

Stale revision-2 remnants found on intake review (2026-08-01). In every case the mocks and/or
PART 12 already encode the intended behavior; per §3.10.5 the mocks are authoritative for layout
and copy. Build against the resolutions below. None of these change a PART 12 decision.

| # | Where | Says | Resolution |
|---|---|---|---|
| 1 | §3.1 heading + diagram, PART 8 | "wide **62** cols", "~62-column right pane" | **64** — §5.1 (`w` toggles 64/44), README, and the mock (`WIDE 64`) all agree |
| 2 | §3.1 diagram footer | `j/k ␣ ⏎ f o y x` | `x` is removed (§5.1); mock footer is `j/k ␣ ⏎ f o y · t tokens` |
| 3 | §3.1 diagram, test-runner row | sparkline `▄▅▃▁···` while inside a quiet `--watch` call | contradicts §2.6a — an open call renders `◐ in Bash` + time-in-call (needs-you once past threshold, since watch is never exempt) |
| 4 | §3.7 item 2 | stall action `x kills it · o opens the log` | §3.12's copy wins: `o opens the log · y yanks the command` |
| 5 | §2.4 transitions | `done\|failed\|killed --+30s--> decayed` | `done --+30s--> decayed` (`failed`/`killed` are cut, §2.3) |
| 6 | §3.5 decay | "settled agent (`done`/`failed`/`killed`)" | `done` only |
| 7 | §3.6 ordering | `… → done/failed → killed` | `ask → stuck → run → queued → done → teammate → unknown` (teammate always last, §3.15) |
| 8 | §2.9 | `stations[5]` | `stations[4]` (§2.2) |
| 9 | PART 9 step 6 | "Inspector, follow mode, `o` / `y` / `x` / toasts" | drop `x` |
| 10 | §5.1 | the x-removal paragraph splits the keymap table; `?` and `ctrl-c` rows orphaned below it | one contiguous table; the x note stands as prose after it |
| 11 | §1.4, PART 6, PART 10 | "replay/reproduce the §3.6 scenario" | dangling since revision 1 — §3.6 is Ordering. The demo scenario is the §3.1 diagram cast (fix-ts2345/api-types, test-runner, explore-schema, typecheck, docs-writer, + contention) |
| 12 | §3.10.5 | refers to the mocks as "v3 - your scheme" and "v2" | actual filenames: `mock-your-scheme.html`, `mock-neutral-palette.html` |

## Deliberate refinements (behaviour the spec left open)

These are not spec errors — they are choices made in build where the spec named a shape but not
its source. Recorded here so the divergence is deliberate and reviewable.

| # | Where | Spec says | Built | Why |
|---|---|---|---|---|
| R1 | §3.1 root row, §2.2 | diagram shows `◍ main  refactor auth` — a short workstream **label** next to `main`; the spec never says where the text comes from | label priority: **session title → current run prompt → main activity → empty**, then budgeted to the columns actually left on line 1 | The first build rendered `Run.Prompt` raw. Real prompts are whole sentences, so main's row was nothing but prompt. Claude Code already writes its own short title into the transcript (`{"type":"ai-title","aiTitle":…}`) — that is the honest short label, taken verbatim, never summarised by agentpane (C8). |
| R2 | §3.1 narrow degradation order | lists the four right-hand elements that drop at 44 | the main-row **label yields first**, before any of them | Same principle, applied to the one element §3.1 did not enumerate: the label is the only variable-length text on the root row, so it is what gives way. The tokens column never moves and the row never wraps at any width. |

The budget rule for R1: `label ≤ width − tokensWidth − 1 (gap) − 6 (`◍ main`) − 2 (separator)`, cut
on a word boundary with the §3.11 `…`, mid-word only when a single word is longer than the whole
budget, measured in display columns so wide and combining runes stay intact. The **full**
untruncated text remains reachable: the inspector renders it as main's description line (PART 4
item 2, the same treatment agent rows get) and `y`/`Y` yank it verbatim.

Ambiguity resolved from the mock template (mock is authoritative for layout):

- **Tree row line 2, right side** = station pips + label at wide width, pips only at narrow
  (matches §3.1 degradation order "station label text (pips remain)"). The full
  `pips + name + ✓ verified (inferred)` line and the `no station data — limited telemetry`
  teammate variant belong to the **inspector** (`stationRow` in the mock).
- Per-agent tokens (§3.18) right-align on that same expanded line 2; `t` swaps the row's right
  column to burn rate.
