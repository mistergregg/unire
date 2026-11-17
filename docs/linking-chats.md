# Linking chats

Two separate things, for two situations.

## New chat from this one (a handoff)

You are deep in a conversation and want to carry its work on in a fresh chat without its whole history.

**New chat from this** (in a chat's header) opens the New chat form with:

- the same repos, each starting from the branch the conversation last pushed (so the new chat has the code as it stands, not the original base);
- a note saying where it comes from.

When you start it, the new agent is told what happened: the conversation boiled down (the latest turn nearly in full, older turns shortened, see [memory.md](memory.md)), where the work is (a branch per repo), and the state of its pull requests. After that the two chats are independent. The new chat shows **started from …** with a link; nothing flows between them.

## Parent chat (live data from another chat)

A chat can follow a parent: it keeps hearing what the parent does.

- **Set it** under **Options, Parent chat** on the New chat form, or with **Set parent…** in a chat's header (**Unlink** removes it). A chat cannot be its own parent, and a loop (the parent already following this chat) is refused.
- **On start**, the child is told what the parent has done so far.
- **After that**, each message you send the child carries what the parent has finished since the child last heard: only the new turns, not the history again, plus where the parent's work and pull requests stand now. The header shows `parent … · N new` while updates are waiting, and the parent's header lists the chats that follow it.
- **While it works**, the child's agent can run `unire parent` to see the parent as it stands (everything, counted as seen), for when it needs the answer in the middle of a task.
- It is one way: the child reads the parent. The child does not change the parent, and a running agent is not interrupted when the parent changes; it hears at its next message or when it asks.

A chat that was started from another can also have a parent; the two links are independent. The links are kept on the first session of each conversation, so replies keep them.
