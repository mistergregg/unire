package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/tools"
)

// The tools of an automation's chat: the agent can list the open pull requests on the
// chat's repos (dependency updates and the like), and merge one when the owner allows
// it for the automation. Only Unire holds the tokens, so it does both; it merges only a
// pull request that is open, not a draft, has checks that all passed, and still has the
// commit those checks ran on.

const prListMax = 30 // pull requests shown per repo

// automationFor finds the automation of a turn: the one it was started for, else the
// one whose chat it is in (a reply the owner wrote there).
func (s *Server) automationFor(automationID, rootID string) (store.Automation, bool) {
	if automationID != "" {
		if a, ok, err := s.store.Automation(automationID); err == nil && ok {
			return a, true
		}
	}
	a, ok, err := s.store.AutomationForChat(rootID)
	return a, err == nil && ok
}

// automationTools answers the tools of automation chats for the session manager
// (sessions.Automations).
type automationTools struct{ s *Server }

// ToolsFor gives an automation's chat its pull request tools.
func (t automationTools) ToolsFor(automationID, rootID string) []tools.Tool {
	return t.s.automationToolsFor(automationID, rootID)
}

// Call answers pr-list and pr-merge.
func (t automationTools) Call(ctx context.Context, automationID, rootID, tool, args string) string {
	return t.s.automationCall(ctx, automationID, rootID, tool, args)
}

func (s *Server) automationToolsFor(automationID, rootID string) []tools.Tool {
	a, ok := s.automationFor(automationID, rootID)
	if !ok {
		return nil
	}
	if a.MayMerge {
		return []tools.Tool{tools.PRList, tools.PRMerge}
	}
	return []tools.Tool{tools.PRList}
}

func (s *Server) automationCall(ctx context.Context, automationID, rootID, tool, args string) string {
	a, ok := s.automationFor(automationID, rootID)
	if !ok {
		return "This tool is only available in an automation's chat."
	}
	repos := s.automationRepos(a, rootID)
	if len(repos) == 0 {
		return "This automation has no repos, so there are no pull requests to look at."
	}
	args = strings.Trim(strings.TrimSpace(args), `"'`)
	switch tool {
	case tools.PRList.Name:
		return s.prList(ctx, repos, args)
	case tools.PRMerge.Name:
		if !a.MayMerge {
			return "Merging is turned off for this automation. Say what you would merge; the owner can allow it on the Automations page."
		}
		return s.prMerge(ctx, repos, rootID, args)
	}
	return "This tool is not available in this conversation."
}

// automationRepos are the repos the tools may touch: the automation's, and the ones
// its chat has (a repo the agent brought in with the owner's yes).
func (s *Server) automationRepos(a store.Automation, rootID string) []store.RepoRef {
	seen := map[string]bool{}
	var out []store.RepoRef
	add := func(refs []store.RepoRef) {
		for _, r := range refs {
			key := r.ServerID + "/" + r.FullName
			if !seen[key] {
				seen[key] = true
				out = append(out, r)
			}
		}
	}
	add(a.Repos)
	if rootID != "" {
		if thread, err := s.threadOf(rootID); err == nil {
			for _, t := range thread {
				add(t.Repos)
			}
		}
	}
	return out
}

// pickRepo resolves "owner/repo" (or nothing, with a single repo) to one of repos.
func pickRepo(repos []store.RepoRef, name string) (store.RepoRef, string) {
	if name == "" {
		if len(repos) == 1 {
			return repos[0], ""
		}
		return store.RepoRef{}, "Name the repo: " + automationRepoNames(repos) + "."
	}
	for _, r := range repos {
		if strings.EqualFold(r.FullName, name) {
			return r, ""
		}
	}
	return store.RepoRef{}, fmt.Sprintf("%s is not one of this chat's repos (%s).", name, automationRepoNames(repos))
}

func automationRepoNames(repos []store.RepoRef) string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.FullName
	}
	return strings.Join(names, ", ")
}

