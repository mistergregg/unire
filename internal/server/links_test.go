package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func (e *env) sentToAgent() string {
	e.box.mu.Lock()
	defer e.box.mu.Unlock()
	return strings.Join(e.box.prompts, "\n=====\n")
}

func getJSON(t *testing.T, e *env, path string) map[string]any {
	t.Helper()
	code, body := e.do("GET", path, nil)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", path, code, body)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	return out
}

func TestANewChatCanStartFromTheStateOfAnotherOne(t *testing.T) {
	e, base := pickEnv(t)
	e.box.out = "I found the slow query and fixed it."
	code, a, raw := startChat(e, "why is the app slow", []map[string]any{repoRef(base, "Gbreed/echotalk-mobile")})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %s", code, raw)
	}
	aid := a["id"].(string)
	e.waitStatus(aid, "done")

	fork := getJSON(t, e, "/api/sessions/"+aid+"/fork")
	repos := fork["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("fork = %v", fork)
	}
	for _, r := range repos {
		m := r.(map[string]any)
		if m["base"] == "" || !strings.HasPrefix(m["base"].(string), "agent/claude/") {
			t.Errorf("the new chat starts from the branch that was pushed: %v", m)
		}
	}

	body := map[string]any{"agent": "claude", "prompt": "now add caching to it", "network": "full", "forkFrom": aid}
	code, raw2 := e.do("POST", "/api/sessions", body)
	var b map[string]any
	json.Unmarshal([]byte(raw2), &b)
	if code != http.StatusCreated || repoNames(b) != "Gbreed/echotalk-mobile" {
		t.Fatalf("start from = %d %s", code, raw2)
	}
	bid := b["id"].(string)
	e.waitStatus(bid, "done")
	sent := e.sentToAgent()
	for _, want := range []string{"started from another conversation", "why is the app slow", "I found the slow query", "the work is on branch agent/claude/", "now add caching to it"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the new agent was not told %q:\n%s", want, sent)
		}
	}
	links := getJSON(t, e, "/api/sessions/"+bid+"/links")
	from, _ := links["forkedFrom"].(map[string]any)
	if from == nil || from["id"] != aid {
		t.Errorf("links = %v", links)
	}
	if code, _ := e.do("POST", "/api/sessions", map[string]any{"agent": "claude", "prompt": "x", "forkFrom": "nope"}); code != http.StatusBadRequest {
		t.Errorf("a chat that does not exist = %d, want 400", code)
	}
}

func TestAChatThatFollowsAParentHearsWhatTheParentDid(t *testing.T) {
	e, base := pickEnv(t)
	e.box.out = "Parent result one."
	code, p, raw := startChat(e, "plan the backend change", []map[string]any{repoRef(base, "Gbreed/echotalk-backend")})
	if code != http.StatusCreated {
		t.Fatalf("parent = %d %s", code, raw)
	}
	pid := p["id"].(string)
	e.waitStatus(pid, "done")

	e.box.mu.Lock()
	e.box.out = "Child result."
	e.box.mu.Unlock()
	code, raw = e.do("POST", "/api/sessions", map[string]any{"agent": "claude", "prompt": "build the mobile side", "network": "full",
		"parent": pid, "repos": []map[string]any{repoRef(base, "Gbreed/echotalk-mobile")}})
	var c map[string]any
	json.Unmarshal([]byte(raw), &c)
	if code != http.StatusCreated {
		t.Fatalf("child = %d %s", code, raw)
	}
	cid := c["id"].(string)
	e.waitStatus(cid, "done")
	if sent := e.sentToAgent(); !strings.Contains(sent, "has a parent chat") || !strings.Contains(sent, "Parent result one.") || !strings.Contains(sent, "plan the backend change") {
		t.Errorf("the child is told what the parent did:\n%s", sent)
	}
	links := getJSON(t, e, "/api/sessions/"+cid+"/links")
	parent, _ := links["parent"].(map[string]any)
	if parent == nil || parent["id"] != pid || parent["updates"] != nil {
		t.Errorf("up to date: %v", links)
	}
	kids := getJSON(t, e, "/api/sessions/"+pid+"/links")["children"].([]any)
	if len(kids) != 1 || kids[0].(map[string]any)["id"] != cid {
		t.Errorf("the parent lists its child: %v", kids)
	}

	// The parent moves on.
	e.box.mu.Lock()
	e.box.out = "Parent result two, with a new decision."
	e.box.prompts = nil
	e.box.mu.Unlock()
	code, raw = e.do("POST", "/api/sessions/"+pid+"/reply", map[string]any{"prompt": "also decide the API shape"})
	var p2 map[string]any
	json.Unmarshal([]byte(raw), &p2)
	if code != http.StatusCreated {
		t.Fatalf("parent reply = %d %s", code, raw)
	}
	e.waitStatus(p2["id"].(string), "done")
	links = getJSON(t, e, "/api/sessions/"+cid+"/links")
	if parent, _ := links["parent"].(map[string]any); parent == nil || parent["updates"] != float64(1) {
		t.Errorf("the child sees one update waiting: %v", links)
	}

	// The next message to the child carries the update, once.
	e.box.mu.Lock()
	e.box.prompts = nil
	e.box.mu.Unlock()
	code, raw = e.do("POST", "/api/sessions/"+cid+"/reply", map[string]any{"prompt": "carry on"})
	var c2 map[string]any
	json.Unmarshal([]byte(raw), &c2)
	if code != http.StatusCreated {
		t.Fatalf("child reply = %d %s", code, raw)
	}
	e.waitStatus(c2["id"].(string), "done")
	sent := e.sentToAgent()
	if !strings.Contains(sent, "has moved on since you last heard") || !strings.Contains(sent, "also decide the API shape") {
		t.Errorf("the child is told of the new turn:\n%s", sent)
	}
	if strings.Contains(sent, "Parent result one.") {
		t.Errorf("only what is new is repeated, not the parent's whole history:\n%s", sent)
	}
	if parent, _ := getJSON(t, e, "/api/sessions/"+cid+"/links")["parent"].(map[string]any); parent == nil || parent["updates"] != nil {
		t.Errorf("after telling it, nothing is waiting: %v", parent)
	}
	e.box.mu.Lock()
	e.box.prompts = nil
	e.box.mu.Unlock()
	_, raw = e.do("POST", "/api/sessions/"+c2["id"].(string)+"/reply", map[string]any{"prompt": "and again"})
	var c3 map[string]any
	json.Unmarshal([]byte(raw), &c3)
	e.waitStatus(c3["id"].(string), "done")
	if strings.Contains(e.sentToAgent(), "moved on since") {
		t.Error("nothing new, so no update")
	}
}

