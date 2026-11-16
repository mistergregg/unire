package sessions

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/workflow"
)

// How long an agent's question waits for the owner before the agent is told to
// go on with its best judgment.
var askTimeout = 30 * time.Minute

// askFunc is what the workflow calls when the agent asks something. The
// question goes into the session's log as an "ask" event ("<id>\n<question>");
// the owner's answer comes back through Answer and is logged as an "answer" event.
func (m *Manager) askFunc(sessionID string) workflow.AskFunc {
	return func(ctx context.Context, id, question string) (string, bool) {
		key := sessionID + "/" + id
		ch := make(chan string, 1)
		m.mu.Lock()
		m.asks[key] = ch
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.asks, key)
			m.mu.Unlock()
		}()
		_ = m.st.AddEvent(sessionID, "ask", id+"\n"+question)
		select {
		case answer := <-ch:
			return answer, true
		case <-time.After(askTimeout):
			_ = m.st.AddEvent(sessionID, "answer", id+"\n(no answer: the agent went on without one)")
			return "", false
		case <-ctx.Done():
			return "", false
		}
	}
}

// Answer gives the owner's answer to a question an agent is waiting on.
func (m *Manager) Answer(sessionID, questionID, text string) error {
	key := sessionID + "/" + questionID
	m.mu.Lock()
	ch, ok := m.asks[key]
	delete(m.asks, key)
	m.mu.Unlock()
	if !ok {
		return errors.New("the agent is no longer waiting for this answer")
	}
	ch <- text // buffered: never blocks
	_ = m.st.AddEvent(sessionID, "answer", questionID+"\n"+text)
	return nil
}

// Waiting is how many questions the session's agent is waiting for an answer to.
func (m *Manager) Waiting(sessionID string) int {
	prefix := sessionID + "/"
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for key := range m.asks {
		if strings.HasPrefix(key, prefix) {
			n++
		}
	}
	return n
}
