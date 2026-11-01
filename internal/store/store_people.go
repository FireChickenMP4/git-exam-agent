package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Admin 是管理端账号。
type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	Disabled     bool
	CreatedAt    time.Time
}

// Candidate 是考生。
type Candidate struct {
	ID        int64
	ExamID    string
	TrackID   int64
	Track     string
	StudentNo string
	Name      string
	GiteaUser string
	RepoName  string
	TokenHash string
	Variant   string
	Status    string
	CreatedAt time.Time
}

// Notice 是公告。
type Notice struct {
	ID       int64
	ExamID   string
	T        time.Time
	Content  string
	AuthorID int64
}

// LiveRow 是监考看板的一行。
type LiveRow struct {
	CandidateID   int64
	StudentNo     string
	Name          string
	Track         string
	Status        string
	RepoURL       string
	FinalCommit   string
	LastPushAt    time.Time
	PushCount     int
	Archived      bool
	LastAgentAt   time.Time
	LastAgentType string
}

// CreateAdmin 新建管理员。
func (s *Store) CreateAdmin(username, passwordHash string) error {
	_, err := s.db.Exec(`INSERT INTO admin (username,password_hash,role,disabled,created_at) VALUES (?,?, 'admin', 0, ?)`,
		username, passwordHash, toMS(time.Now()))
	if err != nil {
		return fmt.Errorf("创建管理员 %s: %w", username, err)
	}
	return nil
}

// GetAdmin 按用户名取管理员。
func (s *Store) GetAdmin(username string) (*Admin, error) {
	row := s.db.QueryRow(`SELECT id,username,password_hash,role,disabled,created_at FROM admin WHERE username=?`, username)
	a := &Admin{}
	var disabled int
	var created int64
	if err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &disabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Disabled = disabled != 0
	a.CreatedAt = fromMS(created)
	return a, nil
}