func TestParentLinksCanBeSetAndMustNotLoop(t *testing.T) {
	e, base := pickEnv(t)
	mk := func(prompt string) string {
		code, s, raw := startChat(e, prompt, []map[string]any{repoRef(base, "Gbreed/gbr-client")})
		if code != http.StatusCreated {
			t.Fatalf("start = %d %s", code, raw)
		}
		e.waitStatus(s["id"].(string), "done")
		return s["id"].(string)
	}
	a, b := mk("first"), mk("second")
	if code, _ := e.do("POST", "/api/sessions/"+b+"/parent", map[string]any{"parent": a}); code != 200 {
		t.Fatalf("link = %d", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+a+"/parent", map[string]any{"parent": b}); code != http.StatusBadRequest {
		t.Errorf("a loop = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+a+"/parent", map[string]any{"parent": a}); code != http.StatusBadRequest {
		t.Errorf("its own parent = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+b+"/parent", map[string]any{"parent": ""}); code != 200 {
		t.Errorf("unlink = %d", code)
	}
	if links := getJSON(t, e, "/api/sessions/"+b+"/links"); links["parent"] != nil {
		t.Errorf("unlinked: %v", links)
	}
}

func TestTheParentToolGivesTheLatestAndCountsItAsSeen(t *testing.T) {
	e, base := pickEnv(t)
	e.box.out = "Parent result one."
	_, p, _ := startChat(e, "plan it", []map[string]any{repoRef(base, "Gbreed/echotalk-backend")})
	pid := p["id"].(string)
	e.waitStatus(pid, "done")
	_, raw := e.do("POST", "/api/sessions", map[string]any{"agent": "claude", "prompt": "build", "network": "full", "parent": pid, "repos": []map[string]any{repoRef(base, "Gbreed/gbr-client")}})
	var c map[string]any
	json.Unmarshal([]byte(raw), &c)
	cid := c["id"].(string)
	e.waitStatus(cid, "done")

	e.box.mu.Lock()
	e.box.out = "Parent result two."
	e.box.mu.Unlock()
	_, raw = e.do("POST", "/api/sessions/"+pid+"/reply", map[string]any{"prompt": "more"})
	var p2 map[string]any
	json.Unmarshal([]byte(raw), &p2)
	e.waitStatus(p2["id"].(string), "done")

	got := e.srv.Latest(context.Background(), cid)
	if !strings.Contains(got, "Parent result two.") || !strings.Contains(got, "plan it") {
		t.Errorf("the tool gives the parent as it stands, in full: %q", got)
	}
	if parent, _ := getJSON(t, e, "/api/sessions/"+cid+"/links")["parent"].(map[string]any); parent == nil || parent["updates"] != nil {
		t.Errorf("asking counts as seen: %v", parent)
	}
	if got := e.srv.Latest(context.Background(), pid); !strings.Contains(got, "no parent chat") {
		t.Errorf("a chat without a parent: %q", got)
	}
}

// A conversation is one branch and so one pull request: a reply answered by a different
// agent adds commits to that branch instead of starting a second branch to open a second
// pull request from.
func TestAReplyByAnotherAgentContinuesTheSameBranch(t *testing.T) {
	e, base := pickEnv(t)
	code, a, raw := startChat(e, "build it", []map[string]any{repoRef(base, "Gbreed/echotalk-mobile")})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %s", code, raw)
	}
	aid := a["id"].(string)
	e.waitStatus(aid, "done")
	code, raw = e.do("POST", "/api/sessions/"+aid+"/reply", map[string]any{"prompt": "now review it", "agent": "antigravity"})
	var b map[string]any
	json.Unmarshal([]byte(raw), &b)
	if code != http.StatusCreated {
		t.Fatalf("reply = %d %s", code, raw)
	}
	if b["agent"] != "antigravity" || b["branch"] != a["branch"] {
		t.Errorf("the second agent continues branch %v, got agent %v on branch %v", a["branch"], b["agent"], b["branch"])
	}
}
