# agentpane

<img src="docs/demo.gif" alt="agentpane watching a fan-out" width="100%">

Another terminal UI harness. Written in Go. With a tree in it. I know - how
original.

Claude Code fans out to eight subagents and the transcript turns into a wall of
scrolling text. Worse: one of them hits a permission prompt and then just waits,
silently, while you are looking at a different window.

agentpane draws the tree live in a second pane. One row per agent, what it is
doing right now, and a flag when something is blocked on you.

It is read-only. It cannot send prompts, edit your files, or kill an agent.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/simoncoombes/agentpane/main/get.sh | sh
agentpane install
```

A binary into `~/.local/bin`, then the hooks into `~/.claude/settings.json`. The
installer shows you the diff and asks first. Add `--autopane` and the pane opens
itself with every session. Undo it all with `agentpane install --uninstall`.

Then run `agentpane` in a second pane next to your session.

* Just want a look? `agentpane --demo` needs no install and no Claude.
* Something wrong? `agentpane doctor` says what.
* Prefer Go? `go install github.com/simoncoombes/agentpane/cmd/agentpane@latest`
* Using Codex? `agentpane --source codex`

## The marks

`⚑` blocked on you · `●` running · `○` queued · `◇` teammate, reports almost
nothing · `▪▪▫·` how far it got · the number on the right is tokens

`⏎` inspects a row, `⇥` jumps to the session pane to answer whoever is blocked,
`a` shows the agents that finished, `?` lists the rest.

Nothing on a row is a guess. The two marks that are inferred say so, and an
agent that reports nothing gets a dim row rather than an invention.

## Docs

* [READING-THE-PANE.md](docs/READING-THE-PANE.md) — every glyph, every
  animation, and what the pane won't tell you
* [CLI.md](docs/CLI.md) — every flag, key and config setting
* [INSTALL.md](docs/INSTALL.md) — the installer, the hook snippet, auto-open
* [ITERM2.md](docs/ITERM2.md) — iTerm2 profile and split
* [CODEX.md](docs/CODEX.md) — watching a Codex run
* [DATA-SOURCES.md](docs/DATA-SOURCES.md) — what it reads, and how fast
* [CONTRIBUTING.md](CONTRIBUTING.md) · [SECURITY.md](SECURITY.md)

MIT licensed. See [LICENSE](LICENSE).
