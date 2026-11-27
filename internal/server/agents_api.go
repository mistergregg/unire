package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/terminal"
)

// TerminalFactory builds the way to start a login terminal for a sandbox
// setting. Tests replace it.
type TerminalFactory func(Sandbox) terminal.Starter

// dockerTerminal runs the agent's own login command in a throwaway container
// on a pseudo-terminal.
func dockerTerminal(sb Sandbox) terminal.Starter {
	d := runner.Docker{Image: sb.Image, CredsVolume: sb.CredsVolume}
	return func(argv []string, cols, rows uint16) (terminal.Process, error) {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		name := "unire-login-" + hex.EncodeToString(b)
		d.Refresh(context.Background()) // an old image on this host may lack the agent
		cmd := exec.Command("docker", d.LoginTerminalArgs(name, argv)...)
		return terminal.StartCmd(cmd, cols, rows, func() {
			// Closing the terminal kills the docker client; remove the container it left.
			_ = exec.Command("docker", "rm", "-f", name).Run()
		})
	}
}

// ---- login status ----

type statusEntry struct {
	known    bool
	loggedIn bool
	note     string
	at       time.Time
}

type statusCache struct {
	mu sync.Mutex
	m  map[string]statusEntry
}

const statusTTL = 2 * time.Minute

func (s *Server) invalidateStatus() {
	s.signins.mu.Lock()
	s.signins.m = map[string]statusEntry{}
	s.signins.mu.Unlock()
	s.models.invalidate()
}

// agentStatus asks an agent whether it is signed in, using a short-lived
// sandbox with no network. A failure to ask is reported as unknown, never as
// signed out.
func (s *Server) agentStatus(ctx context.Context, ad agent.Adapter) statusEntry {
	info := ad.Info()
	if len(info.StatusArgv) == 0 {
		return statusEntry{note: "this agent has no way to report its sign-in"}
	}
	s.signins.mu.Lock()
	if e, ok := s.signins.m[info.ID]; ok && time.Since(e.at) < statusTTL {
		s.signins.mu.Unlock()
		return e
	}
	s.signins.mu.Unlock()

	e := statusEntry{at: time.Now()}
	if one, ok := s.runnerFactory(s.sandbox()).(runner.OneShot); !ok {
		e.note = "cannot check from here"
	} else {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, okExit, err := one.RunOnce(cctx, info.StatusArgv)
		if err != nil {
			e.note = "could not ask Docker: " + shorten(err.Error(), 120)
		} else {
			e.known, e.loggedIn = true, info.LoggedIn(out, okExit)
		}
	}
	if e.known || e.note != "" {
		s.signins.mu.Lock()
		s.signins.m[info.ID] = e
		s.signins.mu.Unlock()
	}
	return e
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (s *Server) agentDefaultModel(agentID string) string {
	if v, ok, _ := s.store.Setting("agent_model_" + agentID); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (s *Server) setAgentDefaultModel(agentID, model string) error {
	return s.store.SetSetting("agent_model_"+agentID, model)
}

func (s *Server) putAgentModel(w http.ResponseWriter, r *http.Request) {
	ad, err := agent.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such agent")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	model := strings.TrimSpace(req.Model)
	if model != "" {
		if err := agent.ValidateModel(model); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	id := ad.Info().ID
	if err := s.setAgentDefaultModel(id, model); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": id, "model": model})
}

type agentView struct {
	agent.Info
	LoggedIn        *bool             `json:"loggedIn"` // null when unknown
	StatusNote      string            `json:"statusNote,omitempty"`
	Usage           *agent.AgentUsage `json:"usage,omitempty"`
	ModelsUpdatedAt int64             `json:"modelsUpdatedAt,omitempty"`
	ModelsNote      string            `json:"modelsNote,omitempty"`
	DefaultModel    string            `json:"defaultModel,omitempty"`
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	infos := agent.Infos()
	out := make([]agentView, len(infos))
	var wg sync.WaitGroup
	for i, in := range infos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer safe.Recover("agent status", nil)
			ad, _ := agent.Get(in.ID)
			st := s.agentStatus(r.Context(), ad)
			models, updatedAt, modelsNote := s.modelsMetaFor(in)
			in.Models = models
			u := s.usageFor(in.ID)
			v := agentView{
				Info:            in,
				StatusNote:      st.note,
				Usage:           &u,
				ModelsUpdatedAt: updatedAt,
				ModelsNote:      modelsNote,
				DefaultModel:    s.agentDefaultModel(in.ID),
			}
			if st.known {
				v.LoggedIn = &st.loggedIn
			}
			out[i] = v
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// agentTool runs an agent's own diagnostic or reset command in a throwaway
// sandbox with no network. The commands are fixed by the agent's adapter; the
// browser sends nothing but the agent's id.
func (s *Server) agentTool(reset bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ad, err := agent.Get(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, "no such agent")
			return
		}
		info := ad.Info()
		argv := info.InspectArgv
		if reset {
			argv = info.ResetArgv
		}
		if len(argv) == 0 {
			writeErr(w, http.StatusBadRequest, info.Label+" has nothing to "+map[bool]string{false: "show", true: "reset"}[reset])
			return
		}
		one, ok := s.runnerFactory(s.sandbox()).(runner.OneShot)
		if !ok {
			writeErr(w, http.StatusBadRequest, "this cannot be done from here")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		out, okExit, err := one.RunOnce(ctx, argv)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "could not ask Docker: "+shorten(err.Error(), 120))
			return
		}
		if reset {
			s.invalidateStatus() // the sign-in state has changed
		}
		writeJSON(w, http.StatusOK, map[string]any{"output": shorten(strings.TrimSpace(out), 4000), "ok": okExit})
	}
}

// lastSignin returns what the agent's most recent sign-in terminal showed, as
// plain text with codes, links and email addresses removed.
func (s *Server) lastSignin(w http.ResponseWriter, r *http.Request) {
	ad, err := agent.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such agent")
		return
	}
	text, at, ok := s.transcripts.get(ad.Info().ID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no sign-in has been opened since Unire started")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": text, "startedAt": at.Unix()})
}

