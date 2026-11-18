package pulls

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
)

type fakeGitea struct {
	mu   sync.Mutex
	sent map[string]string
	made int // pull requests created so far: each gets the next number, from 4
}

func (f *fakeGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.sent[key] = string(b)
	f.mu.Unlock()
	prJSON := func(n int) string {
		return fmt.Sprintf(`{"number":%d,"title":"Fix it","html_url":"http://git/pr/%d","state":"open","head":{"ref":"agent/x","sha":"abc"},"base":{"ref":"main"}}`, n, n)
	}
	pr := prJSON(4)
	if key == "POST /api/v1/repos/o/r/pulls" {
		f.mu.Lock()
		pr = prJSON(4 + f.made)
		f.made++
		f.mu.Unlock()
	}
	// Every pull request answers like number 4.
	lookup := regexp.MustCompile(`/(pulls|issues)/d+`).ReplaceAllString(key, "/$1/4")
	routes := map[string]string{
		"GET /api/v1/repos/o/r":                    `{"default_branch":"main"}`,
		"POST /api/v1/repos/o/r/pulls":             pr,
		"GET /api/v1/repos/o/r/pulls/4":            pr,
		"GET /api/v1/repos/o/r/commits/abc/status": `{"statuses":[{"context":"build","status":"failure","target_url":"http://git/b"}]}`,
		"GET /api/v1/repos/o/r/issues/4/comments":  `[{"id":1,"body":"please add a test","user":{"login":"amy"},"created_at":"2026-01-02T00:00:00Z"}]`,
		"GET /api/v1/repos/o/r/pulls/4/reviews":    `[]`,
		"POST /api/v1/repos/o/r/issues/4/comments": `{}`,
		"PATCH /api/v1/repos/o/r/pulls/4":          `{}`,
	}
	reply, ok := routes[lookup]
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
	if svc.HasAny(ctx, "s1") { // none opened yet
		t.Error("no pull request is open yet")
	}
	props, _ := svc.Proposals(thread)
	if _, err := svc.Open(ctx, thread, Proposal{ServerID: props[0].ServerID, Repo: "o/r", Branch: "agent/x"}); err != nil {
		t.Fatal(err)
	}
	if !svc.HasAny(ctx, "s1") {
		t.Error("a pull request was opened")
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

func TestAPullRequestCanBeClosedOnlyByTheConversationThatOpenedIt(t *testing.T) {
	svc, thread, api := setup(t)
	ctx := context.Background()
	props, _ := svc.Proposals(thread)
	v, err := svc.Open(ctx, thread, Proposal{ServerID: props[0].ServerID, Repo: "o/r", Branch: "agent/x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetOpen(ctx, "s1", v.ServerID, "o/r", 4, false); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(api.sent["PATCH /api/v1/repos/o/r/pulls/4"], `"state":"closed"`) {
		t.Errorf("close was not sent: %v", api.sent)
	}
	if _, err := svc.SetOpen(ctx, "s1", v.ServerID, "o/r", 99, false); err == nil {
		t.Error("a pull request this conversation did not open must not be changed")
	}
	if _, err := svc.SetOpen(ctx, "other-conversation", v.ServerID, "o/r", 4, false); err == nil {
		t.Error("another conversation must not be able to close it")
	}
}

func TestTheSameChangeCanGoIntoSeveralBranches(t *testing.T) {
	svc, thread, api := setup(t)
	ctx := context.Background()
	// The agent also made a branch for a release branch.
	svc.Store.AddEvent("s1", "log", "r: also pushed agent/x-release (into release/1.2)")

	props, _ := svc.Proposals(thread)
	if len(props) != 2 {
		t.Fatalf("the session branch and the extra branch are both offered: %+v", props)
	}
	byBranch := map[string]Proposal{}
	for _, p := range props {
		byBranch[p.Branch] = p
	}
	extra := byBranch["agent/x-release"]
	if extra.Base != "release/1.2" || !strings.HasSuffix(extra.Title, "(into release/1.2)") {
		t.Errorf("the extra branch goes into the branch it was made for, and says so: %+v", extra)
	}

	first, err := svc.Open(ctx, thread, Proposal{ServerID: extra.ServerID, Repo: "o/r", Branch: "agent/x"})
	if err != nil || first.Number != 4 {
		t.Fatalf("open the session branch: %+v %v", first, err)
	}
	second, err := svc.Open(ctx, thread, Proposal{ServerID: extra.ServerID, Repo: "o/r", Branch: "agent/x-release"})
	if err != nil || second.Number != 5 {
		t.Fatalf("open the extra branch: %+v %v", second, err)
	}
	if !strings.Contains(api.sent["POST /api/v1/repos/o/r/pulls"], "release/1.2") {
		t.Errorf("the extra pull request goes into release/1.2: %q", api.sent["POST /api/v1/repos/o/r/pulls"])
	}
	// The same branch into one more branch.
	third, err := svc.Open(ctx, thread, Proposal{ServerID: extra.ServerID, Repo: "o/r", Branch: "agent/x", Base: "develop", Title: "Fix it (into develop)"})
	if err != nil || third.Number != 6 {
		t.Fatalf("open the same branch into another: %+v %v", third, err)
	}
	if _, err := svc.Open(ctx, thread, Proposal{ServerID: extra.ServerID, Repo: "o/r", Branch: "agent/x", Base: "develop"}); err == nil {
		t.Error("the same branch into the same branch twice must be refused")
	}
	if left, _ := svc.Proposals(thread); len(left) != 0 {
		t.Errorf("nothing is left to offer: %+v", left)
	}
	if list, _ := svc.List(ctx, "s1"); len(list) != 3 {
		t.Errorf("three pull requests: %+v", list)
	}
}

func TestTheAgentCanCloseAndReopenItsPullRequest(t *testing.T) {
	svc, thread, api := setup(t)
	ctx := context.Background()
	props, _ := svc.Proposals(thread)
	if _, err := svc.Open(ctx, thread, Proposal{ServerID: props[0].ServerID, Repo: "o/r", Branch: "agent/x"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Call(ctx, "s1", ToolReopen, ""); !strings.Contains(got, "no pull request to reopen") {
		t.Errorf("nothing is closed yet: %q", got)
	}
	if got := svc.Call(ctx, "s1", ToolClose, ""); !strings.Contains(got, "now closed") || !strings.Contains(got, "Nothing was merged") {
		t.Errorf("close = %q", got)
	}
	if !strings.Contains(api.sent["PATCH /api/v1/repos/o/r/pulls/4"], `"state":"closed"`) {
		t.Errorf("the server was told to close it: %v", api.sent)
	}
	if !svc.HasAny(ctx, "s1") {
		t.Error("a closed pull request keeps the tools, so it can be reopened")
	}
}

// A later turn that pushes the same branch updates the same pull request, and its text is
// written from that turn, not copied from the first turn's description.
func TestTheDescriptionComesFromTheTurnThatPushedNotAnEarlierOne(t *testing.T) {
	svc, thread, _ := setup(t)
	st := svc.Store
	st.AddEvent("s1", "prbody", "Description of the FIRST piece of work.")
	second := store.Session{ID: "s2", Task: "Fix the login", Agent: "antigravity", Mode: "hands-off", Prompt: "now also add tests", Branch: "agent/x", ParentID: "s1", Repos: thread[0].Repos}
	if err := st.CreateSession(second); err != nil {
		t.Fatal(err)
	}
	st.AddEvent("s2", "out", "I added the tests for the redirect.")
	st.AddEvent("s2", "log", "r: pushed agent/x")
	st.MarkFinished("s2", store.StatusDone, "")
	thread = append(thread, second)

	props, err := svc.Proposals(thread)
	if err != nil || len(props) != 1 {
		t.Fatalf("one branch, one proposal: %+v %v", props, err)
	}
	if strings.Contains(props[0].Body, "FIRST piece") || !strings.Contains(props[0].Body, "I added the tests for the redirect.") {
		t.Errorf("the offered description describes the turn that pushed: %q", props[0].Body)
	}
	if props[0].Title == "" || strings.Contains(props[0].Title, "FIRST") {
		t.Errorf("title = %q", props[0].Title)
	}
}

func TestALaterTurnOffersToUpdateThePullRequestsText(t *testing.T) {
	svc, thread, api := setup(t)
	ctx := context.Background()
	st := svc.Store
	props, _ := svc.Proposals(thread)
	if _, err := svc.Open(ctx, thread, Proposal{ServerID: props[0].ServerID, Repo: "o/r", Branch: "agent/x"}); err != nil {
		t.Fatal(err)
	}
	if ups, _ := svc.Updates(thread); len(ups) != 0 {
		t.Fatalf("nothing has changed since it was opened: %+v", ups)
	}

	time.Sleep(1200 * time.Millisecond) // the next turn finishes after the pull request's text was set
	second := store.Session{ID: "s2", Task: "Fix the login", Agent: "antigravity", Mode: "hands-off", Prompt: "also tests", Branch: "agent/x", ParentID: "s1", Repos: thread[0].Repos}
	if err := st.CreateSession(second); err != nil {
		t.Fatal(err)
	}
	st.AddEvent("s2", "prtitle", "Fix the login redirect and add tests")
	st.AddEvent("s2", "prbody", "Adds tests for the redirect.")
	st.AddEvent("s2", "log", "r: pushed agent/x")
	st.MarkFinished("s2", store.StatusDone, "")
	second, _, _ = st.Session("s2") // as the server loads it: with the time it finished
	thread = append(thread, second)

	ups, err := svc.Updates(thread)
	if err != nil || len(ups) != 1 {
		t.Fatalf("the branch was pushed again, so its text can be updated: %+v %v", ups, err)
	}
	u := ups[0]
	if u.Number != 4 || u.Title != "Fix the login redirect and add tests" || !strings.Contains(u.Body, "Adds tests for the redirect.") {
		t.Errorf("update = %+v", u)
	}
	if _, err := svc.SetText(ctx, "s1", u.ServerID, u.Repo, u.Number, u.Title, u.Body); err != nil {
		t.Fatal(err)
	}
	sent := api.sent["PATCH /api/v1/repos/o/r/pulls/4"]
	if !strings.Contains(sent, "Fix the login redirect and add tests") || !strings.Contains(sent, "Adds tests for the redirect.") {
		t.Errorf("the server was sent %q", sent)
	}
	if ups, _ := svc.Updates(thread); len(ups) != 0 {
		t.Errorf("once updated, nothing more is offered: %+v", ups)
	}
	if _, err := svc.SetText(ctx, "s1", u.ServerID, u.Repo, 99, "x", "y"); err == nil {
		t.Error("a pull request this conversation did not open must not be changed")
	}
	if _, err := svc.SetText(ctx, "s1", u.ServerID, u.Repo, 4, " ", "y"); err == nil {
		t.Error("a title is required")
	}
}
