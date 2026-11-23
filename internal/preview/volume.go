package preview

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing/fstest"
	"time"
)

// Found is what detection found in one repo of a workspace.
type Found struct {
	Repo     string // the repo's folder under /workspace
	Previews []Preview
	// Err is set when the repo has a unire.preview.json that cannot be used.
	Err string
}

const (
	marker = "@@unire-config "
	endTag = "@@unire-end"
)

// listScript prints the files that matter (one path per line), then each
// unire.preview.json between marker lines. It runs in the workspace volume,
// mounted at /ws. The listing is shallow on purpose: Detect only looks at the
// repo root and one folder down.
const listScript = `cd /ws || exit 1
find . -maxdepth 3 -type f \( -name pubspec.yaml -o -name index.html -o -name unire.preview.json \) -not -path '*/node_modules/*' -not -path '*/.git/*' -print
find . -maxdepth 2 -type f -name unire.preview.json -not -path '*/.git/*' -exec sh -c 'echo "` + marker + `$1"; cat "$1"; echo; echo "` + endTag + `"' _ {} \;`

// listArgs builds the docker command that runs listScript: no network, nothing
// writable, no extra privileges.
func listArgs(image, volume string) []string {
	return []string{
		"run", "--rm", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume + ":/ws:ro", image, "sh", "-c", listScript,
	}
}

// DetectInVolume finds previews in the repos of a workspace volume by running
// a short throwaway container from image (the sandbox image has the tools).
// It does not create the volume: a conversation that has not run yet has none.
func DetectInVolume(ctx context.Context, image, volume string) ([]Found, error) {
	if !safeName.MatchString(volume) || image == "" || strings.HasPrefix(image, "-") {
		return nil, fmt.Errorf("unsafe workspace or image name")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "volume", "inspect", volume).Run(); err != nil {
		return nil, fmt.Errorf("this conversation has no workspace yet; let the agent run once first")
	}
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", listArgs(image, volume)...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not look at the workspace: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return foundFromListing(out.String()), nil
}

// foundFromListing turns the output of listScript into previews, one Found per
// repo that has any (or a broken config), sorted by repo.
func foundFromListing(out string) []Found {
	repos := map[string]fstest.MapFS{}
	add := func(rel string, data []byte) {
		rel = strings.TrimPrefix(rel, "./")
		repo, rest, ok := strings.Cut(rel, "/")
		if !ok || repo == "" || strings.HasPrefix(repo, ".") || rest == "" {
			return // a file straight in /workspace belongs to no repo
		}
		if repos[repo] == nil {
			repos[repo] = fstest.MapFS{}
		}
		repos[repo][rest] = &fstest.MapFile{Data: data}
	}
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, marker):
			path := strings.TrimPrefix(line, marker)
			var body []string
			for i++; i < len(lines) && lines[i] != endTag; i++ {
				body = append(body, lines[i])
			}
			add(path, []byte(strings.Join(body, "\n")))
		case strings.HasPrefix(line, "./"):
			// A config listed here is replaced by its content below.
			if !strings.HasSuffix(line, "/"+ConfigFile) {
				add(line, nil)
			}
		}
	}
	names := make([]string, 0, len(repos))
	for n := range repos {
		names = append(names, n)
	}
	sort.Strings(names)
	var found []Found
	for _, n := range names {
		ps, err := Load(repos[n])
		switch {
		case err != nil:
			found = append(found, Found{Repo: n, Err: err.Error()})
		case len(ps) > 0:
			found = append(found, Found{Repo: n, Previews: ps})
		}
	}
	return found
}
