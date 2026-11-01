package store

import (
	"database/sql"
	"errors"
	"time"
)

// Repo 是考生仓库的服务器侧记录。
type Repo struct {
	ID            int64
	CandidateID   int64
	URL           string
	DefaultBranch string
	FinalCommit   string
	LastPushAt    time.Time
	Archived      bool
}

// PushEvent 是一次推送记录。
type PushEvent struct {
	ID           int64
	CandidateID  int64
	Ref          string
	CommitSHA    string
	ReceivedAt   time.Time
	LinesAdded   int
	LinesDeleted int
	FilesChanged int
	Source       string
}

// JudgeResult 是一个考生一道题的判题结果。
type JudgeResult struct {
	ID          int64
	CandidateID int64
	ProblemID   int64
	VariantID   int64
	JudgedAt    time.Time
	Status      string
	Passed      int
	Total       int
	Score       int
	DetailJSON  string
}

// UpsertRepo 写入或更新考生仓库地址。
func (s *Store) UpsertRepo(candidateID int64, url string) error {
	_, err := s.db.Exec(`INSERT INTO repo (candidate_id,url,default_branch) VALUES (?,?,'main')
		ON CONFLICT(candidate_id) DO UPDATE SET url=excluded.url`, candidateID, url)
	return err
}

// GetRepoByCandidate 取考生仓库记录。
func (s *Store) GetRepoByCandidate(candidateID int64) (*Repo, error) {
	row := s.db.QueryRow(`SELECT id,candidate_id,url,default_branch,final_commit,last_push_at,archived FROM repo WHERE candidate_id=?`, candidateID)
	r := &Repo{}
	var archived int
	var lastPush int64
	if err := row.Scan(&r.ID, &r.CandidateID, &r.URL, &r.DefaultBranch, &r.FinalCommit, &lastPush, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.LastPushAt = fromMS(lastPush)
	r.Archived = archived != 0
	return r, nil
}

// SetRepoFinalCommit 记录冻结时的最终提交。
func (s *Store) SetRepoFinalCommit(candidateID int64, sha string) error {
	_, err := s.db.Exec(`UPDATE repo SET final_commit=? WHERE candidate_id=?`, sha, candidateID)
	return err
}

// MarkRepoArchived 标记仓库已归档。
func (s *Store) MarkRepoArchived(candidateID int64) error {
	_, err := s.db.Exec(`UPDATE repo SET archived=1 WHERE candidate_id=?`, candidateID)
	return err
}

// FinalizeExam 用每个考生最后一次推送的 commit 补齐缺失的 final_commit。
func (s *Store) FinalizeExam(examID string) (int64, error) {
	res, err := s.db.Exec(`UPDATE repo SET final_commit=COALESCE(
			(SELECT e.commit_sha FROM push_event e
			 WHERE e.candidate_id=repo.candidate_id
			 ORDER BY e.received_at DESC, e.id DESC LIMIT 1), final_commit)
		WHERE final_commit='' AND candidate_id IN (SELECT id FROM candidate WHERE exam_id=?)`, examID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AddPushEvent 写入或合并推送记录（同一 commit 只保留一条）。
func (s *Store) AddPushEvent(e *PushEvent) error {
	return s.UpsertPushEvent(e)
}

// UpsertPushEvent 按 (考生, commit_sha) 去重写入；后到的统计会补全行数。
func (s *Store) UpsertPushEvent(e *PushEvent) error {
	if e.Source == "" {
		e.Source = "webhook"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		id                       int64
		linesAdded, linesDeleted int
		filesChanged             int
	)
	err = tx.QueryRow(`SELECT id,lines_added,lines_deleted,files_changed FROM push_event
		WHERE candidate_id=? AND commit_sha=? ORDER BY id DESC LIMIT 1`, e.CandidateID, e.CommitSHA).
		Scan(&id, &linesAdded, &linesDeleted, &filesChanged)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO push_event (candidate_id,ref,commit_sha,received_at,lines_added,lines_deleted,files_changed,source)
			VALUES (?,?,?,?,?,?,?,?)`,
			e.CandidateID, e.Ref, e.CommitSHA, toMS(e.ReceivedAt), e.LinesAdded, e.LinesDeleted, e.FilesChanged, e.Source); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// 统计值只增不减：webhook 先到时行数为 0，post-receive 后到补全。
		if e.LinesAdded > linesAdded {
			linesAdded = e.LinesAdded
		}
		if e.LinesDeleted > linesDeleted {
			linesDeleted = e.LinesDeleted
		}
		if e.FilesChanged > filesChanged {
			filesChanged = e.FilesChanged
		}
		if _, err := tx.Exec(`UPDATE push_event SET lines_added=?,lines_deleted=?,files_changed=? WHERE id=?`,
			linesAdded, linesDeleted, filesChanged, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE repo SET last_push_at=? WHERE candidate_id=? AND last_push_at<?`,
		toMS(e.ReceivedAt), e.CandidateID, toMS(e.ReceivedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

// CountPushEvents 统计考生推送次数。
func (s *Store) CountPushEvents(candidateID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM push_event WHERE candidate_id=?`, candidateID).Scan(&n)
	return n, err
}

// LastPushEvent 返回考生最后一次推送。
func (s *Store) LastPushEvent(candidateID int64) (*PushEvent, error) {
	row := s.db.QueryRow(`SELECT id,candidate_id,ref,commit_sha,received_at,lines_added,lines_deleted,files_changed,source
		FROM push_event WHERE candidate_id=? ORDER BY received_at DESC, id DESC LIMIT 1`, candidateID)
	e := &PushEvent{}
	var received int64
	if err := row.Scan(&e.ID, &e.CandidateID, &e.Ref, &e.CommitSHA, &received,
		&e.LinesAdded, &e.LinesDeleted, &e.FilesChanged, &e.Source); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.ReceivedAt = fromMS(received)
	return e, nil
}

// ReplaceJudgeResults 覆盖写入某考生的全部判题结果。
func (s *Store) ReplaceJudgeResults(candidateID int64, results []JudgeResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM judge_result WHERE candidate_id=?`, candidateID); err != nil {
		return err
	}
	for _, r := range results {
		if r.JudgedAt.IsZero() {
			r.JudgedAt = time.Now()
		}
		if _, err := tx.Exec(`INSERT INTO judge_result (candidate_id,problem_id,variant_id,judged_at,status,passed,total,score,detail_json)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			r.CandidateID, r.ProblemID, r.VariantID, toMS(r.JudgedAt), r.Status,
			r.Passed, r.Total, r.Score, r.DetailJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListJudgeResults 返回场次全部判题结果。
func (s *Store) ListJudgeResults(examID string) ([]JudgeResult, error) {
	rows, err := s.db.Query(`SELECT j.id,j.candidate_id,j.problem_id,j.variant_id,j.judged_at,j.status,j.passed,j.total,j.score,j.detail_json
		FROM judge_result j JOIN candidate c ON c.id=j.candidate_id WHERE c.exam_id=? ORDER BY j.candidate_id,j.problem_id`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JudgeResult
	for rows.Next() {
		var r JudgeResult
		var judged int64
		if err := rows.Scan(&r.ID, &r.CandidateID, &r.ProblemID, &r.VariantID, &judged,
			&r.Status, &r.Passed, &r.Total, &r.Score, &r.DetailJSON); err != nil {
			return nil, err
		}
		r.JudgedAt = fromMS(judged)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AcquireJudgeLock 抢占一次判题任务；staleAfter 之前的锁视为过期。
func (s *Store) AcquireJudgeLock(scope, runID string, staleAfter time.Duration) (bool, error) {
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO judge_lock (scope,run_id,started_at) VALUES (?,?,?)
		ON CONFLICT(scope) DO UPDATE SET run_id=excluded.run_id, started_at=excluded.started_at
		WHERE judge_lock.started_at < ?`,
		scope, runID, toMS(now), toMS(now.Add(-staleAfter)))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseJudgeLock 释放自己持有的判题锁。
func (s *Store) ReleaseJudgeLock(scope, runID string) error {
	_, err := s.db.Exec(`DELETE FROM judge_lock WHERE scope=? AND run_id=?`, scope, runID)
	return err
}

// CountCandidates 返回场次考生数。
func (s *Store) CountCandidates(examID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM candidate WHERE exam_id=?`, examID).Scan(&n)
	return n, err
}
