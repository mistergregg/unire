package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/store"
)

var pngData = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func TestCheckAttachments(t *testing.T) {
	ok := []Attachment{
		{Name: "a.png", Data: pngData},
		{Name: "b.jpg", Data: append([]byte("\xff\xd8\xff\xe0"), make([]byte, 32)...)},
		{Name: "c.gif", Data: append([]byte("GIF87a"), make([]byte, 32)...)},
		{Name: "d.webp", Data: append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)},
	}
	if err := CheckAttachments(ok); err != nil {
		t.Errorf("good images: %v", err)
	}
	six := make([]Attachment, MaxAttachments+1)
	for i := range six {
		six[i] = ok[0]
	}
	big := append(append([]byte(nil), pngData...), make([]byte, MaxAttachmentSize)...)
	for name, list := range map[string][]Attachment{
		"too many":        six,
		"too big":         {{Name: "big.png", Data: big}},
		"not an image":    {{Name: "x.png", Data: []byte("hello there, I am text")}},
		"a pdf":           {{Name: "x.pdf", Data: []byte("%PDF-1.4\n")}},
		"empty":           {{Name: "x.png"}},
		"one bad of many": append(append([]Attachment(nil), ok[:2]...), Attachment{Name: "x", Data: []byte("<html>")}),
	} {
		if err := CheckAttachments(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAttachmentNamesAreSafeToShow(t *testing.T) {
	for in, want := range map[string]string{
		"shot.png":               "shot.png",
		`C:\Users\me\shot.png`:   "shot.png",
		"../../etc/passwd":       "passwd",
		"two\nlines.png":         "twolines.png",
		"":                       "image",
		"   ":                    "image",
		strings.Repeat("a", 300): strings.Repeat("a", 100),
	} {
		if got := attachName(in); got != want {
			t.Errorf("attachName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttachmentPathOnlyReachesStoredFiles(t *testing.T) {
	if p := AttachmentPath("/data/att", "abc123", "0123456789ab.png"); p != filepath.Join("/data/att", "abc123", "0123456789ab.png") {
		t.Errorf("a stored file: %q", p)
	}
	for _, c := range [][2]string{{"abc123", "../store.db"}, {"..", "0123456789ab.png"}, {"abc123", "0123456789ab.exe"}, {"abc/../x", "0123456789ab.png"}} {
		if p := AttachmentPath("/data/att", c[0], c[1]); p != "" {
			t.Errorf("AttachmentPath(%q, %q) = %q, want nothing", c[0], c[1], p)
		}
	}
	if AttachmentPath("", "abc123", "0123456789ab.png") != "" {
		t.Error("no folder, no attachments")
	}
}

// Every agent gets the images in its sandbox the same way: as files the message names.
func TestEveryAgentGetsAttachedImagesInItsSandbox(t *testing.T) {
	for _, agentName := range []string{"claude", "codex", "antigravity"} {
		t.Run(agentName, func(t *testing.T) {
			sb := &fakeSandbox{agentOutput: "done"}
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			dir := t.TempDir()
			m, err := New(st, func() Config { return Config{Runner: &fakeRunner{sb: sb}, Attachments: dir} })
			if err != nil {
				t.Fatal(err)
			}
			r := req("")
			r.Agent = agentName
			r.Attachments = []Attachment{{Name: "shot.png", Data: pngData}}
			x, err := m.Start(r)
			if err != nil {
				t.Fatal(err)
			}
			m.Wait()
			if got, _, _ := st.Session(x.ID); got.Prompt != "go" {
				t.Errorf("the owner's words are kept as written: %q", got.Prompt)
			}
			stored, _ := filepath.Glob(filepath.Join(dir, x.ID, "*.png"))
			if len(stored) != 1 {
				t.Fatalf("stored = %v", stored)
			}
			to := AttachmentDir + "/" + filepath.Base(stored[0])
			sb.mu.Lock()
			copied := sb.files[to]
			sb.mu.Unlock()
			if copied != string(pngData) {
				t.Errorf("%s was not copied into the sandbox", to)
			}
			if log := logText(st, x.ID); !strings.Contains(log, "attach: \n"+filepath.Base(stored[0])+" shot.png\n") {
				t.Errorf("the log should reference the image:\n%s", log)
			}
			RemoveAttachments(dir, x.ID)
			if _, err := os.Stat(filepath.Join(dir, x.ID)); !os.IsNotExist(err) {
				t.Errorf("RemoveAttachments left %v", err)
			}
		})
	}
}

func TestAttachmentsNeedAFolder(t *testing.T) {
	m, _ := setup(t, &fakeSandbox{})
	r := req("")
	r.Attachments = []Attachment{{Name: "shot.png", Data: pngData}}
	if _, err := m.Start(r); err == nil {
		t.Error("a manager with nowhere to keep attachments should refuse them")
	}
}

func TestAttachNoteNamesEveryImage(t *testing.T) {
	note := attachNote([]savedAttachment{{file: "aaaaaaaaaaaa.png", name: "one.png"}, {file: "bbbbbbbbbbbb.jpg", name: "two.jpg"}})
	for _, want := range []string{AttachmentDir + "/aaaaaaaaaaaa.png (one.png)", AttachmentDir + "/bbbbbbbbbbbb.jpg (two.jpg)", "open them to look"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q is missing %q", note, want)
		}
	}
	if attachNote(nil) != "" {
		t.Error("no attachments, no note")
	}
}
