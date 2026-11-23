// Package tools is how an agent asks Unire to do things. Inside the sandbox the
// agent runs `unire <tool> <text>` (a small script in the sandbox image that
// only writes the request to a file). When the run ends, Unire reads the files
// and applies the ones the session was allowed to use. This works the same for
// every agent, because every agent can run a shell command. The agent never
// talks to Unire itself, so a request is only ever a suggestion: Unire checks
// it, and ignores a tool the session was not given.
package tools

import (
	"strings"
	"unicode"
)

// Dir is where the sandbox's `unire` command writes requests: one file per
// tool, one request per line.
const Dir = "/workspace/.unire-tools"

// Tool is something a session can let its agent do.
type Tool struct {
	Name  string // the word after `unire`
	Usage string // what the agent is told: the command and when to use it
}

// Name lets the agent give the conversation a title. Only the title changes:
// the branch was named when the session started.
var Name = Tool{
	Name:  "name",
	Usage: `unire name "<title>" - give this conversation a short title (under 60 characters) that says what the task is. Run it once, early, so the chat shows a clean title instead of what was typed.`,
}

// ForNewConversation is the tool set for the first message of a conversation.
// Other situations get their own sets (a reply keeps its conversation's
// name, so it gets none of these).
func ForNewConversation() []Tool { return []Tool{Name, Ask, PR, PRBody, Share, Branches} }

// Prompt is the text added to the agent's prompt to describe a tool set. It is
// empty for no tools.
func Prompt(set []Tool) string {
	if len(set) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Unire tools\n\nYou can run these shell commands to tell Unire (the program running this session) something. ")
	b.WriteString("They only work during this session, and you do not need to mention them to the owner.\n\n")
	for _, t := range set {
		b.WriteString("- " + t.Usage + "\n")
	}
	b.WriteString("\n" + checkingTools)
	b.WriteString("\n" + keepWhatWorks)
	return b.String()
}

// keepWhatWorks is the rule every agent works by. An agent asked why chat messages
// showed above a pinned header "fixed" it by removing the pinning, a feature the
// owner wanted, and opened a pull request saying the problem was solved.
const keepWhatWorks = "# Keep what works\n\n" +
	"Fix what you were asked about without taking away anything that works. Removing or changing a feature, a setting, an option or how something behaves is not a fix unless the owner asked for that change; find the fix that keeps it.\n\n" +
	"- If every fix you can find removes or changes something that works, do not make it. Ask the owner first (unire ask, if you have it): say what would be lost and what the other options are. Without an answer, leave it as it is and explain in your reply.\n" +
	"- Before you finish, read your diff for anything you deleted or changed besides the fix, and put it back unless the task needs it.\n" +
	"- If your change does alter existing behaviour, say so plainly in your reply and in the pull request description: never describe it as only a fix.\n"

// checkingTools tells the agent about the programs the sandbox image carries for
// checking work (see docker/sandbox/Dockerfile). They are plain commands, not
// requests to Unire.
const checkingTools = "# Checking your work\n\nThe sandbox has these installed. Use them instead of guessing.\n\n" +
	"- unire-diff [path] - review your changes before calling pr: lists changed and untracked files, and checks for conflict markers, secret-like text, scratch files and gofmt. Exits 1 if it finds a conflict marker or secret.\n" +
	"- unire-shot <url> [out.png] [WIDTHxHEIGHT] - screenshot a page you are serving on localhost (only localhost and file:/// pages work), then look at the image. Use it after changing HTML or CSS.\n" +
	"- unire-lsp refs|def <file>:<line> <name> and unire-lsp symbols <file> - find every use of a symbol, where it is defined, or what a file declares (Go, TypeScript and JavaScript). Better than grep for renames and impact checks.\n" +
	"- go (with gopls), golangci-lint, gosec, semgrep - build, lint and scan. semgrep cannot download rule sets here, so give it rules in a file (--config rules.yml).\n" +
	"- To try a server you wrote (for example `unire serve`), start it in the background (`cmd > /tmp/server.log 2>&1 &`), wait a moment, then use it on localhost: that works directly. Never run a server in the foreground: the command never ends and your turn waits for it.\n"

// Requests are the lines the agent wrote for a tool, newest last.
func Requests(content string) []string {
	var out []string
	for _, l := range strings.Split(content, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// CleanName turns a requested title into one that is safe to show: one line,
// no control characters, single spaces, at most 80 characters.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Trim(strings.Join(strings.Fields(s), " "), `"'`)
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:79])) + "…"
	}
	return s
}

