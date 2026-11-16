# Memory

Unire remembers what happened in earlier sessions so a new session, run by the same agent or a different one, does not start from nothing.

## What is remembered

For each repo (a repo is identified by its git server and full name):

- **A brief:** a short markdown document, at most 6000 characters, covering what the repo is, its structure, conventions, commands, decisions and open items. It is rewritten after each session, merging the old brief with what was learned.
- **Session summaries:** one per finished session: what was asked, what was done, decisions, gotchas, what is still open, and how to run and test. Searchable in the Memory tab.

## How it works

1. **At the start** of a session, Unire writes the briefs of the session's repos, plus the three most recent summaries touching them, into a file in `/workspace` named after the agent's own convention (`CLAUDE.md` for Claude Code). It sits above the repos, not inside any of them, so it is never committed. The file is at most 16,000 characters (about 4,000 tokens): older summaries are dropped first, then briefs are shortened.
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
