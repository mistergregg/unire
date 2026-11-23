package preview

import (
	"strings"
	"testing"
	"testing/fstest"
)

func files(names ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for _, n := range names {
		m[n] = &fstest.MapFile{Data: []byte("x")}
	}
	return m
}

func TestDetectFlutterAtRoot(t *testing.T) {
	got := Detect(files("pubspec.yaml", "lib/main.dart"))
	if len(got) != 1 || got[0].Recipe.Name != "flutter" || got[0].Dir != "." {
		t.Fatalf("got %+v", got)
	}
}

func TestDetectStaticAtRoot(t *testing.T) {
	got := Detect(files("index.html", "style.css"))
	if len(got) != 1 || got[0].Recipe.Name != "static" {
		t.Fatalf("got %+v", got)
	}
}

func TestFlutterBeatsStaticInTheSameDir(t *testing.T) {
	// A Flutter project often has web/index.html, but at the root it is Flutter.
	got := Detect(files("pubspec.yaml", "index.html"))
	if len(got) != 1 || got[0].Recipe.Name != "flutter" {
		t.Fatalf("got %+v", got)
	}
}

func TestDetectMonorepoOneLevelDown(t *testing.T) {
	got := Detect(files("app/pubspec.yaml", "site/index.html", "api/main.go", "node_modules/x/index.html", ".git/index.html"))
	if len(got) != 2 || got[0].Name != "app" || got[1].Name != "site" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Dir != "app" || got[0].Recipe.Name != "flutter" || got[1].Recipe.Name != "static" {
		t.Fatalf("got %+v", got)
	}
}

func TestRootWinsOverSubdirs(t *testing.T) {
	got := Detect(files("pubspec.yaml", "docs/index.html"))
	if len(got) != 1 || got[0].Dir != "." {
		t.Fatalf("got %+v", got)
	}
}

func TestDetectNothing(t *testing.T) {
	if got := Detect(files("main.go", "README.md")); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadPrefersConfig(t *testing.T) {
	repo := files("pubspec.yaml")
	repo[ConfigFile] = &fstest.MapFile{Data: []byte(`{"previews":[{"name":"site","recipe":"static","dir":"web"}]}`)}
	got, err := Load(repo)
	if err != nil || len(got) != 1 || got[0].Name != "site" || got[0].Dir != "web" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadReportsBadConfig(t *testing.T) {
	repo := fstest.MapFS{ConfigFile: &fstest.MapFile{Data: []byte(`{nope`)}}
	if _, err := Load(repo); err == nil {
		t.Fatal("a broken config must be an error, not silently ignored")
	}
}

func TestConfigOverridesRecipeDefaults(t *testing.T) {
	got, err := ParseConfig([]byte(`{"previews":[{"recipe":"flutter","port":9000,"command":["flutter","run"],"ready_when":{"log_contains":"is being served"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := got[0]
	if p.Name != "flutter" || p.Port != 9000 || len(p.Command) != 2 || p.Ready.LogContains == "" || p.Ready.HTTPPath != "" {
		t.Fatalf("got %+v", p)
	}
	if p.Image != Catalog["flutter"].Image {
		t.Fatal("the image must stay the recipe's")
	}
}

func TestConfigOverrideDoesNotChangeCatalog(t *testing.T) {
	before := append([]string(nil), Catalog["flutter"].Command...)
	got, err := ParseConfig([]byte(`{"previews":[{"recipe":"flutter","command":["x"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got[0].Command[0] = "changed"
	if strings.Join(Catalog["flutter"].Command, " ") != strings.Join(before, " ") {
		t.Fatal("editing a preview changed the shared catalog")
	}
}

func TestPreviewsDoNotShareTheCatalogCommand(t *testing.T) {
	want := strings.Join(Catalog["flutter"].Command, " ")
	for _, got := range [][]Preview{
		Detect(files("pubspec.yaml")),
		mustParse(t, `{"previews":[{"recipe":"flutter"}]}`),
	} {
		got[0].Command[0] = "changed"
		if strings.Join(Catalog["flutter"].Command, " ") != want {
			t.Fatal("editing a preview changed the shared catalog")
		}
	}
}

func mustParse(t *testing.T, cfg string) []Preview {
	t.Helper()
	got, err := ParseConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConfigCannotChooseAnImage(t *testing.T) {
	_, err := ParseConfig([]byte(`{"previews":[{"recipe":"static","image":"evil/miner"}]}`))
	if err == nil || !strings.Contains(err.Error(), "cannot choose a container image") {
		t.Fatalf("got %v", err)
	}
}

func TestConfigRejects(t *testing.T) {
	cases := map[string]string{
		"unknown recipe":  `{"previews":[{"recipe":"cobol"}]}`,
		"unknown field":   `{"previews":[{"recipe":"static","privileged":true}]}`,
		"no previews":     `{"previews":[]}`,
		"absolute dir":    `{"previews":[{"recipe":"static","dir":"/etc"}]}`,
		"escaping dir":    `{"previews":[{"recipe":"static","dir":"../x"}]}`,
		"sneaky dir":      `{"previews":[{"recipe":"static","dir":"a/../../x"}]}`,
		"backslash dir":   `{"previews":[{"recipe":"static","dir":"..\\x"}]}`,
		"bad port":        `{"previews":[{"recipe":"static","port":70000}]}`,
		"two ready modes": `{"previews":[{"recipe":"static","ready_when":{"http_path":"/","log_contains":"x"}}]}`,
		"no ready mode":   `{"previews":[{"recipe":"static","ready_when":{}}]}`,
		"duplicate name":  `{"previews":[{"recipe":"static"},{"recipe":"static"}]}`,
	}
	for name, cfg := range cases {
		if _, err := ParseConfig([]byte(cfg)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestConfigCleansDir(t *testing.T) {
	got, err := ParseConfig([]byte(`{"previews":[{"recipe":"static","dir":"web/./site/"}]}`))
	if err != nil || got[0].Dir != "web/site" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCatalogRecipesAreValid(t *testing.T) {
	for name, r := range Catalog {
		if r.Name != name || r.Image == "" || len(r.Command) == 0 || r.Port == 0 {
			t.Errorf("%s: incomplete recipe %+v", name, r)
		}
		if err := r.Ready.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
