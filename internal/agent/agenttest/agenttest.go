// Package agenttest plays agents for tests: a fake `claude -p --input-format
// stream-json` that reads messages on its input and answers them the way the
// real one reports (lifecycle events, an assistant message, a result), without
// needing a login or a network.
package agenttest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Unwrap recognises the command Unire runs a steerable agent with: the agent
// behind a small `sh -c` that records its process id. It returns the agent's own
// command, or reports that the command is a signal sent to it.
func Unwrap(argv []string) (inner []string, isAgent, isSignal bool) {
	if len(argv) >= 3 && argv[0] == "sh" && argv[1] == "-c" && strings.Contains(argv[2], "unire-agent.pid") {
		if strings.Contains(argv[2], "kill -") {
			return nil, false, true
		}
		if len(argv) >= 5 {
			return argv[4:], true, false
		}
	}
	return nil, false, false
}

// User is a message the fake agent received.
type User struct {
	Text     string
	Priority string // "", "next", "later" or "now"
}

// Claude plays a live Claude Code until its input is closed.
//
//	reply:  what to answer a message with (may be empty)
//	before: called before answering a message (a test can hold the agent here
//	        until ctx is done, to play a long-running turn); may be nil
//	onUser: called with every message received; may be nil
func Claude(ctx context.Context, in io.Reader, out io.Writer, reply func(User) string, before func(ctx context.Context, u User), onUser func(User)) error {
	emit := func(v map[string]any) {
		b, _ := json.Marshal(v)
		fmt.Fprintln(out, string(b))
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			UUID    string `json:"uuid"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Priority string `json:"priority"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "user" {
			continue
		}
		u := User{Text: m.Message.Content, Priority: m.Priority}
		if onUser != nil {
			onUser(u)
		}
		emit(map[string]any{"type": "command_lifecycle", "command_uuid": m.UUID, "state": "queued"})
		emit(map[string]any{"type": "command_lifecycle", "command_uuid": m.UUID, "state": "started"})
		if before != nil {
			before(ctx, u)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		text := ""
		if reply != nil {
			text = reply(u)
		}
		if text != "" {
			emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		}
		emit(map[string]any{"type": "result", "subtype": "success", "result": text, "user_message_uuids": []string{m.UUID}})
	}
	return nil
}

// ClaudeSteered plays a live Claude Code that reads its input while it works,
// as the real one does: a message that arrives during a turn is taken into that
// turn. The first turn is held open until waitFor messages have arrived (counting
// the first), so a test can steer it; then one result answers them all.
func ClaudeSteered(ctx context.Context, in io.Reader, out io.Writer, reply func(User) string, waitFor int, onUser func(User)) error {
	emit := func(v map[string]any) {
		b, _ := json.Marshal(v)
		fmt.Fprintln(out, string(b))
	}
	type msg struct {
		User
		uuid string
	}
	ch := make(chan msg, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m struct {
				Type    string `json:"type"`
				UUID    string `json:"uuid"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Priority string `json:"priority"`
			}
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.Type == "user" {
				ch <- msg{User{Text: m.Message.Content, Priority: m.Priority}, m.UUID}
			}
		}
	}()
	received := 0
	take := func(m msg) {
		received++
		if onUser != nil {
			onUser(m.User)
		}
		emit(map[string]any{"type": "command_lifecycle", "command_uuid": m.uuid, "state": "queued"})
	}
	for {
		var first msg
		select {
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			first = m
		case <-ctx.Done():
			return ctx.Err()
		}
		take(first)
		emit(map[string]any{"type": "command_lifecycle", "command_uuid": first.uuid, "state": "started"})
		uuids := []string{first.uuid}
		last := first.User
		for received < waitFor {
			select {
			case m, ok := <-ch:
				if !ok {
					return nil
				}
				take(m)
				emit(map[string]any{"type": "command_lifecycle", "command_uuid": m.uuid, "state": "started"})
				uuids = append(uuids, m.uuid)
				last = m.User
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		text := ""
		if reply != nil {
			text = reply(last)
		}
		if text != "" {
			emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		}
		emit(map[string]any{"type": "result", "subtype": "success", "result": text, "user_message_uuids": uuids})
	}
}
