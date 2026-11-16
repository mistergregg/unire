# Tools an agent can use

A tool is something an agent can ask Unire to do during a session. Inside the
sandbox the agent runs a shell command:

```
unire name "Fix the login redirect"
```

The `unire` command (in the sandbox image, `docker/sandbox/unire`) only writes
the request to a file under `/workspace/.unire-tools/`. When the run ends,
Unire reads the file and decides what to do. The agent never talks to Unire
directly, so a request is a suggestion: Unire checks it, and ignores a tool the
session was not given. It works the same for every agent, because every agent
can run a shell command.

Which tools a session gets depends on the situation (`internal/tools`):

| Situation | Tools |
|---|---|
| First message of a conversation | `name`, `ask` |
| A reply in a conversation | `ask` (the conversation keeps its name) |

## `name`

Gives the conversation a short title. Only the title changes; the branch was
named when the session started and keeps its name. The last request wins and is
cleaned up (one line, at most 80 characters) before it is shown.

## Adding a tool

1. Add a `Tool` in `internal/tools/tools.go` (its name, and the line that tells
   the agent how and when to use it) and add it to the set for the situations
   that should have it.
2. Read its request file after the run in `internal/workflow/run.go` and return
   the result.
3. Apply it in `internal/sessions/manager.go`.

Later tools could share a preview link or add a repo.

## `ask`

Lets the agent put a question to you and wait for the answer, for a decision
only you can make.

```
unire ask "Which database should I use?"
```

The question appears in the conversation as a card with an answer box, and the
agent carries on once you answer. Agents stop a command that runs too long, so
`unire ask` waits in rounds of 90 seconds; if you have not answered, it tells
the agent to run `unire wait <number>` to keep waiting, or to go on with its
best judgment. After 30 minutes without an answer Unire tells the agent to go on
without one. The agent sees your answer, so do not paste passwords or tokens
into it.
