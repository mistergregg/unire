package agent

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// appendValidModel appends id to out when it passes ValidateModel and has not
// already been added.
func appendValidModel(out []string, seen map[string]bool, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" || seen[id] || ValidateModel(id) != nil {
		return out
	}
	seen[id] = true
	return append(out, id)
}

// antigravityModelsArgv asks `agy` for the models available to the signed-in
// account without running a turn.
var antigravityModelsArgv = []string{"agy", "models"}

// ParseAntigravityModelsOutput reads `agy models` output: spinner progress
// lines followed by tab-separated `<id>\t<displayName>` lines.
func ParseAntigravityModelsOutput(out string) ([]string, error) {
	cleaned := ansiEscapeRe.ReplaceAllString(out, "")
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	var models []string
	seen := map[string]bool{}
	var errLine string
	for _, line := range strings.Split(cleaned, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if id, _, hasTab := strings.Cut(trimmed, "\t"); hasTab {
			models = appendValidModel(models, seen, id)
			continue
		}
		if strings.HasPrefix(trimmed, "Error:") && errLine == "" {
			errLine = strings.TrimSpace(strings.TrimPrefix(trimmed, "Error:"))
		}
	}
	if len(models) > 0 {
		return models, nil
	}
	if errLine != "" {
		return nil, errors.New("agy: " + clip(errLine, 200))
	}
	return nil, errors.New("agy reported no models")
}

// codexModelsScript asks `codex app-server` for its model catalog (`model/list`)
// without running a turn or spending tokens. Like codexUsageScript, it keeps
// stdin open until the response for id=2 arrives.
const codexModelsScript = `d=$(mktemp -d) || exit 1
{
  printf '%s\n' '{"method":"initialize","id":1,"params":{"clientInfo":{"name":"unire","title":"Unire","version":"1"}}}' '{"method":"initialized"}' '{"method":"model/list","id":2,"params":{}}'
  i=0; while [ ! -e "$d/done" ] && [ $i -lt 100 ]; do sleep 0.2; i=$((i+1)); done
} | timeout 25 codex app-server 2>/dev/null | while IFS= read -r l; do
  case "$l" in '{"id":2,'*|*',"id":2}'*) printf '%s\n' "$l"; touch "$d/done";; esac
done
rm -rf "$d"`

// ParseCodexModelsOutput reads what codexModelsScript printed.
func ParseCodexModelsOutput(out string) ([]string, error) {
	for _, line := range strings.Split(out, "\n") {
		var m struct {
			ID     int            `json:"id"`
			Error  map[string]any `json:"error"`
			Result struct {
				Data []struct {
					ID     string `json:"id"`
					Model  string `json:"model"`
					Hidden bool   `json:"hidden"`
				} `json:"data"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &m) != nil || m.ID != 2 {
			continue
		}
		if m.Error != nil {
			msg, _ := m.Error["message"].(string)
			return nil, errors.New("codex: " + clip(msg, 200))
		}
		var models []string
		seen := map[string]bool{}
		for _, entry := range m.Result.Data {
			if entry.Hidden {
				continue
			}
			id := entry.ID
			if id == "" {
				id = entry.Model
			}
			models = appendValidModel(models, seen, id)
		}
		if len(models) == 0 {
			return nil, errors.New("codex reported no models")
		}
		return models, nil
	}
	return nil, errors.New("codex gave no answer about its models")
}

// claudeModelsScript sends the stream-json initialize control request so Claude
// Code reports its supported models without running a turn or spending tokens.
const claudeModelsScript = `printf '%s\n' '{"type":"control_request","request_id":"1","request":{"subtype":"initialize"}}' | timeout 20 claude -p --input-format stream-json --output-format stream-json --verbose 2>/dev/null`

// ParseClaudeModelsOutput reads the control_response line printed by
// claudeModelsScript.
func ParseClaudeModelsOutput(out string) ([]string, error) {
	for _, line := range strings.Split(out, "\n") {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				Subtype  string `json:"subtype"`
				Error    string `json:"error"`
				Response struct {
					Models []struct {
						Value string `json:"value"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &m) != nil || m.Type != "control_response" {
			continue
		}
		if m.Response.Subtype != "success" {
			msg := m.Response.Error
			if msg == "" {
				msg = "subtype " + m.Response.Subtype
			}
			return nil, errors.New("claude: " + clip(msg, 200))
		}
		var models []string
		seen := map[string]bool{}
		for _, entry := range m.Response.Response.Models {
			// Unire's UI always provides a "Default model" option mapped to "".
			if strings.TrimSpace(entry.Value) == "default" {
				continue
			}
			models = appendValidModel(models, seen, entry.Value)
		}
		if len(models) == 0 {
			return nil, errors.New("claude reported no models")
		}
		return models, nil
	}
	return nil, errors.New("claude gave no answer about its models")
}
