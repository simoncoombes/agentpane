# Security

## What agentpane can touch

agentpane is a read-only monitor and the constraint is structural, not a
policy: there is no code path that writes to your repository, your session, or
your Claude Code settings, and none that sends a prompt or terminates an
agent. What it does touch:

- **Reads** Claude Code's transcripts under `~/.claude/projects/`, its session
  registry, and Codex's rollout files under `~/.codex/sessions/`. These carry
  your prompts, tool inputs and file paths. Nothing is copied anywhere.
- **Listens** on a per-session unix socket in the temp directory, mode 0600,
  for hook payloads forwarded by `agentpane hook`. This is its only input.
- **Writes** its own state under `~/.local/state/agentpane/` (the run store
  and UI preferences) and, only when you ask for them, the log files named by
  `--log` and by `agentpane autopane --debug-on`. Those logs contain prompts
  and commands verbatim; they are created 0600, and they are yours to delete.
- **Runs `osascript`** — only when auto-open is enabled, and only to ask
  iTerm2 to split a pane. The AppleScript embeds two values it did not
  author: the pane id from `$ITERM_SESSION_ID` and the session id from the
  hook payload. The session id is shell-quoted and then escaped for the
  AppleScript string literal it sits in; the pane id is escaped for its
  literal. A test drives a hostile id through both layers.
- **`install.sh`** edits `~/.claude/settings.json`, backs it up first, shows a
  plan, and asks before writing. It touches no key but `hooks`.

agentpane makes no network connections of any kind.

## Trust boundaries

Everything arriving from a session — prompts, tool inputs, file paths, agent
names — is untrusted text that is about to be printed to a terminal. It is
stripped of control characters before it is measured or drawn, so it cannot
inject its own escape sequences, ring the bell, or move the cursor off the
frame. A malformed or hostile payload degrades to a dimmer row; it never
crashes the pane and never becomes a fact on screen.

The hook socket is the one place another local process can reach agentpane. It
is mode 0600, so only your account can connect. A local attacker who could
connect could invent agents in your pane — they could not make agentpane write
anything, run anything, or reach the network.

## Reporting a vulnerability

Please report privately rather than opening an issue: use GitHub's [private
vulnerability reporting](https://github.com/simoncoombes/agentpane/security/advisories/new)
on this repository.

Include what you found, the version (`agentpane version`), and how to
reproduce it. Please do not include a log file without reading it first — it
will contain your own prompts and commands.

This is a personal project with no SLA. Expect an acknowledgement within a
week, and a fix or an explanation of why it is not one after that.
