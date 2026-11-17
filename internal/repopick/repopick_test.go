package repopick

import (
	"strings"
	"testing"
)

func repo(name string) Repo {
	return Repo{ServerID: "g", FullName: name, CloneURL: "https://git/" + name + ".git"}
}

var owned = []Repo{
	repo("Gbreed/echotalk-mobile"), repo("Gbreed/echotalk-backend"), repo("Gbreed/gbr-client"),
	repo("Gbreed/gbr-control-panel"), repo("Gbreed/gbr-games"), repo("mistergregg/unire"), repo("Gbreed/Gbreed-WIKI"),
}

func names(c []Candidate) string {
	var n []string
	for _, x := range c {
		n = append(n, shortName(x.FullName))
	}
	return strings.Join(n, ",")
}

func TestARepoNamedInTheTextIsPicked(t *testing.T) {
	for text, want := range map[string]string{
		"Check the echotalk mobile app for crashes": "echotalk-mobile",
		"look at echotalk-mobile":                   "echotalk-mobile",
		"why is EchoTalkMobile slow":                "echotalk-mobile",
		"update the gbr client login":               "gbr-client",
		"add a feature to unire for pull requests":  "unire",
		"the gbr control panel has a broken menu":   "gbr-control-panel",
	} {
		auto := Auto(Rank(text, owned, History{}, nil, nil))
		if len(auto) != 1 || shortName(auto[0].FullName) != want {
			t.Errorf("%q picked %s, want just %s", text, names(auto), want)
		}
	}
}

func TestAnAmbiguousNameIsSuggestedNotPicked(t *testing.T) {
	c := Rank("echotalk is crashing", owned, History{}, nil, nil)
	if len(Auto(c)) != 0 {
		t.Errorf("echotalk fits two repos, so neither is picked for the owner: %s", names(Auto(c)))
	}
	if len(c) != 2 {
		t.Errorf("both are suggested: %s", names(c))
	}
}

func TestGenericWordsPickNothing(t *testing.T) {
	if c := Rank("please fix the app, it looks slow on my phone", owned, History{}, nil, nil); len(Auto(c)) != 0 {
		t.Errorf("nothing is named: %s", names(c))
	}
	if c := Rank("", owned, History{}, nil, nil); c != nil {
		t.Error("no text, no guess")
	}
}

func TestReposUsuallyUsedTogetherComeAlong(t *testing.T) {
	m, b, c := "g/Gbreed/echotalk-mobile", "g/Gbreed/echotalk-backend", "g/Gbreed/gbr-client"
	hist := BuildHistory([][]string{{m, b}, {m, b}, {m, b}, {m}, {c}})
	got := Auto(Rank("the mobile app needs a new settings screen for echotalk-mobile", owned, hist, nil, nil))
	if names(got) != "echotalk-mobile,echotalk-backend" {
		t.Errorf("the backend usually goes with the mobile app: %s", names(got))
	}
	var why string
	for _, x := range got {
		if shortName(x.FullName) == "echotalk-backend" {
			why = strings.Join(x.Why, "; ")
		}
	}
	if !strings.Contains(why, "usually used with echotalk-mobile") {
		t.Errorf("it says why: %q", why)
	}
	// Used together only once: not a habit.
	once := BuildHistory([][]string{{m, b}, {m}, {m}})
	if got := Auto(Rank("echotalk-mobile settings", owned, once, nil, nil)); names(got) != "echotalk-mobile" {
		t.Errorf("one shared chat is not a habit: %s", names(got))
	}
}

func TestChosenReposAreNotOfferedAgainButTheirCompanionsAre(t *testing.T) {
	m, b := "g/Gbreed/echotalk-mobile", "g/Gbreed/echotalk-backend"
	hist := BuildHistory([][]string{{m, b}, {m, b}})
	got := Rank("also the api behind it, echotalk", owned, hist, []string{m}, nil)
	for _, x := range got {
		if x.Key() == m {
			t.Error("a repo already in the chat is not offered")
		}
	}
	if auto := Auto(got); names(auto) != "echotalk-backend" {
		t.Errorf("the companion of a repo already chosen comes along: %s", names(got))
	}
}

func TestAWordInEveryNameSaysLittle(t *testing.T) {
	got := Rank("something about gbr", owned, History{}, nil, nil)
	if len(Auto(got)) != 0 {
		t.Errorf("gbr is in three repos: %s", names(got))
	}
}

func TestTheDescriptionAndHistoryOnlyHelpAlongsideAMatch(t *testing.T) {
	r := repo("Gbreed/billing")
	r.Description = "Invoices and payments for customers"
	repos := append([]Repo{r}, owned...)
	hist := BuildHistory([][]string{{r.Key()}, {r.Key()}, {r.Key()}})
	got := Rank("fix the payments for customers", repos, hist, nil, nil)
	if len(got) == 0 || shortName(got[0].FullName) != "billing" || got[0].Auto {
		t.Errorf("a description match is suggested but not picked: %+v", got)
	}
	if got := Rank("make coffee", repos, hist, nil, nil); len(got) != 0 {
		t.Errorf("a habit alone proposes nothing: %s", names(got))
	}
}

func TestAtMostFourArePickedAtOnce(t *testing.T) {
	var many []Repo
	var text []string
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"} {
		many = append(many, repo("o/"+n))
		text = append(text, n)
	}
	if got := Auto(Rank(strings.Join(text, " and "), many, History{}, nil, nil)); len(got) != 4 {
		t.Errorf("auto-picked %d, want 4", len(got))
	}
}
