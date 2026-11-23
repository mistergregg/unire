// Package preview decides how a repo can be previewed while an agent works on
// it: which built-in recipe fits, and what command and port it uses. It only
// describes previews; starting containers and proxying traffic live elsewhere.
package preview

import "fmt"

// Ready says how Unire knows a preview is up. Exactly one field is set.
type Ready struct {
	// HTTPPath is fetched from the preview port; any answer below 500 means ready.
	HTTPPath string `json:"http_path,omitempty"`
	// LogContains is a substring that appears in the command's output once it is ready.
	LogContains string `json:"log_contains,omitempty"`
}

// Recipe is one way to run a preview. Recipes are built in: a repo's config can
// choose one by name and tune its command, port and readiness, but it can never
// name an image (see ParseConfig).
type Recipe struct {
	Name string
	// Image is the container image the preview runs in. It is never read from a repo.
	Image string
	// Command is the argv to run, with no shell. It runs in the preview's directory.
	Command []string
	// Port is the port the app listens on inside the container.
	Port  int
	Ready Ready
}

// Catalog is the built-in recipes, by name. Treat it as read-only.
var Catalog = map[string]Recipe{
	"flutter": {
		Name: "flutter",
		// Not yet pinned or tried against a real project: pin a Flutter version
		// here once one has been checked, so the preview and the agent agree.
		Image:   "ghcr.io/cirruslabs/flutter:stable",
		Command: []string{"flutter", "run", "-d", "web-server", "--web-hostname", "0.0.0.0", "--web-port", "8080"},
		Port:    8080,
		Ready:   Ready{HTTPPath: "/"},
	},
	"static": {
		Name:    "static",
		Image:   "python:3-alpine",
		Command: []string{"python3", "-m", "http.server", "8080", "--bind", "0.0.0.0"},
		Port:    8080,
		Ready:   Ready{HTTPPath: "/"},
	},
}

// Preview is a recipe applied to one directory of a repo.
type Preview struct {
	// Name labels the tab. Unique within a repo.
	Name string
	// Dir is relative to the repo root; "." is the root. It never leaves the repo.
	Dir string
	Recipe
}

// fromCatalog returns a copy of a built-in recipe that is safe to modify.
func fromCatalog(name string) (Recipe, bool) {
	r, ok := Catalog[name]
	r.Command = append([]string(nil), r.Command...)
	return r, ok
}

func (r Ready) validate() error {
	if (r.HTTPPath == "") == (r.LogContains == "") {
		return fmt.Errorf("ready_when needs exactly one of http_path or log_contains")
	}
	return nil
}
