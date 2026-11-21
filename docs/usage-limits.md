# Usage left per agent

The chat pages show, in a slim strip above the message box (and on the new-chat
screen), how much of each agent's usage limits is left: for each window (5-hour,
7-day, 30-day) the percent left and a thin bar, green while there is plenty,
amber under 50% and red under 20%. Hover or tap an agent for the details: the
percent used, when each window resets, the plan if the CLI says, and when the
reading was taken. A reading older than 10 minutes is dimmed and says how old it
is. Antigravity keeps one set of limits per group of models, so it shows one
item per group ("Gemini", "Claude/GPT"), each with its 5-hour and 7-day bars;
its details also name the group and the models in it. The page reads `GET /api/usage` every minute while a chat page is open and
right after a turn ends.

## Where the numbers come from

Unire shows only what each CLI reports; it never estimates.

| Agent | What it reports | When Unire reads it |
| --- | --- | --- |
| Claude Code | `rate_limit_event` in its stream-json: `rate_limit_info.unifiedWindows.five_hour` and `.seven_day`, each with `utilization` (0 to 1) and `resetsAt` (Unix seconds) | During each turn. Claude has no way to ask without sending a prompt, so there is no reading until the first Claude turn |
| Codex | app-server `account/rateLimits/read` and the `account/rateLimits/updated` notification: `primary` / `secondary` windows with `usedPercent` (0 to 100), `windowDurationMins` and `resetsAt`, and `planType` | During each turn, and outside chats: when the reading is over 5 minutes old and a page asks for it, Unire runs `codex app-server` with just those two requests in a throwaway sandbox (the agent login volume and the network, no repos, no tokens). It uses none of the limits and takes about a second |
| Antigravity CLI | `agy --print=/usage --output-format=json`: `command.data.groups[].buckets[]`, each with `window` (`5h` or `weekly`), `remaining_fraction` (0 to 1, what is left) and `reset_time` (RFC 3339), and the group's name and models | Only outside chats (agy refuses `/usage` in stream-json mode, so no turn carries it): when the reading is over 5 minutes old and a page asks for it, Unire runs that command in a throwaway sandbox (the agent login volume and the network, no repos, no tokens). agy answers it itself: no model call, no tokens, about a second |

The Antigravity reading comes from `agy --print=/usage`, a CLI command that is
not a documented API, so its format may change. Unire reads the structured JSON
(not the text table) and checks its shape; if it is not what Unire expects, it
shows "no reading" with a note in the details rather than a wrong number. The
used percent is 100 minus `remaining_fraction`.

Window names come from the window length: 300 minutes is "5-hour", 10080 is
"7-day", 43200 is "30-day".

The background Codex and Antigravity check never makes a page wait: the request returns what is
saved and the new reading shows on the next poll. It is not run while a chat with that agent is queued or running (both would use
the same login), nor when the agent is known to be signed out, and after any try the next waits at least a minute, so
polling pages do not start a sandbox on every request. Why the last try failed
shows in the details.

The latest reading per agent is kept in the database (`agent_usage`), so it
survives a restart.

## API

`GET /api/usage` returns one entry per agent:

```json
{"agentId": "codex", "label": "Codex", "reported": true, "status": "allowed", "plan": "free",
 "windows": [{"name": "30-day", "durationMins": 43200, "usedPercent": 13, "resetsAt": 1797607630}],
 "updatedAt": 1795215254, "stale": false}
```

For Antigravity each window also has `group` and `models`:
`{"name": "5-hour", "durationMins": 300, "usedPercent": 22, "resetsAt": 1795225834, "group": "Gemini Models", "models": "Gemini 3.8 Flash"}`.

`reported` is false for an agent whose CLI reports nothing; `updatedAt` is 0
when there is no reading yet; `note` says why the last background try failed.
`GET /api/agents` includes the same entry as `usage`.

## Adding it to an agent

Parse the CLI's own report into an `agent.AgentUsage` and set it on the
`Event` the adapter returns (`Usage`); the workflow saves it. If the CLI can be
asked without using any of its limits, set `UsageArgv` and `ParseUsage` on its
`Info` (see `codexUsageScript` in `internal/agent/usage.go`).
