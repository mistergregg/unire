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
| First message of a conversation | `name` |
| A reply in a conversation | none (the conversation keeps its name) |

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

Later tools can ask the owner a question, share a preview link, or add a repo.