// parsePRRef reads "owner/repo#12", "#12" or "12"; number is 0 without one.
func parsePRRef(s string) (repo string, number int, ok bool) {
	repo, num, has := strings.Cut(s, "#")
	repo = strings.TrimSpace(repo)
	if !has {
		if n, err := strconv.Atoi(repo); err == nil {
			return "", n, n > 0
		}
		return repo, 0, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return repo, n, true
}

// prAPI returns the pull request and merge support for a repo's server.
func (s *Server) prAPI(ref store.RepoRef) (gitprovider.PullRequests, gitprovider.Merger, error) {
	g, ok, err := s.store.GitServer(ref.ServerID)
	if err != nil || !ok {
		return nil, nil, fmt.Errorf("the git server of %s is no longer set up", ref.FullName)
	}
	p, err := s.provider(g)
	if err != nil {
		return nil, nil, err
	}
	prs, err := gitprovider.AsPullRequests(p)
	if err != nil {
		return nil, nil, err
	}
	m, err := gitprovider.AsMerger(p)
	if err != nil {
		return nil, nil, err
	}
	return prs, m, nil
}

// checksSummary boils checks down to one word and counts.
func checksSummary(checks []gitprovider.Check) (state string, text string) {
	var ok, bad, wait int
	var failing []string
	for _, c := range checks {
		switch c.State {
		case gitprovider.CheckSuccess:
			ok++
		case gitprovider.CheckFailure:
			bad++
			failing = append(failing, c.Name)
		default:
			wait++
		}
	}
	switch {
	case len(checks) == 0:
		return "none", "no checks"
	case bad > 0:
		return gitprovider.CheckFailure, fmt.Sprintf("%d of %d checks failed (%s)", bad, len(checks), strings.Join(failing, ", "))
	case wait > 0:
		return gitprovider.CheckPending, fmt.Sprintf("%d of %d checks still running", wait, len(checks))
	}
	return gitprovider.CheckSuccess, fmt.Sprintf("all %d checks passed", ok)
}

func (s *Server) prList(ctx context.Context, repos []store.RepoRef, args string) string {
	name, number, ok := parsePRRef(args)
	if !ok {
		return `unire pr-list: give a repo ("owner/repo") or a pull request ("owner/repo#12").`
	}
	if number > 0 {
		ref, msg := pickRepo(repos, name)
		if msg != "" {
			return msg
		}
		return s.prDetail(ctx, ref, number)
	}
	targets := repos
	if name != "" {
		ref, msg := pickRepo(repos, name)
		if msg != "" {
			return msg
		}
		targets = []store.RepoRef{ref}
	}
	var b strings.Builder
	for _, ref := range targets {
		prs, m, err := s.prAPI(ref)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n\n", ref.FullName, err)
			continue
		}
		list, err := m.OpenPRs(ctx, ref.FullName)
		if err != nil {
			fmt.Fprintf(&b, "%s: could not list pull requests: %v\n\n", ref.FullName, err)
			continue
		}
		if len(list) == 0 {
			fmt.Fprintf(&b, "%s: no open pull requests\n\n", ref.FullName)
			continue
		}
		fmt.Fprintf(&b, "%s: %d open pull request(s)\n", ref.FullName, len(list))
		for i, p := range list {
			if i >= prListMax {
				fmt.Fprintf(&b, "  (%d more not shown)\n", len(list)-prListMax)
				break
			}
			full, err := prs.GetPR(ctx, ref.FullName, p.Number) // the list may leave out what the checks need
			if err == nil {
				if full.Author == "" {
					full.Author = p.Author
				}
				p = full
			}
			checks := "checks unknown"
			if cs, err := prs.Checks(ctx, ref.FullName, p); err == nil {
				_, checks = checksSummary(cs)
			}
			draft := ""
			if p.Draft {
				draft = " [draft]"
			}
			fmt.Fprintf(&b, "  #%d %s%s\n     by %s, %s -> %s, %s, %s\n", p.Number, p.Title, draft, authorOr(p.Author), p.Branch, p.Base, checks, p.URL)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func authorOr(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func (s *Server) prDetail(ctx context.Context, ref store.RepoRef, number int) string {
	prs, _, err := s.prAPI(ref)
	if err != nil {
		return err.Error()
	}
	p, err := prs.GetPR(ctx, ref.FullName, number)
	if err != nil {
		return fmt.Sprintf("could not read %s#%d: %v", ref.FullName, number, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s#%d %s\nstate: %s, draft: %v, by %s\nbranch: %s -> %s\n%s\n", ref.FullName, p.Number, p.Title, p.State, p.Draft, authorOr(p.Author), p.Branch, p.Base, p.URL)
	checks, err := prs.Checks(ctx, ref.FullName, p)
	if err != nil {
		fmt.Fprintf(&b, "checks: could not read them: %v\n", err)
		return b.String()
	}
	_, sum := checksSummary(checks)
	fmt.Fprintf(&b, "checks: %s\n", sum)
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s: %s\n", c.Name, c.State)
	}
	return b.String()
}

func (s *Server) prMerge(ctx context.Context, repos []store.RepoRef, rootID, args string) string {
	name, number, ok := parsePRRef(args)
	if !ok || number == 0 {
		return `unire pr-merge: name the pull request, like "owner/repo#12".`
	}
	ref, msg := pickRepo(repos, name)
	if msg != "" {
		return msg
	}
	prs, m, err := s.prAPI(ref)
	if err != nil {
		return err.Error()
	}
	p, err := prs.GetPR(ctx, ref.FullName, number)
	if err != nil {
		return fmt.Sprintf("could not read %s#%d: %v", ref.FullName, number, err)
	}
	label := fmt.Sprintf("%s#%d", ref.FullName, number)
	switch {
	case p.State != gitprovider.PROpen:
		return fmt.Sprintf("Not merged: %s is %s.", label, p.State)
	case p.Draft:
		return fmt.Sprintf("Not merged: %s is a draft.", label)
	case p.HeadSHA == "":
		return fmt.Sprintf("Not merged: could not tell which commit %s is on.", label)
	}
	checks, err := prs.Checks(ctx, ref.FullName, p)
	if err != nil {
		return fmt.Sprintf("Not merged: could not read the checks of %s: %v", label, err)
	}
	state, sum := checksSummary(checks)
	if state != gitprovider.CheckSuccess {
		reason := sum
		if state == "none" {
			reason = "it has no checks, so Unire cannot tell that it works (the owner can merge it by hand)"
		}
		return fmt.Sprintf("Not merged: %s: %s.", label, reason)
	}
	if err := m.MergePR(ctx, ref.FullName, number, p.HeadSHA); err != nil {
		return fmt.Sprintf("Not merged: the git server said no for %s: %v", label, err)
	}
	if thread, err := s.threadOf(rootID); err == nil && len(thread) > 0 {
		_ = s.store.AddEvent(thread[len(thread)-1].ID, "log", fmt.Sprintf("merged %s (%s): %s", label, sum, p.Title))
	}
	return fmt.Sprintf("Merged %s: %s (%s).", label, p.Title, sum)
}
