# Memory

Unire remembers what happened in earlier sessions so a new session, run by the same agent or a different one, does not start from nothing.

## What is remembered

For each repo (a repo is identified by its git server and full name):

- **A brief:** a short markdown document (the agent is asked to aim for under 2,500 characters; 6,000 is the hard limit) with only what an agent cannot see for itself: non-obvious commands and how to check a change, conventions and quirks, decisions with the reason, gotchas and open items, each fact naming the file it comes from. It is rewritten after each session, merging the old brief with what was learned.
- **Session summaries:** one per finished session: what was asked, what was done, decisions, gotchas, what is still open, and how to run and test. Searchable in the Memory tab.

## How it works

1. **At the start** of a session, Unire writes a small core into a file in `/workspace` named after the agent's own convention (`CLAUDE.md` for Claude Code): the briefs of the session's repos, and a one-line list (date, task, agent, id) of the five latest sessions touching them. It sits above the repos, not inside any of them, so it is never committed. The file is at most 7,000 characters (about 1,750 tokens); briefs are shortened if needed. Earlier sessions are not copied in.
2. **The agent works** as normal and finishes. Unire commits and pushes its changes.
3. **At the end,** Unire resumes the same agent conversation and asks it to write a summary and an updated brief for each repo, into a folder outside the repos. This costs one short extra request to the agent. It reads those files back, cleans them, removes secrets, enforces the size limits, and saves them.
4. **If the agent cannot do that** (it failed, timed out after five minutes, or wrote nothing), Unire saves a plain summary built from what it knows: the task, which repos changed and the file statistics, the session's status, and the end of the agent's output. A plain summary never replaces a brief.

Memory is on by default and can be turned off per session on the New session page.

## Handing work to another agent

On a finished session, **Continue with a new session** opens the New session page with the same repos and the base branch set to the branch that session pushed. Choose any agent: it starts from that branch's code with the project memory. Only Claude Code exists as an agent today; the memory format is plain markdown and each agent adapter says which file it reads.

## Rules

- **Memory is background data, not instructions.** The injected file says so, because it was written by earlier agent runs and can be wrong or contain text copied from a repo. The update prompt also marks the previous briefs as data.
- **Secrets are removed before saving.** Exact tokens used by the session, common token formats (GitHub, GitLab, Slack, AWS, JWTs, `sk-` keys, private keys) and values assigned to names like `password` or `api_key` are replaced with `***`. This is a safety net, not a guarantee; do not rely on it to make unsafe notes safe.
- **You are in control.** In the Memory tab you can read and edit any brief, restore the previous version (one step of undo), clear a brief, and delete individual summaries. Clearing a brief keeps the summaries, and deleting a session keeps its summary.
- **Where it lives:** in Unire's database (`unire.db`), which the backup in Settings includes.

## Limits

- **Quality depends on the agent.** The agent decides what is worth writing down. In testing, an early version of the prompt let one run leave out a fact the user had said to keep out of the repo; the prompt now says memory files are the exception, and a clean re-run recorded it. Check important facts in the Memory tab.
- **Compression is lossy.** Briefs are rewritten each time, so details can drop out. Session summaries and git history are the record.
- **No on-demand search for the agent yet.** The agent gets the brief and recent summaries up front. A tool it could call to search older summaries is planned.
- **No workspace-level memory yet,** only per repo. A session that touches several repos gets each one's brief and the shared summaries.
- **Only unattended sessions** write memory so far.

## What goes in a brief, and why

Studies of context files for coding agents (for example the ETH Zurich evaluation of `AGENTS.md`/`CLAUDE.md`) found that files written by an LLM that restate what the repo is, or list its folders, made agents slower and a little less successful, while short notes on what the agent cannot find out itself (tooling choices, quirks, non-default conventions) helped. So the update prompt asks for the gap, not the overview: no "what it is" or "structure" sections, nothing the README, `CLAUDE.md`, `AGENTS.md` or `docs/` already say (Unire tells the agent which of those exist in each repo), and one short line per fact with the file it comes from, so it can be checked later.

A session that changed nothing is not asked to update the briefs: that costs a request each time and has little to record. A plain summary of it is still kept and is searchable.

## Looking things up (`unire memory`)

The core is small on purpose. When the agent needs something an earlier session may have settled, it runs `unire memory "<words>"` in the sandbox, which searches the notes of the repos in this chat and prints a few matches with an excerpt and an id, or `unire memory <id>` to read one in full. Unire answers it while the agent runs (the same live channel as the pull request tools) and only ever gives notes from the repos of the current chat, marked as background data that may be out of date. A chat therefore pays for old notes only when it uses them.

## Switching agents, and long conversations

Every reply is a new run, but when the same agent replies again it carries on its own saved conversation (Claude Code `--resume`, Codex `thread/resume`, Antigravity `--conversation`), so it remembers everything said and done in it, not a summary. The conversations live in the agents' home folder, which every sandbox shares; Unire records which one each turn worked in. If another agent worked in between, the returning agent is told what happened since its last turn.

An agent new to the conversation, or one whose saved conversation is gone (its home folder was reset, say), gets a summary instead: the latest turn nearly in full (what was asked and the answer), and older turns boiled down to what was asked and how each ended, newest first, within a budget of about 16,000 characters, with a line saying when earlier turns were left out. Each turn names the agent that did it. A lost conversation is noticed when the agent says so, and the turn starts again with the summary. The code is on the branch, which the preamble names.
## Is it helping?

Each run records how much memory it was given and how long saving memory took, and Unire remembers the last state seen of each pull request (open, merged, closed). The Memory page's **Is it helping?** card compares runs with memory on and off over the last 90 days: failures, stops, run time, tokens given at the start, time saving, and how many pull requests were opened and merged. The groups are not matched (you choose which chats have memory off), so differences are a hint, not proof; to learn something, turn memory off for some comparable chats (Options on the New chat page). Pull request states are as last seen, which is when a chat was opened.