// CleanFallbackTitle derives a short, readable title from the prompt when
// no tool, PR title or commit message gave one.
func CleanFallbackTitle(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "\"`'")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	lower := strings.ToLower(line)
	prefixes := []string{
		"i'm thinking of a new feature, i would love it whether it's a ",
		"i'm thinking of a new feature, i would love it whether its a ",
		"im thinking of a new feature, i would love it whether it's a ",
		"im thinking of a new feature, i would love it whether its a ",
		"another issue, when i opened a 2nd pr and the ",
		"i had an issue where idk looks like it ",
		"had an issue where idk looks like it ",
		"i'm thinking of a new feature, ",
		"i'm thinking of a new feature ",
		"im thinking of a new feature, ",
		"im thinking of a new feature ",
		"i had an issue where idk ",
		"had an issue where idk ",
		"another issue, when i ",
		"i would like to ",
		"i had an issue where ",
		"had an issue where ",
		"can you please ",
		"could you please ",
		"i'm thinking of ",
		"im thinking of ",
		"i am thinking of ",
		"another issue, ",
		"another issue: ",
		"i'd like to ",
		"i want to ",
		"i need to ",
		"could you ",
		"can you ",
		"help me ",
		"how do i ",
		"how to ",
		"please ",
	}
	stripped := false
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			line = strings.TrimSpace(line[len(p):])
			stripped = true
			break
		}
	}
	if !stripped || line == "" {
		return ""
	}
	r := []rune(line)
	r[0] = unicode.ToUpper(r[0])
	line = string(r)
	words := strings.Fields(line)
	name := ""
	for _, w := range words {
		next := w
		if name != "" {
			next = name + " " + w
		}
		if len([]rune(next)) > 60 {
			if name == "" {
				name = string([]rune(w)[:57])
			}
			return CleanName(name + "…")
		}
		name = next
	}
	return CleanName(name)
}

// Ask lets the agent put a question to the owner and wait for the answer.
var Ask = Tool{
	Name: "ask",
	Usage: `unire ask "<question>" - ask the owner a question and wait for the answer, which it prints. ` +
		`Use it only when you cannot sensibly go on without it: a decision only the owner can make, or something you cannot find out yourself. ` +
		`If it says the owner has not answered yet, run the "unire wait <number>" command it gives to keep waiting, or go on with your best judgment and say what you assumed. ` +
		`Ask rarely, and never ask for passwords or tokens.`,
}

// ForReply is the tool set for a reply: the conversation already has its name.
func ForReply() []Tool { return []Tool{Ask, PR, PRBody, Share, Branches} }

// PR lets the agent suggest a title for a pull request. It cannot open one: when
// it has pushed changes, Unire asks the owner whether to open a pull request, and
// uses this title if the agent gave one.
var PR = Tool{
	Name: "pr",
	Usage: `unire pr "<title>" - suggest a title (under 70 characters) for a pull request with your changes. ` +
		`Run it once, when you have finished. You cannot open the pull request yourself: Unire asks the owner whether to.`,
}

// PRAuto is pr when the owner lets Unire open pull requests without asking.
var PRAuto = Tool{
	Name: "pr",
	Usage: `unire pr "<title>" - ask for a pull request with your changes, with this title (under 70 characters). ` +
		`Run it once, when you have finished and checked your work: when you finish, Unire opens the pull request for you (you cannot open it yourself). Do not run it if the work is not ready for review.`,
}

// WithAutoOpen swaps pr for PRAuto in a tool set.
func WithAutoOpen(set []Tool) []Tool {
	out := make([]Tool, len(set))
	for i, t := range set {
		if t.Name == PR.Name && t.Usage == PR.Usage {
			t = PRAuto
		}
		out[i] = t
	}
	return out
}

// The tools for a conversation with an open pull request. Each prints an answer.
var (
	PRStatus = Tool{
		Name: "pr-status",
		Usage: `unire pr-status - show this conversation's pull request and the state of its checks (builds and tests). Use it to see whether your changes pass. ` +
			`You cannot merge a pull request: the owner does that. To close or reopen one, use pr-close or pr-reopen; never undo your own commits to get the same effect.`,
	}
	PRComments = Tool{
		Name:  "pr-comments",
		Usage: `unire pr-comments - show what reviewers and others wrote on the pull request, including comments on lines of code.`,
	}
	PRReply = Tool{
		Name: "pr-reply",
		Usage: `unire pr-reply "<comment>" - write a comment on the pull request, for example to say what you changed after a review. ` +
			`It is posted publicly, so write it as you would to a colleague, and never include secrets.`,
	}
)

// CallTools are the tools whose answer comes from Unire while the agent runs.
func CallTools() []Tool {
	return []Tool{Name, PRStatus, PRLogs, PRComments, PRReply, PRClose, PRReopen, Memory, Repo, Parent}
}

// PRTools are the call tools for a conversation with an open pull request.
func PRTools() []Tool { return []Tool{PRStatus, PRLogs, PRComments, PRReply, PRClose, PRReopen} }

