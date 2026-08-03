# agentpane — handoff, revision 4

Response to the build report. Point Claude Code at `SPEC.md`; **PART 13 is the whole of this
round** and supersedes anything earlier it disagrees with.

- `SPEC.md` — full spec. PART 12 = the data-source investigation round. **PART 13 = this round.**
- `mock-your-scheme.html` — v6, my iTerm2 green-on-black. Renders everything in PART 13.
- `mock-neutral-palette.html` — same, neutral palette.

Drive the mock: `s` scene (blocked → risky → clear) · `j/k` select · `n` narration density ·
`t` rate/clock column.

## The one new requirement: narration is fixed-height and scrollable

Tree geometry must not move when a sentence gets longer or the scene changes. Standup is 7 rows
with a sticky chip, commentary is 11 rows and scrolls with auto-follow, the tree takes the rest.
Details in §13.2. This was the main thing wrong with the previous mock.

## Rulings on your eight questions (§13.3)

1. **Gauge history** — five cells is enough on the row; put full-run history in the inspector at
   the same cross-agent scale. Nothing on the row narrows to make space.
2. **Stalled rows** — yes, switch to the silence timer. Rate for producing, open-call timer past
   2s, silence timer for stuck/ask. No stalled row ever shows `0.0k/s`.
3. **Narrow 44** — you're right, drop the station *label* first and keep the gauge. Order: label →
   meta suffixes → gauge 5→3 cells → verified marker. Slug and number never drop.
4. **Decayed copy** — "returned". §3.12 was wrong and is withdrawn.
5. **Glyphs** — folded in: `◍ ▣ ▇` and the braille spinner. `▁▂▃▄▅▆` withdrawn with the sparkline.
6. **Verify** — stays an inferred marker, no station. Intent isn't evidence, and stations are the
   one place that means "fact". But split it on exit code: `✓ verified` / `✖ verify failed`.
7. **main's line 2** — replace the todo with **what main is waiting on** (blocked-on → todo →
   activity). Fact 5 makes todos rare, so leading with them leaves the best line in the tree empty.
8. **Suppressed agents** — both. Inline expansion for the glance, inspector for per-agent detail,
   because the one time you care is when you suspect one isn't a phantom.

## Three amendments to your own changes (§13.1)

- **Slug rename must be silent and atomic**, and must not happen at all once the name has appeared
  in a notification or the standup — a name that changes under you reads as two agents.
- **Flatten multi-line tool text on ingest, not at draw time** — and keep the original bytes, since
  `y` must yank what the tool actually ran.
- **Don't suppress an agent inside `stuck_after` of spawn.** "No recorded activity" and "hasn't
  started yet" look identical for the first second or two, and hiding a real agent is much worse
  than showing a phantom.

## Suggested opening message

> Read PART 13 of SPEC.md — it responds to the build report and rules on all eight open questions.
> Open mock-your-scheme.html and press s / n / t to see the fixed-height narration, the shared-scale
> gauge and the rate-vs-clock column rule.
>
> Start with §13.2 (narration regions) and the Q2/Q3 column and degradation rules; those are the
> ones that change shipped behaviour. §13.4 lists the new done criteria.