// recheckAgents forgets cached sign-in answers (after a sign-in terminal closes).
func (s *Server) recheckAgents(w http.ResponseWriter, r *http.Request) {
	s.invalidateStatus()
	s.listAgents(w, r)
}

// ---- login terminal ----

type termControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func parseSize(r *http.Request) (cols, rows uint16) {
	get := func(key string, def, min, max int) uint16 {
		n, err := strconv.Atoi(r.URL.Query().Get(key))
		if err != nil || n < min || n > max {
			return uint16(def)
		}
		return uint16(n)
	}
	return get("cols", 100, 20, 300), get("rows", 30, 5, 100)
}

// loginTerminal runs an agent's own sign-in command and connects it to the
// browser over a websocket. Binary messages carry terminal bytes both ways; a
// text message {"type":"resize","cols":N,"rows":M} resizes it.
//
// Only the owner can open it (the login cookie), a cross-site page cannot
// (websocket.Accept refuses an Origin that differs from the Host), only the
// agent's fixed sign-in command ever runs (the browser cannot choose a
// command), and only one terminal is open at a time.
func (s *Server) loginTerminal(w http.ResponseWriter, r *http.Request) {
	ad, err := agent.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such agent")
		return
	}
	if !s.termBusy.CompareAndSwap(false, true) {
		writeErr(w, http.StatusConflict, "another sign-in terminal is already open; close it first")
		return
	}
	defer s.termBusy.Store(false)

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has already answered, for example with 403 for a foreign Origin
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)

	cols, rows := parseSize(r)
	proc, err := s.terminalFor(s.sandbox())(ad.Info().LoginArgv, cols, rows)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, shorten("could not start the sign-in: "+err.Error(), 100))
		return
	}
	defer func() { _ = proc.Close(); s.invalidateStatus() }()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	id := ad.Info().ID
	s.transcripts.start(id)
	go func() { // command output to the browser
		defer safe.Recover("terminal output", nil)
		buf := make([]byte, 4096)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				s.transcripts.add(id, buf[:n]) // kept (cleaned) for "Show last sign-in output"
				if c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				_ = c.Close(websocket.StatusNormalClosure, "the command finished")
				return
			}
		}
	}()

	for { // the browser's keystrokes and resizes to the command
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := proc.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var ctl termControl
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" && ctl.Cols >= 20 && ctl.Cols <= 300 && ctl.Rows >= 5 && ctl.Rows <= 100 {
				_ = proc.Resize(ctl.Cols, ctl.Rows)
			}
		}
	}
}
