package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/store"
)

func TestLastActiveIsTheLatestOfWhatHappenedToAConversation(t *testing.T) {
	t0 := time.Unix(1795000000, 0)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	for name, c := range map[string]struct {
		x     store.Session
		owner time.Time
		want  time.Time
	}{
		"just sent":                {store.Session{CreatedAt: at(0)}, time.Time{}, at(0)},
		"started later":            {store.Session{CreatedAt: at(0), StartedAt: at(1)}, time.Time{}, at(1)},
		"finished":                 {store.Session{CreatedAt: at(0), StartedAt: at(1), FinishedAt: at(9)}, time.Time{}, at(9)},
		"you spoke while it ran":   {store.Session{CreatedAt: at(0), StartedAt: at(1)}, at(5), at(5)},
		"finished after you spoke": {store.Session{CreatedAt: at(0), StartedAt: at(1), FinishedAt: at(9)}, at(5), at(9)},
	} {
		if got := lastActive(c.x, c.owner); got != c.want.Unix() {
			t.Errorf("%s: %d, want %d", name, got, c.want.Unix())
		}
	}
}

// The list says when each conversation was last acted on, counting a reply that was
// sent to it (a new turn) and not only when it began.
func TestTheChatListSaysWhenEachChatWasLastActedOn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	st := e.srv.store
	mk := func(id, parent string) {
		if err := st.CreateSession(store.Session{ID: id, ParentID: parent, Task: "chat " + id, Agent: "claude", Mode: "hands-off", Prompt: "p"}); err != nil {
			t.Fatal(err)
		}
		st.MarkFinished(id, store.StatusDone, "")
	}
	mk("old", "")
	time.Sleep(1100 * time.Millisecond)
	mk("newer", "")
	time.Sleep(1100 * time.Millisecond)
	mk("old-reply", "old") // a reply to the older conversation, sent last

	code, body := e.do("GET", "/api/sessions", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var list []struct {
		ID         string `json:"id"`
		LastActive int64  `json:"lastActive"`
		CreatedAt  int64  `json:"createdAt"`
	}
	json.Unmarshal([]byte(body), &list)
	got := map[string]int64{}
	for _, s := range list {
		got[s.ID] = s.LastActive
	}
	if len(list) != 2 {
		t.Fatalf("one row per conversation: %s", body)
	}
	if got["old"] <= got["newer"] {
		t.Errorf("the older conversation got a reply last, so it was acted on last: old=%d newer=%d", got["old"], got["newer"])
	}
}
