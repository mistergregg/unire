package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubBranchUpdate(t *testing.T) {
	var sent string
	status, reply := 202, `{"message":"Updating pull request branch."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v3/repos/o/r/compare/main...abc":
			w.Write([]byte(`{"behind_by":4,"ahead_by":1}`))
		case "GET /api/v3/repos/o/r/compare/release%2F1...abc":
			w.Write([]byte(`{"behind_by":0}`))
		case "PUT /api/v3/repos/o/r/pulls/7/update-branch":
			b := make([]byte, 200)
			n, _ := r.Body.Read(b)
			sent = string(b[:n])
			w.WriteHeader(status)
			w.Write([]byte(reply))
		default:
			http.Error(w, `{"message":"no route"}`, 404)
		}
	}))
	defer srv.Close()
	p, err := New(Config{Type: "github", BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	up, ok := AsBranchUpdater(p)
	if !ok {
		t.Fatal("GitHub can update a branch")
	}
	ctx := context.Background()
	pr := PullRequest{Number: 7, Base: "main", HeadSHA: "abc"}

	if n, err := up.BehindBy(ctx, "o/r", pr); err != nil || n != 4 {
		t.Fatalf("BehindBy = %d, %v", n, err)
	}
	if n, err := up.BehindBy(ctx, "o/r", PullRequest{Number: 7, Base: "release/1", HeadSHA: "abc"}); err != nil || n != 0 {
		t.Fatalf("a base with a slash must be escaped: %d, %v", n, err)
	}
	if _, err := up.BehindBy(ctx, "o/r", PullRequest{Number: 7}); err == nil {
		t.Fatal("without a base and head it cannot compare")
	}

	if conflict, err := up.UpdateBranch(ctx, "o/r", pr); err != nil || conflict {
		t.Fatalf("UpdateBranch = %v, %v", conflict, err)
	}
	if !strings.Contains(sent, `"expected_head_sha":"abc"`) {
		t.Fatalf("it must say which head it saw, so a moved branch is not clobbered: %s", sent)
	}

	status, reply = 422, `{"message":"merge conflict between base and head"}`
	if conflict, err := up.UpdateBranch(ctx, "o/r", pr); err != nil || !conflict {
		t.Fatalf("a conflict is reported as one, not an error: %v, %v", conflict, err)
	}
	status, reply = 422, `{"message":"expected head sha didn't match current head ref."}`
	if conflict, err := up.UpdateBranch(ctx, "o/r", pr); err == nil || conflict {
		t.Fatalf("a moved branch is an error to retry later, not a conflict: %v, %v", conflict, err)
	}
	status, reply = 403, `{"message":"Resource not accessible"}`
	if _, err := up.UpdateBranch(ctx, "o/r", pr); err == nil {
		t.Fatal("a token that cannot write must fail")
	}
}

func TestOtherProvidersDoNotClaimBranchUpdates(t *testing.T) {
	for _, typ := range []string{"gitea", "gitlab"} {
		p, err := New(Config{Type: typ, BaseURL: "http://x", Token: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := AsBranchUpdater(p); ok {
			t.Fatalf("%s is not supported yet and must be skipped, not guessed at", typ)
		}
	}
}
