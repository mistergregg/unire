package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/mistergregg/unire/internal/sessions"
)

// Images the owner attaches to a message come base64-encoded in its JSON body. They
// are checked, saved to the data directory and handed to the agent by the session
// manager (see sessions/attach.go); the log keeps only a reference.

// maxMessageBody is the largest body a message may have: the most and the largest
// images, base64-encoded, and the text.
const maxMessageBody = sessions.MaxAttachments*(sessions.MaxAttachmentSize/3*4+4) + 1<<20

// imageIn is an attached image as the page sends it.
type imageIn struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64, optionally as a data: URL
}

// readMessage reads the JSON body of a message that may carry images.
func readMessage(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("attach at most %d images of %d MB each", sessions.MaxAttachments, sessions.MaxAttachmentSize>>20))
		} else {
			writeErr(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

// decodeImages turns what the page sent into attachments, and checks them.
func decodeImages(in []imageIn) ([]sessions.Attachment, error) {
	if len(in) > sessions.MaxAttachments {
		return nil, fmt.Errorf("attach at most %d images to a message", sessions.MaxAttachments)
	}
	var out []sessions.Attachment
	for _, im := range in {
		data := im.Data
		if strings.HasPrefix(data, "data:") {
			_, data, _ = strings.Cut(data, ",")
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("%s could not be read", im.Name)
		}
		out = append(out, sessions.Attachment{Name: im.Name, Data: b})
	}
	if err := sessions.CheckAttachments(out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachmentsDir holds what owners attached, a folder per session.
func (s *Server) attachmentsDir() string { return filepath.Join(s.dataDir, "attachments") }

// getAttachment serves an image attached to a message of a session.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	p := sessions.AttachmentPath(s.attachmentsDir(), x.ID, r.PathValue("file"))
	if p == "" {
		writeErr(w, http.StatusNotFound, "no such attachment")
		return
	}
	// An attachment never changes, so the page may keep it.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeFile(w, r, p)
}
