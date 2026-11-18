package workflow

import (
	"bytes"
	"context"
	"encoding/base64"
	"path"
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

var fileURI = regexp.MustCompile(`file://(/[^)\s"'>\]]+)`)

// requestedShares reads the files the agent shared this turn, oldest first, and
// removes them so a later turn does not show them again. It also automatically detects
// file:/// URIs in the agent's turn response and shares them. It is empty when the
// session was not given the tool.
func (w *worker) requestedShares(ctx context.Context, answer string) []Shared {
	if !w.hasTool(tools.Share.Name) || ctx.Err() != nil {
		return nil
	}
	var ls bytes.Buffer
	var out []Shared
	if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"ls", "-1", shareDir}, Stdout: &ls}); err == nil {
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
	}
	if len(out) < tools.ShareMaxFiles && answer != "" {
		out = w.autoShareLinks(ctx, answer, out)
	}
	return out
}

func (w *worker) autoShareLinks(ctx context.Context, text string, existing []Shared) []Shared {
	seen := make(map[string]bool)
	for _, s := range existing {
		seen[s.Name] = true
	}
	out := append([]Shared(nil), existing...)
	matches := fileURI.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(out) >= tools.ShareMaxFiles || ctx.Err() != nil {
			break
		}
		p := m[1]
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
		p = strings.TrimRight(p, ".,;:!?'\"")
		p = path.Clean(p)
		if !strings.HasPrefix(p, "/") {
			continue
		}
		if p == "/etc" || strings.HasPrefix(p, "/etc/") ||
			p == "/proc" || strings.HasPrefix(p, "/proc/") ||
			p == "/sys" || strings.HasPrefix(p, "/sys/") ||
			p == "/dev" || strings.HasPrefix(p, "/dev/") {
			continue
		}
		base := path.Base(p)
		if base == "" || base == "/" || base == "." || seen[base] {
			continue
		}
		ext := strings.ToLower(path.Ext(base))
		isImage := ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp"
		if isImage {
			raw, err := w.readFile(ctx, p, tools.ShareMaxImage+1)
			if err != nil || len(raw) == 0 || len(raw) > tools.ShareMaxImage {
				continue
			}
			b64 := base64.StdEncoding.EncodeToString([]byte(raw))
			if s, ok := parseShared(base + "\n" + tools.ShareImage + "\n" + b64); ok {
				out = append(out, s)
				seen[base] = true
			}
		} else {
			raw, err := w.readFile(ctx, p, tools.ShareMaxText+1)
			if err != nil || len(raw) == 0 || len(raw) > tools.ShareMaxText {
				continue
			}
			if s, ok := parseShared(base + "\n" + tools.ShareText + "\n" + raw); ok {
				out = append(out, s)
				seen[base] = true
			}
		}
	}
	return out
}
