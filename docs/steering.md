# Steering: talking to an agent while it works

While a chat is running you can keep typing. The message box stays open with
three actions:

- **Send** (Enter): the agent reads your message at its next step.
- **Send now** (Ctrl+Enter, or the button): stop what it is doing and carry on
  from your message.
- **Stop**: end the run.

Your messages appear in the conversation with what became of them: waiting for
the agent, the agent has it, or not delivered. A message that is still waiting has
its own **Send now** button: the agent is stopped and given it, together with
anything else that is waiting, all at once as one message.

## Attaching files

Any message can carry files: the first one, a reply, or a message sent while
the agent works, for every agent. Use the paperclip, or paste files into the
message box. Pasted text longer than 4,000 characters becomes a file
(`pasted text.txt`) instead of filling the box. Any kind of file is accepted, up
to 5 MB each and 5 per message; the server checks the size and the count again
(`sessions.CheckAttachments`).

- Each file is saved under `<data dir>/attachments/<session id>/<id>.<ext>` and
  deleted with the session. An image (PNG, JPEG, GIF or WebP, judged by its
  content) gets its image extension; any other file keeps its own extension
  (lower case, letters and digits only; else `txt` for text and `bin` for the
  rest), and can never be stored under an image extension.
- The log keeps only a reference, an `attach` event (`<message>\n<file> <name>…`,
  where `<message>` is empty for the turn's prompt or the id of a message sent
  while the agent works), so `steer` events and the text shown in the chat are
  unchanged. The chat shows images as thumbnails and other files as a chip under
  the message, served from `/api/sessions/{id}/attachments/{file}`. Anything but
  an image is served only as a download (`application/octet-stream`,
  `Content-Disposition: attachment`), so an HTML or SVG file never runs in the
  page.
- Before the agent reads the message, Unire copies the files into the sandbox at
  `/workspace/.unire/attachments/<id>.<ext>` (outside every repo, so nothing is
  committed) and adds a line to the text the agent gets: "The owner attached:
  <path> (<name>), … — open them to look." Every agent can open a file, so this
  works the same way for all of them; how a message is sent to an agent (for
  Claude, a stream-json message whose content is a string) is unchanged.
- A message to a working agent has its files copied just before it is handed
  over; if that fails, the message is marked not delivered.

## How each agent does it

An agent that can be steered runs as a process that stays open and reads your
messages on its input (`agent.Live`, `internal/agent/live.go`). Each agent has
its own protocol, so each has a small driver.

| Agent | Send | Send now |
|---|---|---|
| **Claude Code** | `claude -p --input-format stream-json`: the message goes in with the default priority and is read as soon as the tool calls it is running finish. | The message goes in with `priority: "now"` and a human origin: work that can run in the background moves there and Claude reads it in the same turn; otherwise Claude interrupts the turn. Its lifecycle events (`queued`, `started`) say when it took each message. |
| **Codex** | `codex app-server` (JSON-RPC over stdin): `turn/steer` adds the message to the running turn. | `turn/interrupt`, then a new turn starts from the message. |
| **Antigravity** | `agy --input-format stream-json`: its documentation says to wait for a turn's result before the next message, so the message waits and goes in after the current turn (everything waiting goes in together). | It documents no way to interrupt a turn, and nothing about what the process does after a SIGINT, so it is not relied on: the turn is interrupted, the process is ended (it gets a few seconds to leave), and it is started again from the saved conversation (`--conversation <id>`) with all the waiting messages as the new prompt. |

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

## Ending an agent for good

`docker exec` does not return while its input is still open, even after the
process in it has ended, and it also waits for helper processes that keep the
agent's output open. Either one left the run waiting forever (this froze Send
now on Antigravity twice).

So the agent runs under a small wrapper (`agentWrapper`): a shell that records
the agent's process id, runs the agent with the input passed on, and prints
`unire-agent-exited:<code>` when it ends (it does not `exec` the agent, so it
outlives it to say so). When Unire sees that line, or has to end the agent, it
closes the agent's input first. To end an agent (after an interrupt that did not
end its turn, or when a finished agent does not leave) it signals the agent and
every process it started (the sandbox has no `ps` or `pkill`, so the tree is read
from `/proc`), kills it if it will not go, and finally stops waiting for it, so
the run always carries on. Antigravity messages sent now are kept while this
happens and go to the restarted agent together.

`internal/workflow/wrapper_docker_test.go` checks all of this in a real sandbox
(`UNIRE_DOCKER_TESTS=1`, needs the `unire-sandbox` image).
