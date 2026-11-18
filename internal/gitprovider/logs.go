package gitprovider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// LogProvider reads why a check failed: the end of its log, and which step failed. A
// server that cannot give logs through its API does not implement it (or says so).
type LogProvider interface {
	FailureLog(ctx context.Context, repo string, c Check) (string, error)
}

// ErrNoLogs is returned when a server cannot give a check's log through its API.
var ErrNoLogs = fmt.Errorf("this git server does not give job logs through its API")

// getText fetches a plain-text reply (a job log), following redirects (GitHub sends logs
// from another host; Go drops the token on the way). It reads at most limit bytes.
func (c *client) getText(ctx context.Context, path string, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", &httpError{Status: resp.StatusCode, msg: "the server rejected the token for the job log (HTTP " + fmt.Sprint(resp.StatusCode) + ")" + serverReason(resp, body)}
	case resp.StatusCode == http.StatusNotFound:
		return "", &httpError{Status: resp.StatusCode, msg: "the job log was not found (it may have expired)"}
	case resp.StatusCode >= 300:
		return "", &httpError{Status: resp.StatusCode, msg: fmt.Sprintf("unexpected response (HTTP %d)", resp.StatusCode)}
	}
	return string(body), nil
}

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)
	errorLineRe = regexp.MustCompile(`(?i)(##\[error\]|\berror\b|\bfailed\b|\bfail\b|--- FAIL|panic:|exit code [1-9]|exit status [1-9])`)
)

// tidyLog shortens a log to what helps: the lines that look like errors (so a failure
// early in a long log is not lost), then the end of the log. Colour codes and GitHub's
// timestamps are removed, and the result is capped.
func tidyLog(raw string, tailLines, maxChars int) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	clean := make([]string, 0, len(lines))
	for _, l := range lines {
		l = ansiRe.ReplaceAllString(l, "")
		l = timestampRe.ReplaceAllString(l, "")
		clean = append(clean, strings.TrimRight(l, " \t\r"))
	}
	start := len(clean) - tailLines
	if start < 0 {
		start = 0
	}
	var errs []string
	for _, l := range clean[:start] { // errors above the part shown at the end
		if errorLineRe.MatchString(l) && len(errs) < 12 {
			errs = append(errs, l)
		}
	}
	var b strings.Builder
	if len(errs) > 0 {
		b.WriteString("Error lines from earlier in the log:\n" + strings.Join(errs, "\n") + "\n\n")
	}
	b.WriteString("End of the log:\n" + strings.TrimSpace(strings.Join(clean[start:], "\n")))
	out := b.String()
	if r := []rune(out); len(r) > maxChars {
		out = "…" + string(r[len(r)-maxChars:])
	}
	return out
}

const (
	logTailLines = 90
	logMaxChars  = 7000
	logReadLimit = 6 << 20
)

// ---- GitHub ----

func (g github) FailureLog(ctx context.Context, repo string, c Check) (string, error) {
	jobID := c.JobID
	var failedSteps []string
	if jobID == 0 && c.RunID != 0 {
		// A workflow run: its failed job is the one to read.
		var res struct {
			Jobs []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"jobs"`
		}
		if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=50", repo, c.RunID), &res); err != nil {
			return "", err
		}
		for _, j := range res.Jobs {
			if j.Conclusion == "failure" || j.Conclusion == "timed_out" || j.Conclusion == "cancelled" {
				jobID = j.ID
				failedSteps = append(failedSteps, "job "+j.Name)
				break
			}
		}
	}
	if jobID == 0 {
		return "", fmt.Errorf("no failed job found for %q", c.Name)
	}
	var job struct {
		Name  string `json:"name"`
		Steps []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	}
	if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d", repo, jobID), &job); err == nil {
		for _, s := range job.Steps {
			if s.Conclusion == "failure" {
				failedSteps = append(failedSteps, "step "+s.Name)
			}
		}
	}
	text, err := g.c.getText(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, jobID), logReadLimit)
	if err != nil {
		return "", err
	}
	head := ""
	if len(failedSteps) > 0 {
		head = "Failed: " + strings.Join(failedSteps, ", ") + "\n"
	}
	return head + tidyLog(text, logTailLines, logMaxChars), nil
}

// ---- GitLab ----

func (g gitlab) FailureLog(ctx context.Context, repo string, c Check) (string, error) {
	if c.JobID == 0 {
		return "", fmt.Errorf("no job found for %q", c.Name)
	}
	text, err := g.c.getText(ctx, fmt.Sprintf("%s/jobs/%d/trace", g.project(repo), c.JobID), logReadLimit)
	if err != nil {
		return "", err
	}
	return "Failed: job " + c.Name + "\n" + tidyLog(text, logTailLines, logMaxChars), nil
}

// ---- Gitea and Forgejo ----

// Gitea's API does not give a job's log, so the agent is pointed at the check's page.
func (g gitea) FailureLog(ctx context.Context, repo string, c Check) (string, error) {
	return "", ErrNoLogs
}