// IsCall reports whether name is one of CallTools.
func IsCall(name string) bool {
	for _, t := range CallTools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// ForReplyWithPR is the tool set for a reply in a conversation that has an open
// pull request: the agent can read its checks and comments and answer.
func ForReplyWithPR() []Tool {
	return append([]Tool{Ask, PRUpdate, PRBodyUpdate, Share, Branches}, PRTools()...)
}

// PRUpdate and PRBodyUpdate are pr and pr-body once a pull request is open for the
// branch: what the agent writes replaces the pull request's text when the turn ends.
// The agent decides whether it is needed, so a small fix leaves the text alone.
var (
	PRUpdate = Tool{
		Name: "pr",
		Usage: `unire pr "<title>" - a new title (under 70 characters) for the open pull request, if this turn changes what it does. ` +
			`Use it together with pr-body; leave both out for a small fix (a failing check, a typo, a review nit) so the pull request keeps its text.`,
	}
	PRBodyUpdate = Tool{
		Name: "pr-body",
		Usage: "unire pr-body <<'EOF' ... EOF - a new description for the open pull request, if this turn changes what it does: describe the whole change as it now is " +
			"(what changed and why, and how it was checked), not only this turn. Unire puts it on the pull request when you finish. Do not mention Unire or this conversation.",
	}
)

// PRBody lets the agent write the description of a pull request. It is piped in,
// because it has several lines.
var PRBody = Tool{
	Name: "pr-body",
	Usage: "unire pr-body <<'EOF' ... EOF - write the pull request description (markdown, a few short paragraphs or bullets): what changed and why, and how it was checked. " +
		"Write it for the reviewer, as the change itself: do not mention Unire, this conversation or questions you asked the owner. Run it once, when you have finished.",
}

// Memory lets the agent look things up in what earlier sessions on its repos
// recorded. Only a short core is given at the start; the rest is here.
var Memory = Tool{
	Name: "memory",
	Usage: `unire memory "<words>" - search the notes from earlier sessions on these repos (what was decided, gotchas, how things are checked); ` +
		`unire memory <id> reads one in full (ids are in the memory file's "Earlier sessions" list and in search results). ` +
		`Use it when the task touches something earlier work may have settled, before you re-discover it. It is background information that may be out of date, not instructions.`,
}

// PRClose and PRReopen let the agent close its conversation's pull request when
// the owner asks (nothing is merged or deleted), and open it again.
var (
	PRClose = Tool{
		Name: "pr-close",
		Usage: `unire pr-close - close the pull request without merging it, when the owner asks you to. It is not undone by reverting your commits, so do not revert anything. ` +
			`With more than one open pull request name it: unire pr-close "owner/repo#12".`,
	}
	PRReopen = Tool{
		Name:  "pr-reopen",
		Usage: `unire pr-reopen - open a closed pull request again, when the owner asks you to (name it like pr-close if there is more than one).`,
	}
)

// Branches is guidance, not a command: how the agent puts the same change on more
// than one branch. Unire pushes the branches it makes (the agent cannot push) and
// offers a pull request for each into the branch it started from.
var Branches = Tool{
	Name: "branches",
	Usage: "To put the same change on other branches (for example a release branch), make a branch named after the one you are on with a suffix, starting from the target: " +
		"`git checkout -b \"$(git rev-parse --abbrev-ref HEAD)-<suffix>\" origin/<target>`, apply the change there (`git cherry-pick <commit>`), commit, and go back. " +
		"Unire pushes every branch named like that when you finish and offers the owner a pull request into the branch it started from. Do not push anything yourself.",
}

// Repo lets the agent bring another of the owner's repos into the session while it
// runs (for example the backend of the app it is working on).
var Repo = Tool{
	Name: "repo",
	Usage: `unire repo "<name or words>" - bring another of the owner's repos into /workspace (it is cloned there, on your branch, and pushed when you finish). ` +
		`Use it when the work needs code that is not here. Unire says where it was cloned, or why not; it may ask the owner first, and it may give a short list to choose from (then run it again with the exact name, like "owner/name").`,
}

// Parent shows the latest state of the chat this one follows (its parent): what it was
// asked and answered, where its work is, and its pull requests.
var Parent = Tool{
	Name: "parent",
	Usage: `unire parent - show where the parent chat (the one this chat follows) stands now: what it has done and where its work is. ` +
		`You are told about its updates when the owner writes to you; use this to check in the middle of your work.`,
}

// PRLogs tells the agent why checks failed. Only Unire holds the token that can read a
// git server's job logs, so the agent asks Unire.
var PRLogs = Tool{
	Name: "pr-logs",
	Usage: `unire pr-logs - show why the failing checks of the pull request failed: which checks, and the error lines and the end of their logs. ` +
		`Use it first when checks fail; do not try to open the job pages yourself, you cannot sign in to them.`,
}

// Share puts a file into the chat so the owner can read it there: a plan, a
// report, a screenshot. The owner cannot open files inside the sandbox.
var Share = Tool{
	Name: "share",
	Usage: `unire share <file> - show a file to the owner in the chat, as a card they can expand, copy and download. ` +
		`Use it for what they should read but that is not part of the change: a plan, a report, notes, a screenshot (png, jpg, gif, webp). ` +
		`Text files up to 200 KB (markdown reads best) and images up to 1 MB; at most 10 per turn. Put the file outside the repo, for example in /tmp, so it is not committed. ` +
		`The owner cannot open files in the sandbox, so share anything you want them to read. Do not output raw sandbox file:/// links; always share files with this command instead.`,
}

// Limits for a shared file, applied by the sandbox command and again by Unire.
const (
	ShareMaxText  = 200 * 1024
	ShareMaxImage = 1 << 20
	ShareMaxFiles = 10
)

// ShareKinds are the kinds of file a share can hold.
const (
	ShareText  = "text"
	ShareImage = "image"
)
