# Contributing

Bug reports and pull requests are welcome. This file is what a change is
expected to come with, and enough of the layout to find your way.

## Getting set up

```sh
git clone https://github.com/simoncoombes/agentpane
cd agentpane
go build ./cmd/agentpane
go test ./...
agentpane --demo          # the whole UI with no Claude Code session at all
```

Go 1.26 or newer. There is no code generation step, no linter beyond `go vet`
and `gofmt`, and the only dependencies are bubbletea, runewidth, a TOML parser
and `golang.org/x/sys` (Windows only: the console and process-table calls the
standard library's `syscall` package does not export).

Changing anything under `cmd/agentpane` or `internal/installer` means checking
both platforms, because several files are split by build tag:

```sh
go test ./...
GOOS=windows go vet ./...      # compiles the Windows halves and their tests
```

## The shape of the thing

Data flows one way and never turns around:

```
source ──events──▶ state.Machine ──World──▶ renderFrame ──▶ terminal
```

- **`internal/installer`** — the hook installer. On unix a bash script
  embedded with `go:embed`, so a downloaded binary can wire itself up. On
  Windows, which has neither bash nor python3, the same surgery natively:
  `hooks.go` decides what changes (pure, tested everywhere), `ojson.go` writes
  settings.json back without reordering anybody's keys, `diff.go` renders the
  preview, and `installer_windows.go` does the I/O. `TestHookPatternParity`
  and `tests/install_test.sh` are what keep the two from drifting.
- **`internal/event`** — the event taxonomy and the `Source` interface. Every
  platform enters here and nothing outside the taxonomy reaches the machine.
- **`internal/source/*`** — one package per channel: `hooksrc` (a unix socket
  the `agentpane hook` command forwards to), `transcript` (tailing Claude
  Code's JSONL), `codexsrc` (Codex rollout files), `registry`, `fake` (the
  demo script). A source's whole job is producing events; it knows nothing
  about the UI.
- **`internal/state`** — the machine. `Apply` folds one event into the world,
  `Tick` advances time, `Snapshot` hands out an immutable `World`. No I/O, no
  `time.Now()`.
- **`internal/ui`** — the render core is a pure function of
  `(World, UIState, cols, rows, now, Palette)`. No I/O, no clock reads. The
  bubbletea `Model` in `model.go` is the only part that touches either.

Two consequences worth knowing before you change anything:

- **The renderer is pure, so it is snapshot-tested.** Golden frames live in
  `internal/ui/testdata`. Run `UPDATE_SNAPSHOTS=1 go test ./internal/ui/` to
  re-record, then *read the diff* — that is the review, and an unexplained
  change to a golden frame is the change.
- **A row never claims what it did not observe.** If the data does not say an
  agent finished, the row does not say it finished. Inferred marks (`↺n`, the
  verified check) render dim and are labelled as inferences in the inspector.
  Anything that would put an invented fact on the screen is not a fix.

## What a change comes with

- `gofmt`, `go vet ./...` and `go test -race ./...` all clean. CI runs the
  same three on Linux and macOS, plus `tests/install_test.sh`.
- A test that fails without the change. For a rendering change that usually
  means a golden frame; for a source, a fixture line of the real payload.
- Comments that say *why*. The code is dense with reasoning about terminal
  behaviour and platform quirks, because those are the things a reader cannot
  re-derive. Match that.
- The installer (`internal/installer/install.sh`, embedded in the binary and
  reachable as `agentpane install` or `./install.sh`) edits a file that is not
  ours. Any change to it needs a case in `tests/install_test.sh`, which runs
  the script against a throwaway fixture `HOME` and never touches a real one.
  `get.sh` has its own harness, `tests/get_test.sh`, which serves a fake
  release over `file://`.

## Recording the pane

`tools/make-demo-project.sh` scaffolds a synthetic project — a fictional
service, sixteen files — and prints the prompt to drive a subagent fan-out
through it. It exists because a recording of a real session puts a real
repository on screen: file names in every activity line, a directory in the
session list, your own prompt as the label on main's row. Against the fixture,
every string a subagent can put on screen is one we chose.

```sh
sh tools/make-demo-project.sh        # /tmp/demo-project, then follow what it prints
```

The one thing the fixture cannot cover is the idle screen's session list,
which names the working directory of every live session: close your other
Claude Code sessions before recording.

## The `§` citations

About 650 comments cite specification sections — `§3.16`, `§13.3 Q2`. Those
point at [agentpane-design](https://github.com/simoncoombes/agentpane-design),
which holds the specs and mockups the pane was built from. You do not need it:
where the spec and the code disagree, the code is right, and the comment beside
the citation says what it did instead. Keep a citation when you move the code
it annotates; do not add new ones.

## Reporting a bug

`agentpane doctor` prints the environment check, and `--log <path>` records
every merged event. Both are useful in an issue — but read the log before you
paste it: it contains your prompts, your commands and your file paths.

Security issues go to [SECURITY.md](SECURITY.md), not the issue tracker.
