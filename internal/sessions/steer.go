package sessions

import (
	"encoding/json"
	"errors"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/store"
)

// Steering: the owner says something to an agent while it works. A message is
// logged as a "steer" event ("<id>\n<now|next>\n<text>"), handed to the running
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
	m.mu.Unlock()
	for {
		select {
		case s := <-ch:
			_ = m.st.AddEvent(sessionID, "steerstate", s.ID+"\n"+agent.MsgFailed)
		default:
			return
		}
	}
}

// Steer gives a running agent a message. now=true interrupts it and carries on
// from the message; otherwise the agent reads it at its next step. It returns
// an id the message's state is reported under.
func (m *Manager) Steer(sessionID, text string, images []agent.Image, now bool) (string, error) {
	m.mu.Lock()
	ch, ok := m.live[sessionID]
	m.mu.Unlock()
	if !ok {
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
	id := newID()
	mode := "next"
	if now {
		mode = "now"
	}
	select {
	case ch <- agent.Steer{ID: id, Text: text, Images: images, Now: now}:
	default:
		return "", errors.New("too many messages are waiting for the agent")
	}
	encoded, _ := json.Marshal(images)
	_ = m.st.AddEvent(sessionID, "steer", id+"\n"+mode+"\n"+text+"\n"+string(encoded))
	_ = m.st.AddEvent(sessionID, "steerstate", id+"\n"+agent.MsgQueued)
	return id, nil
}

// Promote makes the agent take a message that is still waiting, right away: it
// is interrupted and given everything that waits, at once.
func (m *Manager) Promote(sessionID, messageID string) error {
	m.mu.Lock()
	ch, ok := m.live[sessionID]
	m.mu.Unlock()
	if !ok {
		return errors.New("the agent is not running")
	}
	select {
	case ch <- agent.Steer{ID: messageID, Promote: true, Now: true}:
	default:
		return errors.New("too many messages are waiting for the agent")
	}
	_ = m.st.AddEvent(sessionID, "steerstate", messageID+"\npromoting")
	return nil
}
