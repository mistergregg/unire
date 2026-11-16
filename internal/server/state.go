package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mistergregg/unire/internal/session"
)

// GitServer is a configured git server. It holds no secret: the token lives
// in the vault under TokenSecret.
type GitServer struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Type               string `json:"type"`
	BaseURL            string `json:"baseUrl"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	TokenSecret        string `json:"tokenSecret"`
}

type stateData struct {
	AdminHash string      `json:"adminHash"`
	Servers   []GitServer `json:"servers"`
}

// state is Unire's non-secret configuration, saved as JSON.
type state struct {
	mu   sync.Mutex
	path string
	d    stateData
}

func openState(dir string) (*state, error) {
	s := &state{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("state.json is corrupt: %w", err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	return s, nil
}

func (s *state) saveLocked() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *state) adminHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.AdminHash
}

func (s *state) setAdminHash(h string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.AdminHash = h
	return s.saveLocked()
}

func (s *state) servers() []GitServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GitServer(nil), s.d.Servers...)
}

func (s *state) server(id string) (GitServer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.d.Servers {
		if g.ID == id {
			return g, true
		}
	}
	return GitServer{}, false
}

// addServer assigns a unique ID from the name and saves the server.
func (s *state) addServer(g GitServer) (GitServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := session.Slug(g.Name)
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, e := range s.d.Servers {
			if e.ID == id {
				taken = true
			}
		}
		if !taken {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	g.ID = id
	g.TokenSecret = "git-server-" + id
	s.d.Servers = append(s.d.Servers, g)
	return g, s.saveLocked()
}

func (s *state) removeServer(id string) (GitServer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, g := range s.d.Servers {
		if g.ID == id {
			s.d.Servers = append(s.d.Servers[:i:i], s.d.Servers[i+1:]...)
			return g, true, s.saveLocked()
		}
	}
	return GitServer{}, false, nil
}
