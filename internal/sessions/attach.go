package sessions

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
)

// Attachments: images the owner adds to a message (the first one, a reply, or a
// message to a working agent). Each is saved under Config.Attachments, in a folder
// per session, and copied into the sandbox before the agent reads the message,
// whose text names where it is. Every agent can open an image file, so this works
// the same for all of them. The log keeps only a short reference: an "attach"
// event, "<message>\n<file> <name>\n…", where <message> is empty for the turn's
// prompt or a steer message's id, and <file> is the stored file's name.

// Limits on what the owner may attach to one message.
const (
	MaxAttachments    = 5
	MaxAttachmentSize = 5 << 20 // bytes, per image
)

// AttachmentDir is where attachments are put in the sandbox: in the workspace but
// outside every repo, so nothing is committed.
const AttachmentDir = "/workspace/.unire/attachments"

// Attachment is an image the owner sent with a message.
type Attachment struct {
	Name string // the file name it had on the owner's computer
	Data []byte
}

// attachTypes are the image types accepted, by detected content type.
var attachTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

// CheckAttachments says what is wrong with a message's attachments, if anything:
// too many, too big, or not a PNG, JPEG, GIF or WebP image (judged by the content,
// not the name).
func CheckAttachments(list []Attachment) error {
	if len(list) > MaxAttachments {
		return fmt.Errorf("attach at most %d images to a message", MaxAttachments)
	}
	for _, a := range list {
		name := attachName(a.Name)
		switch {
		case len(a.Data) == 0:
			return fmt.Errorf("%s is empty", name)
		case len(a.Data) > MaxAttachmentSize:
			return fmt.Errorf("%s is too big (%d MB at most)", name, MaxAttachmentSize>>20)
		case attachTypes[http.DetectContentType(a.Data)] == "":
			return fmt.Errorf("%s is not a PNG, JPEG, GIF or WebP image", name)
		}
	}
	return nil
}

// attachName makes a file name safe to show and to put on one line of the log.
func attachName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	if name == "" || name == "." || name == "/" {
		return "image"
	}
	return name
}

// savedAttachment is an attachment written to Unire's data directory.
type savedAttachment struct {
	file string // stored file name: "<id>.<ext>"
	name string // the owner's name for it
	host string // where it is on the host
}

func (a savedAttachment) sandboxPath() string { return AttachmentDir + "/" + a.file }

// storedFile matches a stored attachment's file name.
var (
	storedFile  = regexp.MustCompile(`^[0-9a-f]{12}\.(png|jpg|gif|webp)$`)
	sessionIDRe = regexp.MustCompile(`^[0-9a-f]+$`)
)

// AttachmentPath is where a stored attachment of a session is on the host, or ""
// when the name is not one Unire gave a file (so a request cannot reach elsewhere).
func AttachmentPath(dir, id, file string) string {
	if dir == "" || !storedFile.MatchString(file) || !sessionIDRe.MatchString(id) {
		return ""
	}
	return filepath.Join(dir, id, file)
}

// RemoveAttachments deletes what was saved for a session.
func RemoveAttachments(dir, id string) {
	if dir == "" || !sessionIDRe.MatchString(id) {
		return
	}
	_ = os.RemoveAll(filepath.Join(dir, id))
}

// saveAttachments writes a message's attachments to the session's folder.
func (m *Manager) saveAttachments(sessionID string, list []Attachment) ([]savedAttachment, error) {
	if len(list) == 0 {
		return nil, nil
	}
	if err := CheckAttachments(list); err != nil {
		return nil, err
	}
	root := m.config().Attachments
	if root == "" {
		return nil, errors.New("this Unire cannot keep attachments")
	}
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("could not save the attachments: %w", err)
	}
	var out []savedAttachment
	for _, a := range list {
		s := savedAttachment{file: newID() + "." + attachTypes[http.DetectContentType(a.Data)], name: attachName(a.Name)}
		s.host = filepath.Join(dir, s.file)
		if err := os.WriteFile(s.host, a.Data, 0o600); err != nil {
			removeSaved(out)
			return nil, fmt.Errorf("could not save the attachments: %w", err)
		}
		out = append(out, s)
	}
	return out, nil
}

func removeSaved(list []savedAttachment) {
	for _, a := range list {
		_ = os.Remove(a.host)
	}
}

// attachEvent is the log's reference to a message's attachments (see the top of this file).
func attachEvent(message string, list []savedAttachment) string {
	lines := []string{message}
	for _, a := range list {
		lines = append(lines, a.file+" "+a.name)
	}
	return strings.Join(lines, "\n")
}

// attachNote is what the agent is told after the message: where the images are.
func attachNote(list []savedAttachment) string {
	if len(list) == 0 {
		return ""
	}
	var parts []string
	for _, a := range list {
		parts = append(parts, a.sandboxPath()+" ("+a.name+")")
	}
	return "\n\nThe owner attached: " + strings.Join(parts, ", ") + " — open them to look."
}

// attachFiles are the copies to make into the sandbox.
func attachFiles(list []savedAttachment) []agent.File {
	var out []agent.File
	for _, a := range list {
		out = append(out, agent.File{From: a.host, To: a.sandboxPath()})
	}
	return out
}
