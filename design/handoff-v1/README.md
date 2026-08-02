# agentpane — handoff

Drop this whole folder into the repo root (or anywhere Claude Code can read) and point it at
`SPEC.md`.

- `SPEC.md` — the full functional + technical specification. Start here.
- `mock-your-scheme.html` — interactive visual reference in my iTerm2 green-on-black scheme.
- `mock-neutral-palette.html` — same mock, neutral palette.

Both mocks are single self-contained HTML files: open in a browser, click the pane, then `s` to
spawn the demo run. Keys: `j/k` move · `␣` unfurl · `⏎` inspect · `esc` close · `f` follow ·
`o`/`y`/`x` · `w` toggle width · `r` idle · `[`/`]` run history.

Suggested opening message to Claude Code:

> Read SPEC.md in this folder and open mock-your-scheme.html to see the target UI. Start with
> Part 1 only: investigate what Claude Code exposes on this machine and write
> docs/DATA-SOURCES.md. Stop there and show me before writing any other code.
