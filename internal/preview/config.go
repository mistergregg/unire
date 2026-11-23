package preview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ConfigFile is the name of the optional per-repo preview config.
const ConfigFile = "unire.preview.json"

// Config is the content of unire.preview.json.
type Config struct {
	Previews []ConfigPreview `json:"previews"`
}

// ConfigPreview picks a built-in recipe and may tune it. There is deliberately
// no image field: a repo, or an agent editing it, must not choose what runs
// next to the owner's data.
type ConfigPreview struct {
	Name      string   `json:"name"`
	Recipe    string   `json:"recipe"`
	Dir       string   `json:"dir,omitempty"`
	Command   []string `json:"command,omitempty"`
	Port      int      `json:"port,omitempty"`
	ReadyWhen *Ready   `json:"ready_when,omitempty"`
}

// ParseConfig reads and checks a unire.preview.json and returns the previews it describes.
func ParseConfig(data []byte) ([]Preview, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if err := rejectImage(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if len(c.Previews) == 0 {
		return nil, fmt.Errorf("%s: no previews listed", ConfigFile)
	}
	seen := map[string]bool{}
	out := make([]Preview, 0, len(c.Previews))
	for i, cp := range c.Previews {
		p, err := cp.build()
		if err != nil {
			return nil, fmt.Errorf("%s: preview %d: %w", ConfigFile, i+1, err)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("%s: two previews are named %q", ConfigFile, p.Name)
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	return out, nil
}

// rejectImage gives a clear message when a config tries to choose an image,
// instead of the generic "unknown field".
func rejectImage(raw map[string]json.RawMessage) error {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw["previews"], &list) != nil {
		return nil // the real decode below reports the shape error
	}
	for _, item := range list {
		if _, ok := item["image"]; ok {
			return errors.New(ConfigFile + ": a repo cannot choose a container image; pick a built-in recipe, or ask the owner to add a custom one in Settings")
		}
	}
	return nil
}

func (cp ConfigPreview) build() (Preview, error) {
	base, ok := fromCatalog(cp.Recipe)
	if !ok {
		return Preview{}, fmt.Errorf("unknown recipe %q", cp.Recipe)
	}
	dir, err := cleanDir(cp.Dir)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{Name: cp.Name, Dir: dir, Recipe: base}
	if p.Name == "" {
		p.Name = cp.Recipe
	}
	if len(cp.Command) > 0 {
		p.Command = append([]string(nil), cp.Command...)
	}
	if cp.Port != 0 {
		if cp.Port < 1 || cp.Port > 65535 {
			return Preview{}, fmt.Errorf("port %d is out of range", cp.Port)
		}
		p.Port = cp.Port
	}
	if cp.ReadyWhen != nil {
		if err := cp.ReadyWhen.validate(); err != nil {
			return Preview{}, err
		}
		p.Ready = *cp.ReadyWhen
	}
	return p, nil
}

// cleanDir returns dir as a clean path inside the repo, or an error if it is
// absolute or climbs out of the repo.
func cleanDir(dir string) (string, error) {
	if dir == "" {
		return ".", nil
	}
	if strings.HasPrefix(dir, "/") || strings.Contains(dir, `\`) {
		return "", fmt.Errorf("dir %q must be a relative path inside the repo", dir)
	}
	c := path.Clean(dir)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("dir %q leaves the repo", dir)
	}
	return c, nil
}