// HasAdmin 判断是否已有管理员账号。
func (s *Store) HasAdmin() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// EnsureTrack 返回赛道 id，不存在则创建。
func (s *Store) EnsureTrack(examID, name string) (int64, error) {
	if name == "" {
		name = "default"
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM track WHERE exam_id=? AND name=?`, examID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err := s.db.Exec(`INSERT INTO track (exam_id,name,sort) VALUES (?,?,0) ON CONFLICT DO NOTHING`, examID, name); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`SELECT id FROM track WHERE exam_id=? AND name=?`, examID, name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// UpsertCandidate 按 (场次, 学号) 覆盖写入考生。
func (s *Store) UpsertCandidate(c *Candidate) (int64, error) {
	if c.Variant == "" {
		c.Variant = "default"
	}
	if c.Status == "" {
		c.Status = "draft"
	}
	trackID := c.TrackID
	if trackID == 0 {
		var err error
		if trackID, err = s.EnsureTrack(c.ExamID, c.Track); err != nil {
			return 0, err
		}
	}
	now := toMS(time.Now())
	_, err := s.db.Exec(`INSERT INTO candidate (exam_id,track_id,student_no,name,variant,status,created_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(exam_id,student_no) DO UPDATE SET
			name=excluded.name, track_id=excluded.track_id, variant=excluded.variant`,
		c.ExamID, trackID, c.StudentNo, c.Name, c.Variant, c.Status, now)
	if err != nil {
		return 0, fmt.Errorf("写入考生 %s: %w", c.StudentNo, err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM candidate WHERE exam_id=? AND student_no=?`, c.ExamID, c.StudentNo).Scan(&id); err != nil {
		return 0, err
	}
	c.ID = id
	c.TrackID = trackID
	return id, nil
}

// ListCandidates 返回场次全部考生。
func (s *Store) ListCandidates(examID string) ([]Candidate, error) {
	rows, err := s.db.Query(`SELECT c.id,c.exam_id,c.track_id,COALESCE(t.name,''),c.student_no,c.name,
		c.gitea_user,c.repo_name,c.token_hash,c.variant,c.status,c.created_at
		FROM candidate c LEFT JOIN track t ON t.id=c.track_id
		WHERE c.exam_id=? ORDER BY c.student_no`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var created int64
		if err := rows.Scan(&c.ID, &c.ExamID, &c.TrackID, &c.Track, &c.StudentNo, &c.Name,
			&c.GiteaUser, &c.RepoName, &c.TokenHash, &c.Variant, &c.Status, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = fromMS(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCandidate 按 id 取考生。
func (s *Store) GetCandidate(id int64) (*Candidate, error) {
	row := s.db.QueryRow(`SELECT c.id,c.exam_id,c.track_id,COALESCE(t.name,''),c.student_no,c.name,
		c.gitea_user,c.repo_name,c.token_hash,c.variant,c.status,c.created_at
		FROM candidate c LEFT JOIN track t ON t.id=c.track_id WHERE c.id=?`, id)
	c := &Candidate{}
	var created int64
	if err := row.Scan(&c.ID, &c.ExamID, &c.TrackID, &c.Track, &c.StudentNo, &c.Name,
		&c.GiteaUser, &c.RepoName, &c.TokenHash, &c.Variant, &c.Status, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt = fromMS(created)
	return c, nil
}

// FindCandidateByStudentNo 找到学号对应的考生及其场次（优先未关闭场次）。
func (s *Store) FindCandidateByStudentNo(studentNo string) (*Candidate, *Exam, error) {
	row := s.db.QueryRow(`SELECT c.id,c.exam_id,c.track_id,COALESCE(t.name,''),c.student_no,c.name,
		c.gitea_user,c.repo_name,c.token_hash,c.variant,c.status,c.created_at,
		e.id,e.title,e.type,e.status,e.starts_at,e.ends_at,e.timezone,e.created_at
		FROM candidate c
		JOIN exam e ON e.id=c.exam_id
		LEFT JOIN track t ON t.id=c.track_id
		WHERE c.student_no=? ORDER BY (e.status='closed') ASC, e.starts_at DESC LIMIT 1`, studentNo)
	c := &Candidate{}
	e := &Exam{}
	var created, starts, ends, examCreated int64
	if err := row.Scan(&c.ID, &c.ExamID, &c.TrackID, &c.Track, &c.StudentNo, &c.Name,
		&c.GiteaUser, &c.RepoName, &c.TokenHash, &c.Variant, &c.Status, &created,
		&e.ID, &e.Title, &e.Type, &e.Status, &starts, &ends, &e.Timezone, &examCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	c.CreatedAt = fromMS(created)
	e.StartsAt, e.EndsAt, e.CreatedAt = fromMS(starts), fromMS(ends), fromMS(examCreated)
	return c, e, nil
}

// CandidateByRepo 按 Gitea owner/repo 找考生。
func (s *Store) CandidateByRepo(owner, repoName string) (*Candidate, error) {
	row := s.db.QueryRow(`SELECT c.id,c.exam_id,c.track_id,COALESCE(t.name,''),c.student_no,c.name,
		c.gitea_user,c.repo_name,c.token_hash,c.variant,c.status,c.created_at
		FROM candidate c LEFT JOIN track t ON t.id=c.track_id
		WHERE c.gitea_user=? AND c.repo_name=? ORDER BY c.id DESC LIMIT 1`, owner, repoName)
	c := &Candidate{}
	var created int64
	if err := row.Scan(&c.ID, &c.ExamID, &c.TrackID, &c.Track, &c.StudentNo, &c.Name,
		&c.GiteaUser, &c.RepoName, &c.TokenHash, &c.Variant, &c.Status, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt = fromMS(created)
	return c, nil
}

// SetCandidateToken 保存令牌哈希并把状态推进到 ready。
func (s *Store) SetCandidateToken(id int64, tokenHash string) error {
	_, err := s.db.Exec(`UPDATE candidate SET token_hash=?, status='ready' WHERE id=?`, tokenHash, id)
	return err
}

// SetCandidateRepo 记录考生仓库名与用户名。
func (s *Store) SetCandidateRepo(id int64, giteaUser, repoName string) error {
	_, err := s.db.Exec(`UPDATE candidate SET gitea_user=?, repo_name=? WHERE id=?`, giteaUser, repoName, id)
	return err
}

// SetCandidateStatus 更新考生状态。
func (s *Store) SetCandidateStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE candidate SET status=? WHERE id=?`, status, id)
	return err
}

func sessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateCandidateSession 保存考生会话（只存哈希）。
func (s *Store) CreateCandidateSession(candidateID int64, token string, expires time.Time, ip, ua string) error {
	_, err := s.db.Exec(`INSERT INTO candidate_session (id,candidate_id,created_at,expires_at,ip,ua) VALUES (?,?,?,?,?,?)`,
		sessionID(token), candidateID, toMS(time.Now()), toMS(expires), ip, ua)
	return err
}

// CandidateBySession 用会话令牌取考生及场次。
func (s *Store) CandidateBySession(token string) (*Candidate, *Exam, error) {
	now := toMS(time.Now())
	row := s.db.QueryRow(`SELECT c.id,c.exam_id,c.track_id,COALESCE(t.name,''),c.student_no,c.name,
		c.gitea_user,c.repo_name,c.token_hash,c.variant,c.status,c.created_at,
		e.id,e.title,e.type,e.status,e.starts_at,e.ends_at,e.timezone,e.created_at
		FROM candidate_session s
		JOIN candidate c ON c.id=s.candidate_id
		JOIN exam e ON e.id=c.exam_id
		LEFT JOIN track t ON t.id=c.track_id
		WHERE s.id=? AND s.expires_at>?`, sessionID(token), now)
	c := &Candidate{}
	e := &Exam{}
	var created, starts, ends, examCreated int64
	if err := row.Scan(&c.ID, &c.ExamID, &c.TrackID, &c.Track, &c.StudentNo, &c.Name,
		&c.GiteaUser, &c.RepoName, &c.TokenHash, &c.Variant, &c.Status, &created,
		&e.ID, &e.Title, &e.Type, &e.Status, &starts, &ends, &e.Timezone, &examCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	c.CreatedAt = fromMS(created)
	e.StartsAt, e.EndsAt, e.CreatedAt = fromMS(starts), fromMS(ends), fromMS(examCreated)
	return c, e, nil
}

// CreateAdminSession 保存管理端会话。
func (s *Store) CreateAdminSession(adminID int64, token string, expires time.Time, ip, ua string) error {
	_, err := s.db.Exec(`INSERT INTO admin_session (id,admin_id,created_at,expires_at,ip,ua) VALUES (?,?,?,?,?,?)`,
		sessionID(token), adminID, toMS(time.Now()), toMS(expires), ip, ua)
	return err
}

// AdminBySession 用会话令牌取管理员。
func (s *Store) AdminBySession(token string) (*Admin, error) {
	row := s.db.QueryRow(`SELECT a.id,a.username,a.password_hash,a.role,a.disabled,a.created_at
		FROM admin_session s JOIN admin a ON a.id=s.admin_id
		WHERE s.id=? AND s.expires_at>? AND a.disabled=0`, sessionID(token), toMS(time.Now()))
	a := &Admin{}
	var disabled int
	var created int64
	if err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &disabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Disabled = disabled != 0
	a.CreatedAt = fromMS(created)
	return a, nil
}

// DeleteSession 删除会话（登出）。
func (s *Store) DeleteSession(table, token string) error {
	_, err := s.db.Exec(`DELETE FROM `+table+` WHERE id=?`, sessionID(token))
	return err
}

// PurgeSessions 清理过期会话。
func (s *Store) PurgeSessions(now time.Time) error {
	if _, err := s.db.Exec(`DELETE FROM candidate_session WHERE expires_at<=?`, toMS(now)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM admin_session WHERE expires_at<=?`, toMS(now))
	return err
}

// AcceptConsent 记录考生确认。
func (s *Store) AcceptConsent(candidateID int64, version, ip string) error {
	_, err := s.db.Exec(`INSERT INTO consent (candidate_id,version,accepted_at,ip) VALUES (?,?,?,?)
		ON CONFLICT(candidate_id,version) DO NOTHING`, candidateID, version, toMS(time.Now()), ip)
	return err
}

// HasConsent 判断考生是否已确认某版本。
func (s *Store) HasConsent(candidateID int64, version string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM consent WHERE candidate_id=? AND version=?`, candidateID, version).Scan(&n)
	return n > 0, err
}

// AddNotice 发布公告。
func (s *Store) AddNotice(examID, content string, authorID int64) error {
	_, err := s.db.Exec(`INSERT INTO notice (exam_id,t,content,author_id) VALUES (?,?,?,?)`,
		examID, toMS(time.Now()), content, authorID)
	return err
}

// ListNotices 返回场次公告（新的在前）。
func (s *Store) ListNotices(examID string) ([]Notice, error) {
	rows, err := s.db.Query(`SELECT id,exam_id,t,content,author_id FROM notice WHERE exam_id=? ORDER BY t DESC`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notice
	for rows.Next() {
		var n Notice
		var t int64
		if err := rows.Scan(&n.ID, &n.ExamID, &t, &n.Content, &n.AuthorID); err != nil {
			return nil, err
		}
		n.T = fromMS(t)
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddAudit 记录管理操作。
func (s *Store) AddAudit(examID, actorType, actorID, action, target, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit (exam_id,actor_type,actor_id,action,target,t,detail) VALUES (?,?,?,?,?,?,?)`,
		examID, actorType, actorID, action, target, toMS(time.Now()), detail)
	return err
}

// LiveRows 返回看板数据。
func (s *Store) LiveRows(examID string) ([]LiveRow, error) {
	rows, err := s.db.Query(`SELECT c.id,c.student_no,c.name,COALESCE(t.name,''),c.status,
		COALESCE(r.url,''),COALESCE(r.final_commit,''),COALESCE(r.last_push_at,0),COALESCE(r.archived,0),
		(SELECT COUNT(*) FROM push_event e WHERE e.candidate_id=c.id),
		(SELECT COALESCE(MAX(received_at),0) FROM push_event e WHERE e.candidate_id=c.id),
		(SELECT COALESCE(MAX(t),0) FROM agent_event e WHERE e.candidate_id=c.id),
		(SELECT COALESCE((SELECT type FROM agent_event e WHERE e.candidate_id=c.id ORDER BY t DESC, id DESC LIMIT 1),''))
		FROM candidate c
		LEFT JOIN track t ON t.id=c.track_id
		LEFT JOIN repo r ON r.candidate_id=c.id
		WHERE c.exam_id=? ORDER BY c.student_no`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveRow
	for rows.Next() {
		var r LiveRow
		var lastPush, maxPush, lastAgent int64
		var archived int
		if err := rows.Scan(&r.CandidateID, &r.StudentNo, &r.Name, &r.Track, &r.Status,
			&r.RepoURL, &r.FinalCommit, &lastPush, &archived, &r.PushCount, &maxPush,
			&lastAgent, &r.LastAgentType); err != nil {
			return nil, err
		}
		r.Archived = archived != 0
		r.LastPushAt = fromMS(lastPush)
		if maxPush > lastPush {
			r.LastPushAt = fromMS(maxPush)
		}
		r.LastAgentAt = fromMS(lastAgent)
		out = append(out, r)
	}
	return out, rows.Err()
}
