package workflow

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/tools"
)

// The ask tool lets the agent put a question to the owner and wait for the
// answer. In the sandbox, `unire ask` writes <id>.q and waits for <id>.a. While
// the agent runs, Unire looks for new questions, hands each to Options.Ask
// (which shows it to the owner and waits), and writes the answer file.

const (
	askDir      = tools.Dir + "/ask"
	askMaxBytes = 2000
	noAnswer    = "The owner did not answer. Continue with your best judgment and say what you assumed."
)

var questionFile = regexp.MustCompile(`^([0-9]{1,30})\.q$`)

// AskFunc shows a question to the owner and waits for the answer. ok is false
// when there is none (it took too long, or the session ended).
type AskFunc func(ctx context.Context, id, question string) (answer string, ok bool)

func (w *worker) asking() bool {
	for _, t := range w.opt.Tools {
		if (t.Name == tools.Ask.Name && w.opt.Ask != nil) || (tools.IsCall(t.Name) && w.opt.Call != nil) {
			return true
		}
	}
	return false
}

// callPrefix marks a request in a question file as a live tool call (pr-status
// and the like) rather than a question for the owner: "@call <tool>\n<text>".
const callPrefix = "@call "

// questionIDs lists the questions the agent has written so far.
func (w *worker) questionIDs(ctx context.Context) []string {
	var out bytes.Buffer
	if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"ls", "-1", askDir}, Stdout: &out}); err != nil {
		return nil // the agent has not asked anything
	}
	var ids []string
	for _, name := range strings.Fields(out.String()) {
		if m := questionFile.FindStringSubmatch(name); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// writeAnswer writes the answer so that a reader never sees half of it.
func (w *worker) writeAnswer(ctx context.Context, id, answer string) error {
	path := askDir + "/" + id + ".a"
	return w.sb.Exec(ctx, runner.ExecOpts{
		Argv:  []string{"sh", "-c", `cat > "$1.tmp" && mv "$1.tmp" "$1"`, "sh", path},
		Stdin: strings.NewReader(answer),
	})
}

// serveQuestions answers the agent's questions until ctx ends, then waits for
// the ones in flight.
func (w *worker) serveQuestions(ctx context.Context) {
	poll := w.opt.AskPoll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	seen := map[string]bool{}
	for {
		for _, id := range w.questionIDs(ctx) {
			if seen[id] {
				continue
			}
			seen[id] = true
			question, err := w.readFile(ctx, askDir+"/"+id+".q", askMaxBytes)
			if err != nil {
				continue
			}
			wg.Add(1)
			go func(id, question string) {
				defer wg.Done()
				var answer string
				var ok bool
				if strings.HasPrefix(question, callPrefix) {
					// A live tool: Unire answers it itself, only if the session was given it.
					tool, args, _ := strings.Cut(strings.TrimPrefix(question, callPrefix), "\n")
					tool = strings.TrimSpace(tool)
					if w.opt.Call != nil && w.hasTool(tool) {
						answer, ok = w.opt.Call(ctx, tool, strings.TrimSpace(args)), true
					} else {
						answer, ok = "This tool is not available in this conversation.", true
					}
				} else if w.opt.Ask != nil {
					answer, ok = w.opt.Ask(ctx, id, strings.TrimSpace(question))
				}
				if ctx.Err() != nil {
					return // the agent has finished: nobody is waiting
				}
				if !ok || strings.TrimSpace(answer) == "" {
					answer = noAnswer
				}
				_ = w.writeAnswer(ctx, id, answer)
			}(id, question)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// hasTool reports whether the session was given a tool.
func (w *worker) hasTool(name string) bool {
	for _, t := range w.opt.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
