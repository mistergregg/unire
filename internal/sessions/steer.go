package sessions

import (
	"errors"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/store"
)

// Steering: the owner says something to an agent while it works. A message is
// logged as a "steer" event ("<id>\n<now|next>\n<text>"; images sent with it are
// referenced by an "attach" event, see attach.go), handed to the running
// agent through the session's channel, and what becomes of it (queued,
// delivered, failed) is logged as "steerstate" events ("<id>\n<state>").

const steerBuffer = 32

// registerLive makes a session's agent steerable, if it can be. It returns the
// channel the workflow reads messages from, or nil.
func (m *Manager) registerLive(sessionID, agentName, mode string) chan agent.Steer {
	if mode != agent.HandsOff || !agent.CanSteer(agentName) {
		return nil
	}
	ch := make(chan agent.Steer, steerBuffer)
	m.mu.Lock()
	m.live[sessionID] = ch
	m.mu.Unlock()
	return ch
}

// finishLive stops accepting messages for a session. Messages the agent never
// read are marked as not delivered.
func (m *Manager) finishLive(sessionID string, ch chan agent.Steer) {
	m.mu.Lock()
	delete(m.live, sessionID)
	var leftover []agent.Steer
	for {
		select {
		case s := <-ch:
			leftover = append(leftover, s)
		default:
			m.mu.Unlock()
			for _, s := range leftover {
				_ = m.st.AddEvent(sessionID, "steerstate", s.ID+"\n"+agent.MsgFailed)
			}
			return
		}
	}
}

// Steer gives a running agent a message. now=true interrupts it and carries on
// from the message; otherwise the agent reads it at its next step. It returns
// an id the message's state is reported under.
func (m *Manager) Steer(sessionID, text string, now bool) (string, error) {
	return m.SteerWith(sessionID, text, nil, now)
}

// SteerWith is Steer with images attached: they are copied into the sandbox before
// the agent reads the message, and the message tells it where they are.
func (m *Manager) SteerWith(sessionID, text string, files []Attachment, now bool) (string, error) {
	id := newID()
	mode := "next"
	if now {
		mode = "now"
	}
	saved, err := m.saveAttachments(sessionID, files)
	if err != nil {
		return "", err
	}
	steer := agent.Steer{ID: id, Text: text + attachNote(saved), Now: now, Files: attachFiles(saved)}

	m.mu.Lock()
	ch, ok := m.live[sessionID]
	if !ok {
		m.mu.Unlock()
		removeSaved(saved)
		x, found, _ := m.st.Session(sessionID)
		switch {
		case !found:
			return "", errors.New("no such session")
		case store.Finished(x.Status):
			return "", errors.New("the agent has finished: send this as a reply")
		default:
			return "", errors.New("this agent cannot take messages while it works: it will read your reply when it finishes")
		}
	}
	select {
	case ch <- steer:
		m.mu.Unlock()
	default:
		m.mu.Unlock()
		removeSaved(saved)
		return "", errors.New("too many messages are waiting for the agent")
	}
	if len(saved) > 0 {
		_ = m.st.AddEvent(sessionID, "attach", attachEvent(id, saved))
	}
	_ = m.st.AddEvent(sessionID, "steer", id+"\n"+mode+"\n"+text)
	_ = m.st.AddEvent(sessionID, "steerstate", id+"\n"+agent.MsgQueued)
	return id, nil
}

// Promote makes the agent take a message that is still waiting, right away: it
// is interrupted and given everything that waits, at once.
func (m *Manager) Promote(sessionID, messageID string) error {
	m.mu.Lock()
	ch, ok := m.live[sessionID]
	if !ok {
		m.mu.Unlock()
		return errors.New("the agent is not running")
	}
	select {
	case ch <- agent.Steer{ID: messageID, Promote: true, Now: true}:
		m.mu.Unlock()
	default:
		m.mu.Unlock()
		return errors.New("too many messages are waiting for the agent")
	}
	_ = m.st.AddEvent(sessionID, "steerstate", messageID+"\npromoting")
	return nil
}
