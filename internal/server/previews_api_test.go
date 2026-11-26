package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/preview"
)

type fakePreviewEngine struct {
	mu      sync.Mutex
	ran     []preview.RunSpec
	removed []string
	logs    string
}

func (f *fakePreviewEngine) HaveImage(context.Context, string) bool { return true }
func (f *fakePreviewEngine) Pull(context.Context, string) error     { return nil }
func (f *fakePreviewEngine) Run(_ context.Context, s preview.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, s)
	return nil
}
func (f *fakePreviewEngine) Running(context.Context, string) bool { return true }
func (f *fakePreviewEngine) Logs(context.Context, string, int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs
}
func (f *fakePreviewEngine) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakePreviewEngine) Address(context.Context, string, string) (string, error) {
	return "10.9.8.7", nil
}
func (f *fakePreviewEngine) Exec(context.Context, string, []string) error { return nil }

// previewEnv is a server with one finished chat (network: full access) and a
// preview service whose containers and detection are fakes.
func previewEnv(t *testing.T) (*env, string, *fakePreviewEngine) {
	t.Helper()
	e, base := pickEnv(t)
	code, a, raw := startChat(e, "build a recipe app", []map[string]any{repoRef(base, "Gbreed/echotalk-mobile")})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %s", code, raw)
	}
	id := a["id"].(string)
	e.waitStatus(id, "done")
	eng := &fakePreviewEngine{logs: "Launching lib/main.dart"}
	p := &previewService{
		mgr: &preview.Manager{
			Engine:       eng,
			HTTPGet:      func(context.Context, string) (int, error) { return 200, nil },
			PollEvery:    time.Millisecond,
			ReadyTimeout: 3 * time.Second,
		},
		hub: &preview.Hub{Signer: preview.NewSigner(), Host: "127.0.0.1"},
		detect: func(ctx context.Context, image, volume string) ([]preview.Found, error) {
			if volume != "unire-ws-"+id {
				t.Errorf("detection looked at volume %q, want the conversation's workspace", volume)
			}
			ps, _ := preview.ParseConfig([]byte(`{"previews":[{"name":"app","recipe":"flutter"}]}`))
			return []preview.Found{{Repo: "echotalk-mobile", Previews: ps}, {Repo: "broken", Err: "unire.preview.json: nope"}}, nil
		},
	}
	e.srv.previews = p
	t.Cleanup(p.shutdown)
	return e, id, eng
}

