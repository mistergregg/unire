package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// sent returns the JSON messages a driver has written so far.
func sent(buf *bytes.Buffer) []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			panic(l)
		}
		out = append(out, m)
	}
	return out
}

func liveOf(t *testing.T, name string, o LiveOptions) ([]string, LiveDriver) {
	t.Helper()
	a, err := Get(name)
	if err != nil {
		t.Fatal(err)
	}
	argv, d, err := a.(Live).StartLive(o)
	if err != nil {
		t.Fatal(err)
	}
	return argv, d
}

// liveWithModel is liveOf for an adapter set to run model (see Adapter.WithModel).
func liveWithModel(t *testing.T, name, model string, o LiveOptions) ([]string, LiveDriver) {
	t.Helper()
	a, err := Get(name)
	if err != nil {
		t.Fatal(err)
	}
	argv, d, err := a.WithModel(model).(Live).StartLive(o)
	if err != nil {
		t.Fatal(err)
	}
	return argv, d
}

func TestEveryAgentCanBeSteered(t *testing.T) {
	for _, id := range []string{"claude", "codex", "antigravity"} {
		if !CanSteer(id) {
			t.Errorf("%s should take messages while it works", id)
		}
	}
	if CanSteer("nope") {
		t.Error("an unknown agent cannot be steered")
	}
	for _, id := range []string{"claude", "codex", "antigravity"} {
		a, _ := Get(id)
		if _, _, err := a.(Live).StartLive(LiveOptions{Mode: AskFirst}); err == nil {
			t.Errorf("%s: only unattended runs can be steered", id)
		}
	}
}

// ---- Claude Code ----

