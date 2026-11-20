package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mistergregg/unire/internal/agent"
)

// SaveUsage records the latest usage limits for an agent.
func (s *Store) SaveUsage(u agent.AgentUsage) error {
	windowsJSON, err := json.Marshal(u.Windows)
	if err != nil {
		windowsJSON = []byte("[]")
	}
	updatedAt := u.UpdatedAt
	if updatedAt == 0 {
		updatedAt = s.now().Unix()
	}
	reportedInt := 0
	if u.Reported {
		reportedInt = 1
	}
	_, err = s.db.Exec(`
		INSERT INTO agent_usage (agent_id, reported, status, plan, windows, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			reported = excluded.reported,
			status = excluded.status,
			plan = excluded.plan,
			windows = excluded.windows,
			updated_at = excluded.updated_at
	`, u.AgentID, reportedInt, u.Status, u.Plan, string(windowsJSON), updatedAt)
	return err
}

// AgentUsage returns the latest usage reading for agentID, or ok=false if none recorded.
func (s *Store) AgentUsage(agentID string) (agent.AgentUsage, bool, error) {
	var (
		reportedInt int
		status      string
		plan        string
		windowsStr  string
		updatedAt   int64
	)
	err := s.db.QueryRow(`
		SELECT reported, status, plan, windows, updated_at
		FROM agent_usage
		WHERE agent_id = ?
	`, agentID).Scan(&reportedInt, &status, &plan, &windowsStr, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.AgentUsage{}, false, nil
	}
	if err != nil {
		return agent.AgentUsage{}, false, err
	}
	var windows []agent.UsageWindow
	if err := json.Unmarshal([]byte(windowsStr), &windows); err != nil {
		windows = []agent.UsageWindow{}
	}
	if windows == nil {
		windows = []agent.UsageWindow{}
	}
	return agent.AgentUsage{
		AgentID:   agentID,
		Reported:  reportedInt != 0,
		Status:    status,
		Plan:      plan,
		Windows:   windows,
		UpdatedAt: updatedAt,
	}, true, nil
}

// AllUsage returns the latest usage for all agents that have recorded one, keyed by agent_id.
func (s *Store) AllUsage() (map[string]agent.AgentUsage, error) {
	rows, err := s.db.Query(`SELECT agent_id, reported, status, plan, windows, updated_at FROM agent_usage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]agent.AgentUsage)
	for rows.Next() {
		var (
			agentID     string
			reportedInt int
			status      string
			plan        string
			windowsStr  string
			updatedAt   int64
		)
		if err := rows.Scan(&agentID, &reportedInt, &status, &plan, &windowsStr, &updatedAt); err != nil {
			return nil, err
		}
		var windows []agent.UsageWindow
		if err := json.Unmarshal([]byte(windowsStr), &windows); err != nil {
			windows = []agent.UsageWindow{}
		}
		if windows == nil {
			windows = []agent.UsageWindow{}
		}
		out[agentID] = agent.AgentUsage{
			AgentID:   agentID,
			Reported:  reportedInt != 0,
			Status:    status,
			Plan:      plan,
			Windows:   windows,
			UpdatedAt: updatedAt,
		}
	}
	return out, rows.Err()
}