func previewState(t *testing.T, e *env, path string, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := e.do("GET", path, nil)
		var v map[string]any
		json.Unmarshal([]byte(body), &v)
		if code == 200 && v["state"] == want {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reached %q: %d %s", want, code, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPreviewsAreOffWithoutAPortRange(t *testing.T) {
	e, id, _ := previewEnv(t)
	e.srv.previews = &previewService{mgr: &preview.Manager{Engine: &fakePreviewEngine{}}, hubErr: "Previews are off. Set UNIRE_PREVIEW_PORTS"}
	list := getJSON(t, e, "/api/sessions/"+id+"/previews")
	if list["available"] != false || !strings.Contains(list["reason"].(string), "UNIRE_PREVIEW_PORTS") {
		t.Fatalf("list = %v", list)
	}
	code, body := e.do("POST", "/api/sessions/"+id+"/previews/echotalk-mobile/app/start", nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "UNIRE_PREVIEW_PORTS") {
		t.Fatalf("start = %d %s", code, body)
	}
}

func TestPreviewsNeedProtectionOfTheNetwork(t *testing.T) {
	e, id, _ := previewEnv(t)
	e.srv.egressInfo.Available = false
	e.srv.egressInfo.Reason = "not in a container"
	list := getJSON(t, e, "/api/sessions/"+id+"/previews")
	if list["available"] != false || !strings.Contains(list["reason"].(string), "not in a container") {
		t.Fatalf("list = %v", list)
	}
}

func TestPreviewListShowsWhatWasFoundAndWhatIsBroken(t *testing.T) {
	e, id, _ := previewEnv(t)
	list := getJSON(t, e, "/api/sessions/"+id+"/previews")
	if list["available"] != true {
		t.Fatalf("list = %v", list)
	}
	ps := list["previews"].([]any)
	if len(ps) != 1 {
		t.Fatalf("list = %v", list)
	}
	p := ps[0].(map[string]any)
	if p["repo"] != "echotalk-mobile" || p["name"] != "app" || p["recipe"] != "flutter" || p["status"] != nil {
		t.Fatalf("preview = %v", p)
	}
	probs := list["problems"].([]any)
	if len(probs) != 1 || probs[0].(map[string]any)["repo"] != "broken" {
		t.Fatalf("problems = %v", probs)
	}
}

func TestStartingAPreviewRunsItUnderTheChatsNetworkRules(t *testing.T) {
	e, id, eng := previewEnv(t)
	path := "/api/sessions/" + id + "/previews/echotalk-mobile/app"
	code, body := e.do("POST", path+"/start", nil)
	if code != 200 {
		t.Fatalf("start = %d %s", code, body)
	}
	v := previewState(t, e, path, "ready")
	url, _ := v["url"].(string)
	port := strings.TrimPrefix(strings.SplitN(url, "/?", 2)[0], "http://127.0.0.1:")
	if !strings.HasPrefix(url, "http://127.0.0.1:") || port == "" || strings.Contains(port, ":") {
		t.Fatalf("url %q must be on this host at the preview's own port", url)
	}
	tok := url[strings.Index(url, "unire_token=")+len("unire_token="):]
	key := id + ".echotalk-mobile.app"
	if _, ok := e.srv.previews.hub.Signer.Verify(key, preview.PurposeLink, tok); !ok {
		t.Fatal("the link token must be a valid link token for this preview")
	}

	spec := eng.ran[0]
	if spec.Workspace != "unire-ws-"+id || spec.Network != "unire-sandbox" || spec.WorkDir != "/workspace/echotalk-mobile" {
		t.Fatalf("spec %+v", spec)
	}
	if spec.Env["HTTPS_PROXY"] != "http://unire-egress:3128" {
		t.Fatalf("the app must be pointed at the egress proxy: %v", spec.Env)
	}
	// The chat was started with full access, so the proxy holds that mode for the preview.
	pol, ok := e.srv.egressProxy.Policy("10.9.8.7")
	if !ok || string(pol.Mode) != "full" {
		t.Fatalf("policy %+v ok=%v", pol, ok)
	}

	code, body = e.do("POST", path+"/stop", nil)
	if code != 200 {
		t.Fatalf("stop = %d %s", code, body)
	}
	if _, ok := e.srv.egressProxy.Policy("10.9.8.7"); ok {
		t.Fatal("a stopped preview must be forgotten by the proxy")
	}
	if _, open := e.srv.previews.hub.Port(key); open {
		t.Fatal("a stopped preview must free its port")
	}
	var st map[string]any
	_, body = e.do("GET", path, nil)
	json.Unmarshal([]byte(body), &st)
	if st["state"] != "stopped" || st["url"] != nil {
		t.Fatalf("status after stop: %s", body)
	}
}

func TestPreviewLogsAndUnknownThings(t *testing.T) {
	e, id, _ := previewEnv(t)
	base := "/api/sessions/" + id + "/previews/echotalk-mobile/app"
	if code, _ := e.do("GET", base, nil); code != http.StatusNotFound {
		t.Errorf("an unstarted preview has no status: %d", code)
	}
	if code, _ := e.do("GET", base+"/logs", nil); code != http.StatusNotFound {
		t.Errorf("an unstarted preview has no log: %d", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+id+"/previews/echotalk-mobile/nope/start", nil); code != http.StatusNotFound {
		t.Errorf("a preview that was not detected must be 404: %d", code)
	}
	if code, _ := e.do("POST", "/api/sessions/nope/previews/a/b/start", nil); code != http.StatusNotFound {
		t.Errorf("an unknown chat must be 404: %d", code)
	}
	e.do("POST", base+"/start", nil)
	previewState(t, e, base, "ready")
	code, logs := e.do("GET", base+"/logs", nil)
	if code != 200 || !strings.Contains(logs, "Launching lib/main.dart") {
		t.Errorf("logs = %d %q", code, logs)
	}
}

func TestPreviewEndpointsNeedALogin(t *testing.T) {
	e, id, _ := previewEnv(t)
	anon := &http.Client{}
	for _, c := range [][2]string{
		{"GET", "/api/sessions/" + id + "/previews"},
		{"POST", "/api/sessions/" + id + "/previews/r/n/start"},
		{"POST", "/api/sessions/" + id + "/previews/r/n/stop"},
		{"GET", "/api/sessions/" + id + "/previews/r/n"},
		{"GET", "/api/sessions/" + id + "/previews/r/n/logs"},
	} {
		req, _ := http.NewRequest(c[0], e.ts.URL+c[1], nil)
		req.Header.Set("X-Requested-With", "unire")
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a login = %d", c[0], c[1], resp.StatusCode)
		}
	}
}

func TestRequestHost(t *testing.T) {
	for in, want := range map[string]string{"nas:8080": "nas", "192.168.1.5:8080": "192.168.1.5", "nas": "nas", "[::1]:8080": "::1", "[::1]": "::1"} {
		r, _ := http.NewRequest("GET", "http://x/", nil)
		r.Host = in
		if got := requestHost(r); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPreviewFrameCSP(t *testing.T) {
	e, _, _ := previewEnv(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/status", nil)
	req.Host = "nas.local:8080"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-src 'self' http://nas.local:*") {
		t.Fatalf("CSP = %q, want frame-src for preview host", got)
	}

	e.srv.previews.hub = nil
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := resp2.Header.Get("Content-Security-Policy"); strings.Contains(got, "frame-src") {
		t.Fatalf("CSP without preview hub = %q, should not include frame-src", got)
	}

	bad, _ := http.NewRequest("GET", "http://x/", nil)
	bad.Host = "evil.com; script-src 'unsafe-inline'"
	if got := previewFrameSource(bad); got != "" {
		t.Fatalf("invalid host must be rejected, got %q", got)
	}
}
