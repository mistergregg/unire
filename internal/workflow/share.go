package workflow

import (
	"bytes"
	"context"
	"encoding/base64"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/tools"
)

// The share tool puts a file the agent wrote into the chat. In the sandbox,
// `unire share <file>` copies it to <id>.s ("name\nkind\ncontent"); when the
// turn ends Unire reads the copies, checks them again, and shows them as cards.

const shareDir = tools.Dir + "/share"

var shareFile = regexp.MustCompile(`^[0-9]{1,30}\.s$`)

// Shared is one file the agent showed to the owner.
type Shared struct {
	Name    string // the file's base name, one line
	Kind    string // tools.ShareText or tools.ShareImage
	Content string // the text, or the image as base64
}

// Event is the session event that shows the file in the chat: "name\nkind\ncontent".
func (s Shared) Event() string { return s.Name + "\n" + s.Kind + "\n" + s.Content }

// parseShared checks one copied file. It returns false for anything Unire should not show.
func parseShared(raw string) (Shared, bool) {
	name, rest, ok := strings.Cut(raw, "\n")
	if !ok {
		return Shared{}, false
	}
	kind, content, _ := strings.Cut(rest, "\n")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:] // a base name only
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	if name == "" {
		return Shared{}, false
	}
	switch kind {
	case tools.ShareText:
		if len(content) > tools.ShareMaxText || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return Shared{}, false
		}
	case tools.ShareImage:
		content = strings.TrimSpace(content)
		raw, err := base64.StdEncoding.DecodeString(content)
		if err != nil || len(raw) == 0 || len(raw) > tools.ShareMaxImage {
			return Shared{}, false
		}
	default:
		return Shared{}, false
	}
	return Shared{Name: name, Kind: kind, Content: content}, true
}

// requestedShares reads the files the agent shared this turn, oldest first, and
// removes them so a later turn does not show them again. It is empty when the
// session was not given the tool.
func (w *worker) requestedShares(ctx context.Context) []Shared {
	if !w.hasTool(tools.Share.Name) || ctx.Err() != nil {
		return nil
	}
	var ls bytes.Buffer
	if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"ls", "-1", shareDir}, Stdout: &ls}); err != nil {
		return nil // the agent shared nothing
	}
	var out []Shared
	names := strings.Fields(ls.String())
	sort.Strings(names) // ids are timestamps: oldest first
	for _, name := range names {
		if !shareFile.MatchString(name) || len(out) >= tools.ShareMaxFiles {
			continue
		}
		// An image is base64, a third larger than the file.
		raw, err := w.readFile(ctx, shareDir+"/"+name, tools.ShareMaxImage*4/3+1024)
		if err != nil {
			continue
		}
		if s, ok := parseShared(raw); ok {
			out = append(out, s)
		}
	}
	_ = w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"rm", "-rf", shareDir}})
	return out
}
