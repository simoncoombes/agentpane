# OpenAI Codex

agentpane was built against Claude Code, and Codex is the second platform it
speaks. The tree, the inspector and every key work the same; what differs is
where the data comes from and how the pane gets opened for you.

`agentpane --source codex` watches a Codex CLI run instead of a Claude Code
one.

Everything above `event.Source` — the state machine, the tree, the inspector,
the animation — never knew which platform it was drawing, so supporting a second
one is a package that speaks Codex and nothing else
(`internal/source/codexsrc`).

Codex writes one JSONL "rollout" file per thread under
`~/.codex/sessions/<yyyy>/<mm>/<dd>/`, and a subagent is a thread like any
other whose **first line names its parent**. So the tree is discoverable by
reading files - no database, no new dependency:

| Codex line | becomes |
|---|---|
| `session_meta` (`thread_source: "user"`) | the session, and `main` |
| `session_meta` (`thread_source: "subagent"`) | an agent, named for the task it was spawned for |
| `turn_context.model` | the model on the row |
| `response_item` `custom_tool_call` / `_output` | a tool call starting and ending |
| `event_msg` `task_started` / `task_complete` | an agent starting and returning |
| `token_usage_record` | tokens, `input + output + cache_write` (the same rule as everywhere else) |

Verified against a real four-thread Codex run on the author's machine: the token
totals it derives match Codex's own `tokens_used` column exactly, and the three
subagents reach the tree named for their tasks with their model and their work.

### Auto-opening the pane for Codex

Codex has the same hook system, with the same event names and payload fields, so
the same `autopane` hook works - `~/.codex/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command",
                     "command": "/path/to/agentpane autopane --source codex",
                     "timeout": 5 } ] }
    ]
  }
}
```

`autopane` already forwards its extra arguments to the pane it opens, so the
pane it spawns is `agentpane --session <id> --source codex`.

`--on-prompt` is there because **Codex has no session until you ask it
something**. Claude Code starts a session the moment it launches and fires
`SessionStart` then; an interactive Codex sitting at its prompt has no session,
writes no rollout file, and fires nothing at all (measured: `codex exec` fires
the hook, an interactive Codex idle at the prompt does not, and writes no
session file either). So on that platform the first prompt is the earliest
honest moment to open a pane, and both events are wired to the same command -
whichever comes first wins, and the per-session lock stops the second from
opening a duplicate. `--on-prompt` is off by default because on Claude Code a
prompt is not the start of anything and the pane is already open. Codex's `SessionStart` payload
carries `session_id`, `cwd`, `source: startup` and `model`, which is what
autopane's existing guard chain reads.

**Codex requires the hook to be trusted before it will run it**, and trust is
granted on first use - so the launch where you accept the prompt is itself
skipped, and the hook starts working from the next one. Until then it
is skipped silently - the hook does not fire, and nothing says so. Trust it on
your first interactive `codex` launch (or, for a one-off, run Codex with
`--dangerously-bypass-hook-trust`). `agentpane autopane --debug-on` writes a
trace to `~/.local/state/agentpane/autopane.log` saying which guard stopped a
hook, and `--debug-off` turns it back off - that is the way to tell "not
trusted" from "opened nothing for another reason".

`agentpane install` does not manage Codex's hooks; that file is written by hand.

**Not yet:** no permission requests (Codex approvals are not in the rollout), no
stall detection tuned to its timings, and the session picker lists runs rather
than attaching to one live. Codex also has a hook system whose event names and
payload fields are nearly identical to Claude Code's - a faster, more precise
source than tailing files, and the obvious next step.
