package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/store"
)

// An agent in any chat can propose an automation when the owner asks for something
// to happen regularly (the automation tool). Unire checks the proposal and puts it in
// the chat as an "automation" event, which the page shows as a card with Save and
// Edit. Nothing is created until the owner saves it: an automation keeps using the
// agent by itself, so one misread message must not start one. Merging is never
// turned on by a proposal.

// automationProposal is what the agent writes.
type automationProposal struct {
	Name         string   `json:"name"`
	Prompt       string   `json:"prompt"`
	Repos        []string `json:"repos"`
	DailyAt      string   `json:"daily_at"`
	EveryMinutes int      `json:"every_minutes"`
	MaxPerDay    int      `json:"max_per_day"`
	MaxUsage     *int     `json:"max_usage"`
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
}

const defaultProposalUsage = 80 // the page's default too

func (s *Server) proposeAutomation(ctx context.Context, rootID, args string) string {
	var p automationProposal
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(args))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "unire automation: the proposal must be one JSON object with the fields name, prompt, repos, daily_at or every_minutes, max_per_day, max_usage (" + err.Error() + ")."
	}
	thread, err := s.threadOf(rootID)
	if err != nil || len(thread) == 0 {
		return "unire automation: this chat could not be found."
	}
	last := thread[len(thread)-1]
	if p.Agent == "" {
		p.Agent = last.Agent // the agent the owner is talking to
	}
	in := automationIn{Name: p.Name, Prompt: p.Prompt, Agent: p.Agent, Model: p.Model, EveryMinutes: p.EveryMinutes,
		DailyAt: p.DailyAt, MaxPerDay: p.MaxPerDay, MaxUsage: defaultProposalUsage}
	if p.MaxUsage != nil {
		in.MaxUsage = *p.MaxUsage
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, name := range p.Repos {
		ref, msg := s.findRepoByName(rctx, thread, strings.TrimSpace(name))
		if msg != "" {
			return "unire automation: " + msg
		}
		in.Repos = append(in.Repos, ref)
	}
	a, err := automationFrom(in)
	if err != nil {
		return "unire automation: " + err.Error()
	}
	in.Name, in.DailyAt, in.EveryMinutes = a.Name, a.DailyAt, a.EveryMinutes // as tidied
	if in.Repos == nil {
		in.Repos = []store.RepoRef{}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return "unire automation: could not record the proposal."
	}
	if err := s.store.AddEvent(last.ID, "automation", string(body)); err != nil {
		return "unire automation: could not record the proposal."
	}
	when := scheduleText(a)
	if a.EveryMinutes == 0 {
		when += " (the owner's time)"
	}
	return fmt.Sprintf("ok: proposed the automation %q (%s). The owner sees a card in the chat and it is created only when they press Save; "+
		"merging pull requests stays off unless they turn it on. Tell them what you proposed and that they need to save it.", a.Name, when)
}

// findRepoByName resolves a repo's full name: one the chat already has, else one of
// the owner's repos on their git servers with exactly that name.
func (s *Server) findRepoByName(ctx context.Context, thread []store.Session, name string) (store.RepoRef, string) {
	if name == "" {
		return store.RepoRef{}, "a repo name is empty"
	}
	for _, t := range thread {
		for _, r := range t.Repos {
			if strings.EqualFold(r.FullName, name) {
				r.Base = ""
				return r, ""
			}
		}
	}
	for _, r := range s.reposFor(ctx, name) {
		if strings.EqualFold(r.FullName, name) {
			return store.RepoRef{ServerID: r.ServerID, FullName: r.FullName, CloneURL: r.CloneURL}, ""
		}
	}
	return store.RepoRef{}, fmt.Sprintf("no repo named %s was found on the owner's git servers (use the full name, like owner/repo)", name)
}
