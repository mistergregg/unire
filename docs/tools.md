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
| First message of a conversation | `name`, `ask`, `pr`, `pr-body`, `memory` (with memory on) |
| A reply in a conversation | `ask`, `pr` (the conversation keeps its name) |
| A reply, in a conversation with a pull request | `ask`, `pr-status`, `pr-comments`, `pr-reply`, `pr-close`, `pr-reopen` |

## `name`

Gives the conversation a short title. Only the title changes; the branch was
named when the session started and keeps its name. The last request wins and is
cleaned up (one line, at most 80 characters) before it is shown.

## Pull requests

Unire opens pull requests only when you say so, and follows them afterwards.

- When a turn pushes a branch, the chat asks "Open a pull request for this repo?"
  with the title (editable) and a description made from the agent's final answer.
  **Open pull request** creates it on the repo's git server (GitHub, GitLab,
  Gitea and Forgejo are supported) with the token you saved for that server;
  **Not now** hides the question in this browser (a button keeps it one click
  away). The agent never has the token.
- Each pull request is shown in the chat with its state (open, merged, closed),
  its checks and its comments, refreshed every 30 seconds while you look. A
  failing check or a comment offers a button that sends it to the agent as a
  reply; the agent pushes to the same branch, so the pull request updates.

### `pr`

`unire pr "<title>"` suggests the title Unire offers when it asks. The agent
cannot open the pull request; the last suggestion wins.

### `pr-body`

`unire pr-body <<'EOF' ... EOF` gives the pull request description (markdown,
several lines, so it is piped in). It is meant for the reviewer: what changed,
why, how it was checked. Without one, Unire uses the end of the agent's final
answer. You can edit both the title and the description before opening.

### `pr-close`, `pr-reopen`

When you ask the agent to close the pull request, it runs `unire pr-close`
(`unire pr-reopen` opens it again). Unire closes it on the git server; nothing is
merged or deleted, so the agent has no reason to revert its commits to get the same
effect. With several pull requests, it names one: `unire pr-close "owner/repo#12"`.
The agent cannot merge. You can do the same with the **Close pull request** and
**Reopen pull request** buttons on the card.

### Several pull requests, several branches

- **One branch, more than one target.** On a pull request card, **Open another
  into…** opens a pull request from the same branch into a different branch (a
  release branch, say). The first card also has an **Into branch** box.
- **The same change on other branches.** The agent makes a branch named after its own
  with a suffix, starting from the target (`git checkout -b <branch>-release
  origin/release/1.2`), applies the change (`git cherry-pick`), and commits. When it
  finishes, Unire pushes every branch named like that (the agent cannot push) and
  offers a pull request for each, into the branch it started from. Unire remembers
  that branch in the repo's git config (`branch.<name>.unire-base`), because pushing
  replaces the upstream.
- **Several repos** already work: each repo the chat pushed has its own card.

### `pr-status`, `pr-comments`, `pr-reply`

Once a conversation has an open pull request, its replies also get these tools,
which Unire answers while the agent runs (the sandbox script writes the request
as `@call <tool>` in the same folder questions use, and waits for the answer):

```
unire pr-status                      # the pull request and each check's state
unire pr-comments                    # reviews and comments, with file and line
unire pr-reply "added the test"      # a comment on the pull request
```

`pr-reply` posts publicly, signed "Posted by an agent through Unire". With more
than one open pull request start the comment with `owner/repo#12: `. Unire checks
the conversation owns the pull request, so an agent cannot touch any other.

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

### `memory`

`unire memory "<words>"` searches the notes of earlier sessions on this chat's repos; `unire memory <id>` reads one in full. Unire answers it live. See [memory.md](memory.md).
