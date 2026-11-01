package store

import (
	"database/sql"
	"errors"
	"time"
)

// AgentEvent 是 agent 上报的一条运行事件。
type AgentEvent struct {
	ID          int64
	CandidateID int64
	T           time.Time
	Type        string
	PayloadJSON string
}

// AddAgentEvent 写入 agent 事件。
func (s *Store) AddAgentEvent(candidateID int64, t time.Time, typ, payloadJSON string) error {
	if t.IsZero() {
		t = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO agent_event (candidate_id,t,type,payload_json) VALUES (?,?,?,?)`,
		candidateID, toMS(t), typ, payloadJSON)
	return err
}

// LastAgentEvent 返回考生最近一条 agent 事件。
func (s *Store) LastAgentEvent(candidateID int64) (*AgentEvent, error) {
	row := s.db.QueryRow(`SELECT id,candidate_id,t,type,payload_json FROM agent_event
		WHERE candidate_id=? ORDER BY t DESC, id DESC LIMIT 1`, candidateID)
	e := &AgentEvent{}
	var t int64
	if err := row.Scan(&e.ID, &e.CandidateID, &t, &e.Type, &e.PayloadJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.T = fromMS(t)
	return e, nil
}
