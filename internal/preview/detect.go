package preview

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// skipDirs are never searched for projects.
var skipDirs = map[string]bool{"node_modules": true, "build": true, "dist": true, "vendor": true}

// Load returns the previews for a repo: those in unire.preview.json if the
// repo has one, otherwise whatever Detect finds. A repo with nothing to preview
// returns no previews and no error.
func Load(repo fs.FS) ([]Preview, error) {
	if data, err := fs.ReadFile(repo, ConfigFile); err == nil {
		return ParseConfig(data)
	}
	return Detect(repo), nil
}

// Detect looks for something to preview without a config. It checks the repo
// root first; only if that has nothing does it look one level down, so a
// monorepo with app/ and api/ folders is found but a deep tree is not crawled.
func Detect(repo fs.FS) []Preview {
	if p, ok := detectDir(repo, "."); ok {
		return []Preview{p}
	}
	entries, err := fs.ReadDir(repo, ".")
	if err != nil {
		return nil
	}
	var out []Preview
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		if p, ok := detectDir(repo, name); ok {
			p.Name = name
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// detectDir picks a recipe for one directory. A pubspec.yaml means Flutter; a
// bare index.html means a static site.
func detectDir(repo fs.FS, dir string) (Preview, bool) {
	has := func(name string) bool {
		info, err := fs.Stat(repo, path.Join(dir, name))
		return err == nil && !info.IsDir()
	}
	name := ""
	switch {
	case has("pubspec.yaml"):
		name = "flutter"
	case has("index.html"):
		name = "static"
	default:
		return Preview{}, false
	}
	r, _ := fromCatalog(name)
	return Preview{Name: name, Dir: dir, Recipe: r}, true
}
