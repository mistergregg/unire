package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// SaveModels records the latest discovered models for agentID.
func (s *Store) SaveModels(agentID string, models []string, updatedAt int64) error {
	if len(models) == 0 {
		return nil
	}
	b, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if updatedAt == 0 {
		updatedAt = s.now().Unix()
	}
	_, err = s.db.Exec(`
		INSERT INTO agent_models (agent_id, models, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			models = excluded.models,
			updated_at = excluded.updated_at
	`, agentID, string(b), updatedAt)
	return err
}

// AgentModels returns the latest discovered models for agentID, or ok=false if none recorded.
func (s *Store) AgentModels(agentID string) ([]string, int64, bool, error) {
	var (
		modelsStr string
		updatedAt int64
	)
	err := s.db.QueryRow(`
		SELECT models, updated_at
		FROM agent_models
		WHERE agent_id = ?
	`, agentID).Scan(&modelsStr, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	var models []string
	if err := json.Unmarshal([]byte(modelsStr), &models); err != nil || len(models) == 0 {
		return nil, 0, false, nil
	}
	return models, updatedAt, true, nil
}