func TestClaudeSteeringUsesPriorityAndTracksMessages(t *testing.T) {
	argv, d := liveOf(t, "claude", LiveOptions{Prompt: "do it", Mode: HandsOff, Ref: "ref-1"})
	want := []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--session-id", "ref-1"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v", argv)
	}
	// With a model.
	argvModel, _ := liveWithModel(t, "claude", "sonnet", LiveOptions{Prompt: "do it", Mode: HandsOff, Ref: "ref-1"})
	wantModel := []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--model", "sonnet", "--session-id", "ref-1"}
	if strings.Join(argvModel, " ") != strings.Join(wantModel, " ") {
		t.Errorf("argvModel = %v, want %v", argvModel, wantModel)
	}
	var in bytes.Buffer
	if err := d.Begin(&in, "do it"); err != nil {
		t.Fatal(err)
	}
	first := sent(&in)[0]
	if first["type"] != "user" || first["message"].(map[string]any)["content"] != "do it" || first["priority"] != nil {
		t.Errorf("the task goes in as a plain user message: %v", first)
	}
	firstID := first["uuid"].(string)

	// The agent starts working; the owner adds a message, then one that redirects it.
	d.Line(`{"type":"command_lifecycle","command_uuid":"` + firstID + `","state":"queued"}`)
	d.Line(`{"type":"command_lifecycle","command_uuid":"` + firstID + `","state":"started"}`)
	if err := d.Send(Steer{ID: "m1", Text: "also add tests"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(Steer{ID: "m2", Text: "stop, do X instead", Now: true}); err != nil {
		t.Fatal(err)
	}
	msgs := sent(&in)
	if len(msgs) != 3 || msgs[1]["priority"] != nil || msgs[2]["priority"] != "now" {
		t.Fatalf("messages = %v", msgs)
	}
	if origin, _ := msgs[2]["origin"].(map[string]any); origin["kind"] != "human" {
		t.Errorf("a message sent now comes from the human: %v", msgs[2])
	}
	if d.Done() {
		t.Error("not done: the agent has not answered anything yet")
	}

	// Its lifecycle events become message states.
	id1, id2 := msgs[1]["uuid"].(string), msgs[2]["uuid"].(string)
	ev := d.Line(`{"type":"command_lifecycle","command_uuid":"` + id1 + `","state":"queued"}`)
	if len(ev.Msgs) != 1 || ev.Msgs[0].ID != "m1" || ev.Msgs[0].State != MsgQueued {
		t.Errorf("queued = %+v", ev.Msgs)
	}
	ev = d.Line(`{"type":"command_lifecycle","command_uuid":"` + id1 + `","state":"started"}`)
	if len(ev.Msgs) != 1 || ev.Msgs[0].State != MsgDelivered {
		t.Errorf("started = %+v", ev.Msgs)
	}
	if ev := d.Line(`{"type":"command_lifecycle","command_uuid":"` + id2 + `","state":"cancelled"}`); len(ev.Msgs) != 1 || ev.Msgs[0].State != MsgCancelled {
		t.Errorf("cancelled = %+v", ev.Msgs)
	}
	if ev := d.Line(`{"type":"command_lifecycle","command_uuid":"someone-else","state":"started"}`); len(ev.Msgs) != 0 {
		t.Error("a message that is not ours is ignored")
	}

	// A turn ends. Everything has been taken or dropped, so it is done.
	ev = d.Line(`{"type":"result","subtype":"success","result":"All done."}`)
	if ev.Answer != "All done." {
		t.Errorf("answer = %q", ev.Answer)
	}
	if !d.Done() {
		t.Error("everything was taken and a turn ended: done")
	}
}

func TestClaudeIsNotDoneWhileAMessageIsStillQueued(t *testing.T) {
	_, d := liveOf(t, "claude", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	id := sent(&in)[0]["uuid"].(string)
	d.Line(`{"type":"command_lifecycle","command_uuid":"` + id + `","state":"started"}`)
	d.Send(Steer{ID: "late", Text: "one more thing"})
	late := sent(&in)[1]["uuid"].(string)
	d.Line(`{"type":"command_lifecycle","command_uuid":"` + late + `","state":"queued"}`)
	d.Line(`{"type":"result","subtype":"success","result":"first"}`)
	if d.Done() {
		t.Fatal("a queued message has not been answered: not done")
	}
	// It starts a new turn, which ends.
	d.Line(`{"type":"command_lifecycle","command_uuid":"` + late + `","state":"started"}`)
	if d.Done() {
		t.Error("its turn has begun, not ended")
	}
	d.Line(`{"type":"result","subtype":"success","result":"second"}`)
	if !d.Done() {
		t.Error("done after the second turn")
	}
}

// ---- Antigravity ----

func TestAntigravityQueuesAMessageUntilTheTurnEnds(t *testing.T) {
	argv, d := liveOf(t, "antigravity", LiveOptions{Mode: HandsOff})
	want := []string{"agy", "--dangerously-skip-permissions", "--print=", "--input-format=stream-json", "--output-format=stream-json"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v", argv)
	}
	// With a model.
	argvModel, _ := liveWithModel(t, "antigravity", "gemini-3.8-flash-high", LiveOptions{Mode: HandsOff})
	wantModel := []string{"agy", "--dangerously-skip-permissions", "--print=", "--input-format=stream-json", "--output-format=stream-json", "--model", "gemini-3.8-flash-high"}
	if strings.Join(argvModel, " ") != strings.Join(wantModel, " ") {
		t.Errorf("argvModel = %v, want %v", argvModel, wantModel)
	}
	var in bytes.Buffer
	d.Begin(&in, "the task")
	if m := sent(&in); len(m) != 1 || m[0]["event"] != "user" || m[0]["message"].(map[string]any)["content"] != "the task" {
		t.Fatalf("first message = %v", m)
	}
	d.Line(`{"event":"init","conversation_id":"conv-9","init":{"cwd":"/workspace"}}`)
	if err := d.Send(Steer{ID: "m1", Text: "and this"}); err != nil {
		t.Fatal(err)
	}
	if len(sent(&in)) != 1 {
		t.Fatal("a message waits for the turn's result: the agent's docs say to wait")
	}
	if d.Done() {
		t.Error("not done: a turn is running")
	}
	// The turn ends: the waiting message goes in and is reported as taken.
	ev := d.Line(`{"event":"result","result":{"conversation_id":"conv-9","status":"SUCCESS","response":"first answer"}}`)
	if ev.Answer != "first answer" || len(ev.Msgs) != 1 || ev.Msgs[0].ID != "m1" || ev.Msgs[0].State != MsgDelivered {
		t.Errorf("result = %+v", ev)
	}
	if m := sent(&in); len(m) != 2 || m[1]["message"].(map[string]any)["content"] != "and this" {
		t.Fatalf("messages = %v", m)
	}
	if d.Done() {
		t.Error("the second turn is running")
	}
	d.Line(`{"event":"result","result":{"status":"SUCCESS","response":"second answer"}}`)
	if !d.Done() {
		t.Error("done after the last turn")
	}
	if d.Resume() != "conv-9" {
		t.Errorf("conversation = %q", d.Resume())
	}
}

func TestAntigravitySendNowHoldsEveryWaitingMessageForARestart(t *testing.T) {
	_, d := liveOf(t, "antigravity", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	d.Line(`{"event":"init","conversation_id":"conv-1"}`)
	if err := d.Send(Steer{ID: "m1", Text: "first thought"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(Steer{ID: "m2", Text: "change course", Now: true}); !errors.Is(err, ErrInterrupt) {
		t.Fatalf("Send now = %v, want ErrInterrupt", err)
	}
	if len(sent(&in)) != 1 {
		t.Error("nothing is written to a turn that is being interrupted")
	}
	// The interrupted turn ends: its result is not a cue to write the messages.
	ev := d.Line(`{"event":"result","result":{"conversation_id":"conv-1","status":"INTERRUPTED","error":"interrupted","response":""}}`)
	if len(ev.Msgs) != 0 || ev.Progress != "interrupted" {
		t.Errorf("interrupted result = %+v (an interruption on purpose is a step, not an error)", ev)
	}
	if len(sent(&in)) != 1 {
		t.Error("the messages wait for the process to be started again")
	}
	if d.Done() {
		t.Error("messages are waiting: not done")
	}
	pending := d.Pending()
	if len(pending) != 2 || JoinSteers(pending) != "first thought\n\nchange course" {
		t.Errorf("pending = %+v, want both messages, in order, to be sent together", pending)
	}
	if d.Resume() != "conv-1" {
		t.Errorf("conversation = %q", d.Resume())
	}
	// The new process carries on the conversation.
	argv, _ := liveOf(t, "antigravity", LiveOptions{Mode: HandsOff, Resume: d.Resume()})
	if !strings.Contains(strings.Join(argv, " "), "--conversation conv-1") {
		t.Errorf("restart argv = %v", argv)
	}
}

func TestAntigravityPromoteMakesAWaitingMessageUrgent(t *testing.T) {
	_, d := liveOf(t, "antigravity", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	d.Send(Steer{ID: "m1", Text: "wait for me"})
	if err := d.Promote("nope"); err != nil {
		t.Errorf("an unknown message is ignored: %v", err)
	}
	if err := d.Promote("m1"); !errors.Is(err, ErrInterrupt) {
		t.Fatalf("Promote = %v, want ErrInterrupt", err)
	}
	if p := d.Pending(); len(p) != 1 || p[0].ID != "m1" {
		t.Errorf("pending = %+v", p)
	}
}

func TestAntigravityAMessageWhileIdleGoesStraightIn(t *testing.T) {
	_, d := liveOf(t, "antigravity", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	d.Line(`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`)
	d.Send(Steer{ID: "m", Text: "one more"})
	if m := sent(&in); len(m) != 2 {
		t.Errorf("an idle agent takes it at once: %v", m)
	}
}

// ---- Codex ----

// codexScript plays the server's side of a Codex conversation.
type codexScript struct {
	t  *testing.T
	d  LiveDriver
	in bytes.Buffer
	n  int // messages already looked at
}

// next returns the messages written since the last call.
func (s *codexScript) next() []map[string]any {
	all := sent(&s.in)
	out := all[s.n:]
	s.n = len(all)
	return out
}

func (s *codexScript) feed(line string) Event { return s.d.Line(line) }

func startCodex(t *testing.T) *codexScript {
	_, d := liveOf(t, "codex", LiveOptions{Mode: HandsOff, Dir: "/workspace"})
	s := &codexScript{t: t, d: d}
	if err := d.Begin(&s.in, "the task"); err != nil {
		t.Fatal(err)
	}
	m := s.next()
	if len(m) != 1 || m[0]["method"] != "initialize" {
		t.Fatalf("first message = %v", m)
	}
	s.feed(`{"id":1,"result":{"userAgent":"x"}}`)
	m = s.next()
	if len(m) != 2 || m[0]["method"] != "initialized" || m[1]["method"] != "thread/start" {
		t.Fatalf("after initialize = %v", m)
	}
	p := m[1]["params"].(map[string]any)
	if _, has := p["model"]; has || p["cwd"] != "/workspace" || p["approvalPolicy"] != "never" || p["sandbox"] != "danger-full-access" {
		t.Errorf("thread/start params = %v", p) // no model chosen: none is sent
	}
	s.feed(`{"id":2,"result":{"thread":{"id":"thr-1"}}}`)
	m = s.next()
	if len(m) != 1 || m[0]["method"] != "turn/start" {
		t.Fatalf("after thread/start = %v", m)
	}
	ts := m[0]["params"].(map[string]any)
	in0 := ts["input"].([]any)[0].(map[string]any)
	if _, has := ts["model"]; has || ts["threadId"] != "thr-1" || in0["type"] != "text" || in0["text"] != "the task" {
		t.Errorf("turn/start params = %v", ts)
	}
	s.feed(`{"id":3,"result":{"turn":{"id":"turn-1","status":"inProgress"}}}`)
	return s
}

func TestCodexSteersTheRunningTurn(t *testing.T) {
	s := startCodex(t)
	if err := s.d.Send(Steer{ID: "m1", Text: "also fix the typo"}); err != nil {
		t.Fatal(err)
	}
	m := s.next()
	if len(m) != 1 || m[0]["method"] != "turn/steer" {
		t.Fatalf("steer = %v", m)
	}
	p := m[0]["params"].(map[string]any)
	if p["threadId"] != "thr-1" || p["expectedTurnId"] != "turn-1" {
		t.Errorf("turn/steer params = %v", p)
	}
	id := int(m[0]["id"].(float64))
	ev := s.feed(`{"id":` + itoaForTest(id) + `,"result":{"turnId":"turn-1"}}`)
	if len(ev.Msgs) != 1 || ev.Msgs[0].ID != "m1" || ev.Msgs[0].State != MsgDelivered {
		t.Errorf("steer accepted = %+v", ev.Msgs)
	}
	// Progress and the answer.
	ev = s.feed(`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"go test ./..."}}}`)
	if ev.Progress != "command: go test ./..." {
		t.Errorf("progress = %q", ev.Progress)
	}
	s.feed(`{"method":"item/completed","params":{"item":{"type":"agentMessage","text":"All fixed."}}}`)
	ev = s.feed(`{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"completed"}}}`)
	if ev.Answer != "All fixed." {
		t.Errorf("answer = %q", ev.Answer)
	}
	if !s.d.Done() {
		t.Error("the turn is over and nothing waits: done")
	}
}

func TestCodexSendNowInterruptsThenCarriesOn(t *testing.T) {
	s := startCodex(t)
	if err := s.d.Send(Steer{ID: "now1", Text: "do X instead", Now: true}); err != nil {
		t.Fatal(err)
	}
	m := s.next()
	if len(m) != 1 || m[0]["method"] != "turn/interrupt" {
		t.Fatalf("interrupt = %v", m)
	}
	if p := m[0]["params"].(map[string]any); p["threadId"] != "thr-1" || p["turnId"] != "turn-1" {
		t.Errorf("turn/interrupt params = %v", p)
	}
	if s.d.Done() {
		t.Error("the message has not been carried out yet")
	}
	// The turn ends as interrupted: the message starts the next turn.
	ev := s.feed(`{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"interrupted"}}}`)
	if ev.Answer != "" || len(ev.Msgs) != 1 || ev.Msgs[0].ID != "now1" || ev.Msgs[0].State != MsgDelivered {
		t.Errorf("interrupted = %+v", ev)
	}
	m = s.next()
	if len(m) != 1 || m[0]["method"] != "turn/start" {
		t.Fatalf("next turn = %v", m)
	}
	if text := m[0]["params"].(map[string]any)["input"].([]any)[0].(map[string]any)["text"]; text != "do X instead" {
		t.Errorf("the new turn starts from the message: %v", text)
	}
	if s.d.Done() {
		t.Error("the new turn is running")
	}
	s.feed(`{"id":4,"result":{"turn":{"id":"turn-2"}}}`)
	s.feed(`{"method":"turn/completed","params":{"turn":{"id":"turn-2","status":"completed"}}}`)
	if !s.d.Done() {
		t.Error("done after the second turn")
	}
}

func TestCodexFailuresEndTheRunInsteadOfHanging(t *testing.T) {
	// A refused turn/start (for example not signed in): nothing will happen.
	_, d := liveOf(t, "codex", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	d.Line(`{"id":1,"result":{}}`)
	d.Line(`{"id":2,"result":{"thread":{"id":"t"}}}`)
	ev := d.Line(`{"id":3,"error":{"code":-32000,"message":"not logged in"}}`)
	if !strings.Contains(ev.Progress, "not logged in") {
		t.Errorf("progress = %q", ev.Progress)
	}
	if !d.Done() {
		t.Error("a failed start is the end: done, so input is closed and the process leaves")
	}
	// A request from the server that Unire cannot answer is refused, not left waiting.
	var in2 bytes.Buffer
	_, d2 := liveOf(t, "codex", LiveOptions{Mode: HandsOff})
	d2.Begin(&in2, "task")
	before := len(sent(&in2))
	d2.Line(`{"id":77,"method":"item/tool/requestUserInput","params":{}}`)
	m := sent(&in2)
	if len(m) != before+1 || m[before]["id"] != float64(77) || m[before]["error"] == nil {
		t.Errorf("the request should be answered with an error: %v", m)
	}
}

func itoaForTest(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestClaudePromoteInterruptsTheRunningTurn(t *testing.T) {
	_, d := liveOf(t, "claude", LiveOptions{Mode: HandsOff})
	var in bytes.Buffer
	d.Begin(&in, "task")
	d.Send(Steer{ID: "m1", Text: "later"})
	if err := d.Promote("m1"); err != nil {
		t.Fatal(err)
	}
	m := sent(&in)
	last := m[len(m)-1]
	if last["type"] != "control_request" || last["request"].(map[string]any)["subtype"] != "interrupt" {
		t.Errorf("an interrupt is asked for, and Claude then reads the waiting messages: %v", last)
	}
}

func TestCodexSendsEveryWaitingMessageTogetherAfterAnInterrupt(t *testing.T) {
	s := startCodex(t)
	s.d.Send(Steer{ID: "a", Text: "first", Now: true})
	s.d.Send(Steer{ID: "b", Text: "second", Now: true})
	s.next()
	ev := s.feed(`{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"interrupted"}}}`)
	if len(ev.Msgs) != 2 || ev.Msgs[0].ID != "a" || ev.Msgs[1].ID != "b" {
		t.Errorf("both messages are delivered together: %+v", ev.Msgs)
	}
	m := s.next()
	if len(m) != 1 || m[0]["method"] != "turn/start" {
		t.Fatalf("one new turn: %v", m)
	}
	if text := m[0]["params"].(map[string]any)["input"].([]any)[0].(map[string]any)["text"]; text != "first\n\nsecond" {
		t.Errorf("the turn carries both: %q", text)
	}
}

func TestCodexCarriesOnItsSavedThreadOrStartsAfreshWithTheFallback(t *testing.T) {
	for _, lost := range []bool{false, true} {
		_, d := liveOf(t, "codex", LiveOptions{Mode: HandsOff, Dir: "/workspace", Resume: "thr-old", Fallback: "the whole story"})
		s := &codexScript{t: t, d: d}
		if err := d.Begin(&s.in, "just the new part"); err != nil {
			t.Fatal(err)
		}
		s.next()
		s.feed(`{"id":1,"result":{}}`)
		m := s.next()
		if len(m) != 2 || m[1]["method"] != "thread/resume" {
			t.Fatalf("after initialize = %v", m)
		}
		if p := m[1]["params"].(map[string]any); p["threadId"] != "thr-old" || p["cwd"] != "/workspace" {
			t.Errorf("thread/resume params = %v", p)
		}
		want, thread := "just the new part", "thr-old"
		if lost {
			ev := s.feed(`{"id":2,"error":{"message":"no rollout found for thread id thr-old"}}`)
			if ev.Progress == "" {
				t.Error("losing the thread is reported")
			}
			m = s.next()
			if len(m) != 1 || m[0]["method"] != "thread/start" {
				t.Fatalf("a lost thread starts a new one: %v", m)
			}
			s.feed(`{"id":3,"result":{"thread":{"id":"thr-new"}}}`)
			want, thread = "the whole story", "thr-new"
		} else {
			s.feed(`{"id":2,"result":{"thread":{"id":"thr-old"}}}`)
		}
		m = s.next()
		if len(m) != 1 || m[0]["method"] != "turn/start" {
			t.Fatalf("then a turn: %v", m)
		}
		if text := m[0]["params"].(map[string]any)["input"].([]any)[0].(map[string]any)["text"]; text != want {
			t.Errorf("lost=%v: the turn is told %q, want %q", lost, text, want)
		}
		if d.Resume() != thread {
			t.Errorf("lost=%v: carries on %q next time, want %q", lost, d.Resume(), thread)
		}
	}
}

func TestClaudeResumesItsSavedConversation(t *testing.T) {
	argv, d := liveOf(t, "claude", LiveOptions{Mode: HandsOff, Ref: "new-id", Resume: "old-id"})
	if !slices.Contains(argv, "--resume") || slices.Contains(argv, "--session-id") {
		t.Errorf("argv = %v", argv)
	}
	if d.Resume() != "old-id" {
		t.Errorf("carries on %q", d.Resume())
	}
}

func TestCodexStartLiveAndResumeWithModel(t *testing.T) {
	argv, d := liveWithModel(t, "codex", "gpt-6-luna", LiveOptions{Mode: HandsOff, Dir: "/workspace"})
	if strings.Join(argv, " ") != "codex app-server" {
		t.Errorf("argv = %v; want codex app-server", argv)
	}
	s := &codexScript{t: t, d: d}
	if err := d.Begin(&s.in, "the task"); err != nil {
		t.Fatal(err)
	}
	s.next()
	s.feed(`{"id":1,"result":{"userAgent":"x"}}`)
	m := s.next()
	if len(m) != 2 || m[1]["method"] != "thread/start" {
		t.Fatalf("after initialize = %v", m)
	}
	p := m[1]["params"].(map[string]any)
	if p["model"] != "gpt-6-luna" {
		t.Errorf("thread/start model = %v; want gpt-6-luna", p["model"])
	}
	s.feed(`{"id":2,"result":{"thread":{"id":"thr-1"}}}`)
	m = s.next()
	if len(m) != 1 || m[0]["method"] != "turn/start" {
		t.Fatalf("after thread/start = %v", m)
	}
	ts := m[0]["params"].(map[string]any)
	if ts["model"] != "gpt-6-luna" {
		t.Errorf("turn/start model = %v; want gpt-6-luna", ts["model"])
	}

	// Resuming a thread with another model.
	_, d2 := liveWithModel(t, "codex", "gpt-5.6-terra", LiveOptions{Mode: HandsOff, Dir: "/workspace", Resume: "thr-1"})
	s2 := &codexScript{t: t, d: d2}
	if err := d2.Begin(&s2.in, "carry on"); err != nil {
		t.Fatal(err)
	}
	s2.next()
	s2.feed(`{"id":1,"result":{}}`)
	m2 := s2.next()
	if len(m2) != 2 || m2[1]["method"] != "thread/resume" {
		t.Fatalf("after initialize = %v", m2)
	}
	p2 := m2[1]["params"].(map[string]any)
	if p2["model"] != "gpt-5.6-terra" {
		t.Errorf("thread/resume model = %v; want gpt-5.6-terra", p2["model"])
	}
}
