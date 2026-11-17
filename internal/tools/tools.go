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
	Usage: `unire name "<title>" - give this conversation a short title (under 60 characters) that says what the task is. Run it once, early.`,
}

// ForNewConversation is the tool set for the first message of a conversation.
// Other situations get their own sets (a reply keeps its conversation's
// name, so it gets none of these).
func ForNewConversation() []Tool { return []Tool{Name, Ask, PR, PRBody} }

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
	return b.String()
}

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

// Ask lets the agent put a question to the owner and wait for the answer.
var Ask = Tool{
	Name: "ask",
	Usage: `unire ask "<question>" - ask the owner a question and wait for the answer, which it prints. ` +
		`Use it only when you cannot sensibly go on without it: a decision only the owner can make, or something you cannot find out yourself. ` +
		`If it says the owner has not answered yet, run the "unire wait <number>" command it gives to keep waiting, or go on with your best judgment and say what you assumed. ` +
		`Ask rarely, and never ask for passwords or tokens.`,
}

// ForReply is the tool set for a reply: the conversation already has its name.
func ForReply() []Tool { return []Tool{Ask, PR, PRBody} }

// PR lets the agent suggest a title for a pull request. It cannot open one: when
// it has pushed changes, Unire asks the owner whether to open a pull request, and
// uses this title if the agent gave one.
var PR = Tool{
	Name: "pr",
	Usage: `unire pr "<title>" - suggest a title (under 70 characters) for a pull request with your changes. ` +
		`Run it once, when you have finished. You cannot open the pull request yourself: Unire asks the owner whether to.`,
}

// The tools for a conversation with an open pull request. Each prints an answer.
var (
	PRStatus = Tool{
		Name:  "pr-status",
		Usage: `unire pr-status - show this conversation's pull request and the state of its checks (builds and tests). Use it to see whether your changes pass.`,
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
func CallTools() []Tool { return []Tool{PRStatus, PRComments, PRReply} }

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
func ForReplyWithPR() []Tool { return append([]Tool{Ask}, CallTools()...) }

// PRBody lets the agent write the description of a pull request. It is piped in,
// because it has several lines.
var PRBody = Tool{
	Name: "pr-body",
	Usage: "unire pr-body <<'EOF' ... EOF - write the pull request description (markdown, a few short paragraphs or bullets): what changed and why, and how it was checked. " +
		"Write it for the reviewer, as the change itself: do not mention Unire, this conversation or questions you asked the owner. Run it once, when you have finished.",
}
