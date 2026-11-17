# Steering: talking to an agent while it works

While a chat is running you can keep typing. The message box stays open with
three actions:

- **Send** (Enter): the agent reads your message at its next step.
- **Send now** (Ctrl+Enter, or the button): stop what it is doing and carry on
  from your message.
- **Stop**: end the run.

Your messages appear in the conversation with what became of them: waiting for
the agent, the agent has it, or not delivered.

## How each agent does it

An agent that can be steered runs as a process that stays open and reads your
messages on its input (`agent.Live`, `internal/agent/live.go`). Each agent has
its own protocol, so each has a small driver.

| Agent | Send | Send now |
|---|---|---|
| **Claude Code** | `claude -p --input-format stream-json`: the message goes in with the default priority and is read as soon as the tool calls it is running finish. | The message goes in with `priority: "now"` and a human origin: work that can run in the background moves there and Claude reads it in the same turn; otherwise Claude interrupts the turn. Its lifecycle events (`queued`, `started`) say when it took each message. |
| **Codex** | `codex app-server` (JSON-RPC over stdin): `turn/steer` adds the message to the running turn. | `turn/interrupt`, then a new turn starts from the message. |
| **Antigravity** | `agy --input-format stream-json`: its documentation says to wait for a turn's result before the next message, so the message waits and goes in after the current turn. | It documents no way to interrupt a turn, so the process is stopped (SIGINT) and started again from the saved conversation (`--conversation <id>`) with your message. |

Sources: [Claude streaming input](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode),
[Codex app server](https://developers.openai.com/codex/app-server) (the exact
messages are in the output of `codex app-server generate-json-schema`),
[Antigravity headless mode](https://antigravity.google/docs/cli/headless).

## What Unire does

- The agent's input is closed once it has answered everything it was given, so a
  run still ends by itself. A message that arrives after that is rejected with
  "send this as a reply".
- A message the agent never read (the run ended first) is marked "not delivered".
- Output that is not part of the agent's protocol is shown as it is, and an
  error from the agent (not signed in, for example) appears as a step.

## Testing

`go test ./...` uses fake agents (`internal/agent/agenttest`) that read messages
and answer them as the real ones report.

To check the drivers against the real programs, signed out, build the test
binary for Linux and run it in the sandbox image (each program must accept the
first message, report its error and leave by itself):

```
GOOS=linux go test -c -o agent.test ./internal/agent
docker run --rm -e UNIRE_REAL_CLIS=1 -v "$PWD":/t:ro unire-sandbox sh -c 'cp /t/agent.test /tmp/ && /tmp/agent.test -test.run TestRealCLIs -test.v'
```

A signed-in run is the one thing this does not cover: how each agent behaves
when a message really arrives in the middle of a long turn.
