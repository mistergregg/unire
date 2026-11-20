package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/sessions"
)

// attachedPaths finds the attachments a message names.
var attachedPaths = regexp.MustCompile(regexp.QuoteMeta(sessions.AttachmentDir) + `/[0-9a-f]+\.(png|jpg|gif|webp)`)

// Enough of each format for its type to be recognised.
var (
	pngData  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	jpegData = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)
	gifData  = append([]byte("GIF89a"), make([]byte, 64)...)
	webpData = append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)
)

func image(name string, data []byte) map[string]any {
	return map[string]any{"name": name, "data": base64.StdEncoding.EncodeToString(data)}
}

func TestAttachedFilesAreChecked(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	many := []any{}
	for i := 0; i < sessions.MaxAttachments+1; i++ {
		many = append(many, image("a.png", pngData))
	}
	big := append(append([]byte(nil), pngData...), make([]byte, sessions.MaxAttachmentSize)...)
	for _, c := range []struct {
		name   string
		images []any
		want   string
	}{
		{"too big", []any{image("huge.png", big)}, "huge.png is too big"},
		{"too many", many, "at most 5 files"},
		{"not base64", []any{map[string]any{"name": "x.png", "data": "%%%"}}, "could not be read"},
		{"empty", []any{image("x.png", nil)}, "is empty"},
	} {
		code, raw := e.do("POST", "/api/sessions", map[string]any{"agent": "claude", "prompt": "look", "repos": []any{repoRef(base, "greg/api")}, "images": c.images})
		if code != http.StatusBadRequest || !strings.Contains(raw, c.want) {
			t.Errorf("%s: start = %d %s, want 400 with %q", c.name, code, raw, c.want)
		}
	}
	// A body bigger than the most a message may carry is refused before it is read.
	huge := strings.Repeat("A", maxMessageBody+1)
	if code, _ := e.do("POST", "/api/sessions", map[string]any{"prompt": "x", "images": []any{map[string]any{"name": "x.png", "data": huge}}}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body = %d, want 413", code)
	}
	// Every accepted format, at the limit of how many.
	ok := []any{image("a.png", pngData), image("b.jpg", jpegData), image("c.gif", gifData), image("d.webp", webpData), image("e.png", pngData)}
	if code, raw := e.do("POST", "/api/sessions", map[string]any{"agent": "claude", "prompt": "look", "repos": []any{repoRef(base, "greg/api")}, "images": ok}); code != http.StatusCreated {
		t.Errorf("five good images = %d %s", code, raw)
	}
}

