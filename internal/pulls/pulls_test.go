package pulls

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
)

type fakeGitea struct {
	mu   sync.Mutex
	sent map[string]string
}

func (f *fakeGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.sent[key] = string(b)
	f.mu.Unlock()
	pr := `{"number":4,"title":"Fix it","html_url":"http://git/pr/4","state":"open","head":{"ref":"agent/x","sha":"abc"},"base":{"ref":"main"}}`
	routes := map[string]string{
		"GET /api/v1/repos/o/r":                    `{"default_branch":"main"}`,
		"POST /api/v1/repos/o/r/pulls":             pr,
		"GET /api/v1/repos/o/r/pulls/4":            pr,
		"GET /api/v1/repos/o/r/commits/abc/status": `{"statuses":[{"context":"build","status":"failure","target_url":"http://git/b"}]}`,
		"GET /api/v1/repos/o/r/issues/4/comments":  `[{"id":1,"body":"please add a test","user":{"login":"amy"},"created_at":"2026-01-02T00:00:00Z"}]`,
		"GET /api/v1/repos/o/r/pulls/4/reviews":    `[]`,
		"POST /api/v1/repos/o/r/issues/4/comments": `{}`,
	}
	reply, ok := routes[key]
	if !ok {
		http.Error(w, `{"message":"no route"}`, 404)
		return
	}
	io.WriteString(w, reply)
}

func setup(t *testing.T) (*Service, []store.Session, *fakeGitea) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := &fakeGitea{sent: map[string]string{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	g, err := st.AddGitServer(store.GitServer{Name: "home", Type: "gitea", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ref := store.RepoRef{ServerID: g.ID, FullName: "o/r", CloneURL: srv.URL + "/o/r.git"}
	first := store.Session{ID: "s1", Task: "Fix the login", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "agent/x", Repos: []store.RepoRef{ref}}
	if err := st.CreateSession(first); err != nil {
		t.Fatal(err)
	}
	st.AddEvent("s1", "out", "I fixed the redirect.")
	st.AddEvent("s1", "log", "r: pushed agent/x")
	st.AddEvent("s1", "prtitle", "Fix the login redirect")
	svc := &Service{Store: st, Provider: func(g store.GitServer) (gitprovider.Provider, error) {
		return gitprovider.New(gitprovider.Config{Type: g.Type, BaseURL: g.BaseURL, Token: "t"})
	}}
	return svc, []store.Session{first}, api
}

func TestAPushedBranchIsOfferedThenOpenedOnce(t *testing.T) {
	svc, thread, api := setup(t)
	props, err := svc.Proposals(thread)
	if err != nil || len(props) != 1 {
		t.Fatalf("proposals = %+v, %v", props, err)
	}
	p := props[0]
	if p.Repo != "o/r" || p.Branch != "agent/x" || p.Title != "Fix the login redirect" || !strings.Contains(p.Body, "I fixed the redirect.") {
		t.Errorf("proposal = %+v: the title is the agent's suggestion and the body says what it did", p)
	}

	v, err := svc.Open(context.Background(), thread, Proposal{ServerID: p.ServerID, Repo: p.Repo, Branch: p.Branch})
	if err != nil || v.Number != 4 || v.State != gitprovider.PROpen {
		t.Fatalf("Open = %+v, %v", v, err)
	}
	if !strings.Contains(api.sent["POST /api/v1/repos/o/r/pulls"], "Fix the login redirect") {
		t.Errorf("the suggested title was not used: %v", api.sent)
	}
	if v.Summary != "failure" || len(v.Checks) != 1 || len(v.Comments) != 1 {
		t.Errorf("view = %+v: it should carry the failing check and the comment", v)
	}
	if again, _ := svc.Proposals(thread); len(again) != 0 {
		t.Errorf("a branch with a pull request is not offered again: %+v", again)
	}
	if _, err := svc.Open(context.Background(), thread, Proposal{ServerID: p.ServerID, Repo: p.Repo, Branch: p.Branch}); err == nil {
		t.Error("opening the same branch twice must be refused")
	}
	if list, _ := svc.List(context.Background(), "s1"); len(list) != 1 || list[0].Number != 4 {
		t.Errorf("list = %+v", list)
	}
}

func TestABranchTheConversationDidNotPushCannotBeOpened(t *testing.T) {
	svc, thread, _ := setup(t)
	props, _ := svc.Proposals(thread)
	_, err := svc.Open(context.Background(), thread, Proposal{ServerID: props[0].ServerID, Repo: "o/other", Branch: "main"})
	if err == nil {
		t.Error("a request naming a repo or branch the conversation never pushed must be refused")
	}
}

func TestTheAgentToolsReadTheStatusAndComment(t *testing.T) {
	svc, thread, api := setup(t)
	ctx := context.Background()
	if got := svc.Call(ctx, "s1", ToolStatus, ""); !strings.Contains(got, "no pull request") {
		t.Errorf("before a pull request: %q", got)
	}
	if svc.HasOpen(ctx, "s1") {
		t.Error("no pull request is open yet")
	}
	props, _ := svc.Proposals(thread)
	if _, err := svc.Open(ctx, thread, Proposal{ServerID: props[0].ServerID, Repo: "o/r", Branch: "agent/x"}); err != nil {
		t.Fatal(err)
	}
	if !svc.HasOpen(ctx, "s1") {
		t.Error("the pull request is open")
	}
	if got := svc.Call(ctx, "s1", ToolStatus, ""); !strings.Contains(got, "o/r#4") || !strings.Contains(got, "build: failure") {
		t.Errorf("status = %q", got)
	}
	if got := svc.Call(ctx, "s1", ToolComments, ""); !strings.Contains(got, "amy: please add a test") {
		t.Errorf("comments = %q", got)
	}
	if got := svc.Call(ctx, "s1", ToolReply, "added the test"); !strings.Contains(got, "Commented on o/r#4") {
		t.Errorf("reply = %q", got)
	}
	if body := api.sent["POST /api/v1/repos/o/r/issues/4/comments"]; !strings.Contains(body, "added the test") || !strings.Contains(body, "through Unire") {
		t.Errorf("the comment sent = %q: it should say an agent wrote it", body)
	}
	if got := svc.Call(ctx, "s1", ToolReply, ""); !strings.Contains(got, "usage") {
		t.Errorf("an empty comment: %q", got)
	}
}

func TestTheAgentsOwnDescriptionIsUsedAndTheAnswerLinesKeepTheirBreaks(t *testing.T) {
	svc, thread, _ := setup(t)
	svc.Store.AddEvent("s1", "out", "second line")
	props, _ := svc.Proposals(thread)
	if !strings.Contains(props[0].Body, "I fixed the redirect.\nsecond line") {
		t.Errorf("without a description of its own the answer is used, with its line breaks: %q", props[0].Body)
	}
	svc.Store.AddEvent("s1", "prbody", "Fixes the redirect.\n\n- checked with the tests")
	props, _ = svc.Proposals(thread)
	if !strings.HasPrefix(props[0].Body, "Fixes the redirect.\n\n- checked with the tests") || strings.Contains(props[0].Body, "I fixed") {
		t.Errorf("the agent's own description wins: %q", props[0].Body)
	}
	if !strings.Contains(props[0].Body, "Opened from a Unire conversation") {
		t.Errorf("the description says where it came from: %q", props[0].Body)
	}
}
