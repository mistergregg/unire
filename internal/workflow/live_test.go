package workflow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/agent/agenttest"
	"github.com/mistergregg/unire/internal/runner"
)

// states collects what became of each message, in order.
type states struct {
	mu sync.Mutex
	m  map[string][]string
}

func (s *states) add(id, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]string{}
	}
	s.m[id] = append(s.m[id], state)
}

func (s *states) of(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.m[id]...)
}

// runLiveClaude runs a session against a fake live Claude that holds its first
// turn open until it has been told waitFor things, and steers it with msg.
func runLiveClaude(t *testing.T, msg agent.Steer) (told []agenttest.User, st *states, out string) {
	t.Helper()
	var mu sync.Mutex
	first := make(chan struct{}, 1)
	sb := &fakeSandbox{}
	sb.live = func(ctx context.Context, inner []string, o runner.ExecOpts) error {
		return agenttest.ClaudeSteered(ctx, o.Stdin, o.Stdout,
			func(u agenttest.User) string { return "answer to: " + u.Text }, 2,
			func(u agenttest.User) {
				mu.Lock()
				told = append(told, u)
				n := len(told)
				mu.Unlock()
				if n == 1 {
					first <- struct{}{}
				}
			})
	}
	ctl := make(chan agent.Steer, 4)
	st = &states{}
	var buf bytes.Buffer
	o := opts(nil)
	o.Out, o.Control, o.OnMessage = &buf, ctl, st.add
	go func() {
		<-first // the agent is working: say something
		ctl <- msg
	}()
	done := make(chan error, 1)
	go func() { _, err := run(t, sb, baseSpec(), o); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not finish: the agent's input was never closed")
	}
	mu.Lock()
	defer mu.Unlock()
	return told, st, buf.String()
}

func TestAMessageSentWhileTheAgentWorksReachesIt(t *testing.T) {
	told, st, out := runLiveClaude(t, agent.Steer{ID: "s1", Text: "also add tests"})
	if len(told) != 2 || told[0].Text != "go" || told[1].Text != "also add tests" {
		t.Fatalf("the agent was told %+v", told)
	}
	if told[1].Priority != "" {
		t.Errorf("an ordinary message waits for the agent's next step, got priority %q", told[1].Priority)
	}
	if got := st.of("s1"); len(got) != 2 || got[0] != agent.MsgQueued || got[1] != agent.MsgDelivered {
		t.Errorf("the message went queued then delivered, got %v", got)
	}
	if !strings.Contains(out, "answer to: also add tests") {
		t.Errorf("the agent's answer should be in the output: %q", out)
	}
}

func TestSendNowInterruptsClaudeWithPriorityNow(t *testing.T) {
	told, _, _ := runLiveClaude(t, agent.Steer{ID: "s1", Text: "stop and do X", Now: true})
	if len(told) != 2 || told[1].Priority != "now" {
		t.Errorf("a message sent now carries priority now: %+v", told)
	}
}

func TestASteerableAgentWithNothingToSteerStillEnds(t *testing.T) {
	sb := &fakeSandbox{agentOut: "done"}
	var buf bytes.Buffer
	o := opts(nil)
	o.Out, o.Control = &buf, make(chan agent.Steer, 1)
	done := make(chan error, 1)
	go func() { _, err := run(t, sb, baseSpec(), o); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run never ended: input must be closed once the agent has answered")
	}
	if !strings.Contains(buf.String(), "done") {
		t.Errorf("output = %q", buf.String())
	}
}

// runAgySteered plays Antigravity: its first turn runs until it is interrupted, and
// then it either leaves (exitOnInterrupt) or stays alive until it is stopped, as
// nothing documents which it does. The owner sends one message and then one
// "now". Everything is checked at the end.
func runAgySteered(t *testing.T, exitOnInterrupt bool) {
	t.Helper()
	oldGrace := interruptGrace
	interruptGrace = 300 * time.Millisecond
	defer func() { interruptGrace = oldGrace }()

	var mu sync.Mutex
	var argvs, texts []string
	starts := 0
	intr := make(chan struct{}, 4)
	term := make(chan struct{})
	var termOnce sync.Once
	first := make(chan struct{}, 1)

	sb := &fakeSandbox{}
	sb.onSignal = func(sig string) {
		switch sig {
		case "INT":
			intr <- struct{}{}
		case "TERM":
			termOnce.Do(func() { close(term) })
		}
	}
	sb.live = func(ctx context.Context, inner []string, o runner.ExecOpts) error {
		mu.Lock()
		starts++
		mine := starts
		argvs = append(argvs, strings.Join(inner, " "))
		mu.Unlock()
		sc := bufio.NewScanner(o.Stdin)
		for sc.Scan() {
			var m struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			mu.Lock()
			texts = append(texts, m.Message.Content)
			mu.Unlock()
			fmt.Fprintln(o.Stdout, `{"event":"init","conversation_id":"conv-1","init":{}}`)
			if mine == 1 {
				first <- struct{}{}
				select { // the first process works until it is interrupted
				case <-intr:
					fmt.Fprintln(o.Stdout, `{"event":"result","result":{"conversation_id":"conv-1","status":"INTERRUPTED","error":"interrupted","response":""}}`)
					if exitOnInterrupt {
						return errors.New("exit status 130")
					}
					select { // alive, and silent, until it is stopped
					case <-term:
						return errors.New("terminated")
					case <-ctx.Done():
						return ctx.Err()
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			resp, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": "conv-1", "status": "SUCCESS", "response": "answer: " + m.Message.Content}})
			fmt.Fprintln(o.Stdout, string(resp))
		}
		return nil
	}

	ctl := make(chan agent.Steer, 4)
	st := &states{}
	var buf bytes.Buffer
	spec := baseSpec()
	spec.Agent = "antigravity"
	o := opts(nil)
	o.Out, o.Control, o.OnMessage = &buf, ctl, st.add
	go func() {
		<-first
		ctl <- agent.Steer{ID: "m1", Text: "first thought"}
		ctl <- agent.Steer{ID: "m2", Text: "change course", Now: true}
	}()
	done := make(chan error, 1)
	go func() { _, err := Run(context.Background(), fakeRunner{sb}, spec, o); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an interruption for a message is not a failure: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not finish: this is the freeze (the message was never delivered)")
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 {
		t.Fatalf("the agent should be started twice, got %d: %v", starts, argvs)
	}
	if strings.Contains(argvs[0], "--conversation") || !strings.Contains(argvs[1], "--conversation conv-1") {
		t.Errorf("the second start carries on the saved conversation: %v", argvs)
	}
	if len(texts) != 2 || texts[0] != "go" || texts[1] != "first thought\n\nchange course" {
		t.Errorf("the agent was told %q: both waiting messages, together, in order", texts)
	}
	for _, id := range []string{"m1", "m2"} {
		if got := st.of(id); len(got) == 0 || got[len(got)-1] != agent.MsgDelivered {
			t.Errorf("%s should end up delivered: %v", id, got)
		}
	}
	if !strings.Contains(buf.String(), "answer: first thought") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestSendNowWhenAntigravityLeavesAfterTheInterrupt(t *testing.T) { runAgySteered(t, true) }

// The freeze: the agent ended its turn on the interrupt but kept running, and the
// waiting messages were never delivered.
func TestSendNowWhenAntigravityStaysAliveAfterTheInterrupt(t *testing.T) { runAgySteered(t, false) }