// waitCopied waits until a file named like suffix has been copied into the sandbox,
// and then a moment for the message that names it to be handed to the agent (which
// reads it once the test lets it go on).
func (e *env) waitCopied(suffix string) {
	e.t.Helper()
	for i := 0; i < 300; i++ {
		e.box.mu.Lock()
		found := false
		for p := range e.box.files {
			found = found || strings.HasSuffix(p, suffix)
		}
		e.box.mu.Unlock()
		if found {
			time.Sleep(100 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("no %s file was copied into the sandbox", suffix)
}

func TestAttachedImagesReachTheAgentInEveryKindOfMessage(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.mu.Lock()
	e.box.block = make(chan struct{})
	e.box.mu.Unlock()

	// The first message.
	code, first := e.launch(base, map[string]any{"prompt": "what is wrong here?", "images": []any{image("screen shot.png", pngData)}})
	if code != http.StatusCreated {
		t.Fatalf("launch = %d %v", code, first)
	}
	id := first["id"].(string)
	if first["prompt"] != "what is wrong here?" {
		t.Errorf("the owner's words are kept as written: %q", first["prompt"])
	}
	e.waitStatus(id, "running")

	// A message while the agent works.
	code, raw := e.do("POST", "/api/sessions/"+id+"/steer", map[string]any{"text": "and this one", "images": []any{image("b.jpg", jpegData)}})
	if code != http.StatusOK {
		t.Fatalf("steer = %d %s", code, raw)
	}
	var steer map[string]string
	json.Unmarshal([]byte(raw), &steer)
	e.waitCopied(".jpg")
	e.box.mu.Lock()
	close(e.box.block)
	e.box.block = nil
	e.box.mu.Unlock()
	e.waitStatus(id, "done")

	// A reply.
	code, second := replyTo(e, id, map[string]any{"prompt": "now this", "images": []any{image("c.webp", webpData)}})
	if code != http.StatusCreated {
		t.Fatalf("reply = %d %v", code, second)
	}
	id2 := second["id"].(string)
	e.waitStatus(id2, "done")

	e.box.mu.Lock()
	told := append([]string(nil), e.box.prompts...)
	files := map[string][]byte{}
	for k, v := range e.box.files {
		files[k] = v
	}
	unready := append([]string(nil), e.box.unready...)
	e.box.mu.Unlock()

	// Each message named its image, and the image was in the sandbox before the agent read it.
	want := map[string][]byte{"what is wrong here?": pngData, "and this one": jpegData, "now this": webpData}
	for words, data := range want {
		found := false
		for _, p := range told {
			if !strings.Contains(p, words) {
				continue
			}
			found = true
			paths := attachedPaths.FindAllString(p, -1)
			if len(paths) != 1 || !strings.Contains(p, "The owner attached: "+paths[0]) {
				t.Errorf("the message %q should name its one image: %q", words, p)
				continue
			}
			if string(files[paths[0]]) != string(data) {
				t.Errorf("%s was not copied into the sandbox", paths[0])
			}
		}
		if !found {
			t.Errorf("the agent was never told %q: %q", words, told)
		}
	}
	if len(unready) > 0 {
		t.Errorf("messages named images before they were in the sandbox: %v", unready)
	}

	// The log keeps a reference, not the image, and the message as the owner wrote it.
	_, logs := e.do("GET", "/api/sessions/"+id+"/logs", nil)
	var res struct {
		Events []struct{ Kind, Text string }
	}
	json.Unmarshal([]byte(logs), &res)
	var attach []string
	for _, ev := range res.Events {
		switch ev.Kind {
		case "attach":
			attach = append(attach, ev.Text)
		case "steer":
			if ev.Text != steer["id"]+"\nnext\nand this one" {
				t.Errorf("a steer event is unchanged: %q", ev.Text)
			}
		}
	}
	file := regexp.MustCompile(`^[0-9a-f]{12}\.(png|jpg)`)
	if len(attach) != 2 || !strings.HasPrefix(attach[0], "\n") || !strings.HasSuffix(attach[0], " screen shot.png") ||
		!strings.HasPrefix(attach[1], steer["id"]+"\n") || !strings.HasSuffix(attach[1], " b.jpg") {
		t.Fatalf("attach events = %q", attach)
	}
	if strings.Contains(logs, base64.StdEncoding.EncodeToString(pngData)[:20]) {
		t.Error("the image itself must not be in the log")
	}

	// The page can show it.
	name, _, _ := strings.Cut(strings.TrimPrefix(attach[0], "\n"), " ")
	if !file.MatchString(name) {
		t.Fatalf("stored file name %q", name)
	}
	resp, err := e.c.Get(e.ts.URL + "/api/sessions/" + id + "/attachments/" + name)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("attachment = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, bad := range []string{"..%2F..%2Fstore.db", "x.png", name + ".txt"} {
		if code, _ := e.do("GET", "/api/sessions/"+id+"/attachments/"+bad, nil); code != http.StatusNotFound {
			t.Errorf("attachment %q = %d, want 404", bad, code)
		}
	}
	if code, _ := e.do("GET", "/api/sessions/"+id2+"/attachments/"+name, nil); code != http.StatusNotFound {
		t.Errorf("another session's attachment = %d, want 404", code)
	}

	// Deleting the session deletes what was attached to it.
	dir := filepath.Join(e.dir, "attachments", id)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("attachments are kept in the data directory: %v", err)
	}
	for _, s := range []string{id2, id} {
		if code, raw := e.do("DELETE", "/api/sessions/"+s, nil); code != http.StatusOK {
			t.Fatalf("delete = %d %s", code, raw)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("attachments left after delete: %v", err)
	}
}

func TestAnAttachedFileIsOnlyEverDownloaded(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	html := []byte("<html><script>alert(1)</script></html>")
	code, first := e.launch(base, map[string]any{"prompt": "see the page", "images": []any{image("page.html", html), image("server.log", []byte("boom\n"))}})
	if code != http.StatusCreated {
		t.Fatalf("launch = %d %v", code, first)
	}
	id := first["id"].(string)
	e.waitStatus(id, "done")
	told := e.sentToAgent()
	paths := regexp.MustCompile(regexp.QuoteMeta(sessions.AttachmentDir)+`/[0-9a-f]{12}\.(html|log)`).FindAllString(told, -1)
	if len(paths) != 2 {
		t.Fatalf("the agent should be told about both files: %q", told)
	}
	e.box.mu.Lock()
	got := string(e.box.files[paths[0]])
	e.box.mu.Unlock()
	if got != string(html) {
		t.Errorf("%s was not copied into the sandbox: %q", paths[0], got)
	}
	resp, err := e.c.Get(e.ts.URL + "/api/sessions/" + id + "/attachments/" + filepath.Base(paths[0]))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("a non-image file must be served as a download: %d %v", resp.StatusCode, resp.Header)
	}
}
