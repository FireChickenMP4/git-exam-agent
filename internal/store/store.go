// Package store 提供 SQLite 持久化。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 包装 SQLite 连接。
type Store struct {
	db *sql.DB
}

// Exam 对应 exam 表。
type Exam struct {
	ID        string
	Title     string
	Type      string
	Status    string
	StartsAt  time.Time
	EndsAt    time.Time
	Timezone  string
	CreatedAt time.Time
}

// Problem 是题目及其变体、用例。
type Problem struct {
	ID            int64
	ExamID        string
	Key           string
	Title         string
	StatementMD   string
	Score         int
	TimeLimitMS   int
	MemoryLimitMB int
	CreatedAt     time.Time
	Variants      []Variant
	Cases         []TestCase
}

// Variant 是某个题目的题面变体。
type Variant struct {
	ID          int64
	ProblemID   int64
	Seed        string
	StatementMD string
	AssetsDir   string
}

// TestCase 是一个判题用例。
type TestCase struct {
	ID           int64
	ProblemID    int64
	VariantID    int64
	CaseNo       int
	InputPath    string
	ExpectedPath string
	Score        int
	IsSample     bool
	Sort         int
}

const schema = `
CREATE TABLE IF NOT EXISTS admin (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	disabled INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_session (
	id TEXT PRIMARY KEY,
	admin_id INTEGER NOT NULL REFERENCES admin(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	ua TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS exam (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	type TEXT NOT NULL DEFAULT 'exam',
	status TEXT NOT NULL DEFAULT 'draft',
	starts_at INTEGER NOT NULL,
	ends_at INTEGER NOT NULL,
	timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS track (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	exam_id TEXT NOT NULL REFERENCES exam(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	sort INTEGER NOT NULL DEFAULT 0,
	UNIQUE(exam_id, name)
);

CREATE TABLE IF NOT EXISTS problem (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	exam_id TEXT NOT NULL REFERENCES exam(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	title TEXT NOT NULL,
	statement_md TEXT NOT NULL DEFAULT '',
	score INTEGER NOT NULL DEFAULT 0,
	time_limit_ms INTEGER NOT NULL DEFAULT 3000,
	memory_limit_mb INTEGER NOT NULL DEFAULT 256,
	created_at INTEGER NOT NULL,
	UNIQUE(exam_id, key)
);

CREATE TABLE IF NOT EXISTS variant (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	problem_id INTEGER NOT NULL REFERENCES problem(id) ON DELETE CASCADE,
	seed TEXT NOT NULL,
	statement_md TEXT NOT NULL DEFAULT '',
	assets_dir TEXT NOT NULL DEFAULT '',
	UNIQUE(problem_id, seed)
);

CREATE TABLE IF NOT EXISTS test_case (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	problem_id INTEGER NOT NULL REFERENCES problem(id) ON DELETE CASCADE,
	variant_id INTEGER NOT NULL DEFAULT 0,
	case_no INTEGER NOT NULL,
	input_path TEXT NOT NULL,
	expected_path TEXT NOT NULL,
	score INTEGER NOT NULL DEFAULT 0,
	is_sample INTEGER NOT NULL DEFAULT 0,
	sort INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS candidate (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	exam_id TEXT NOT NULL REFERENCES exam(id) ON DELETE CASCADE,
	track_id INTEGER NOT NULL DEFAULT 0,
	student_no TEXT NOT NULL,
	name TEXT NOT NULL,
	gitea_user TEXT NOT NULL DEFAULT '',
	repo_name TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL DEFAULT '',
	variant TEXT NOT NULL DEFAULT 'default',
	status TEXT NOT NULL DEFAULT 'draft',
	created_at INTEGER NOT NULL,
	UNIQUE(exam_id, student_no)
);

CREATE TABLE IF NOT EXISTS candidate_session (
	id TEXT PRIMARY KEY,
	candidate_id INTEGER NOT NULL REFERENCES candidate(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	ua TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS consent (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	candidate_id INTEGER NOT NULL REFERENCES candidate(id) ON DELETE CASCADE,
	version TEXT NOT NULL,
	accepted_at INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	UNIQUE(candidate_id, version)
);

CREATE TABLE IF NOT EXISTS repo (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	candidate_id INTEGER NOT NULL UNIQUE REFERENCES candidate(id) ON DELETE CASCADE,
	url TEXT NOT NULL,
	default_branch TEXT NOT NULL DEFAULT 'main',
	final_commit TEXT NOT NULL DEFAULT '',
	last_push_at INTEGER NOT NULL DEFAULT 0,
	archived INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS push_event (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	candidate_id INTEGER NOT NULL REFERENCES candidate(id) ON DELETE CASCADE,
	ref TEXT NOT NULL,
	commit_sha TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	lines_added INTEGER NOT NULL DEFAULT 0,
	lines_deleted INTEGER NOT NULL DEFAULT 0,
	files_changed INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'webhook'
);

CREATE TABLE IF NOT EXISTS agent_event (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	candidate_id INTEGER,
	t INTEGER NOT NULL,
	type TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS judge_result (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	candidate_id INTEGER NOT NULL REFERENCES candidate(id) ON DELETE CASCADE,
	problem_id INTEGER NOT NULL,
	variant_id INTEGER NOT NULL DEFAULT 0,
	judged_at INTEGER NOT NULL,
	status TEXT NOT NULL,
	passed INTEGER NOT NULL DEFAULT 0,
	total INTEGER NOT NULL DEFAULT 0,
	score INTEGER NOT NULL DEFAULT 0,
	detail_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS judge_lock (
	scope TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	started_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS notice (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	exam_id TEXT NOT NULL REFERENCES exam(id) ON DELETE CASCADE,
	t INTEGER NOT NULL,
	content TEXT NOT NULL,
	author_id INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	exam_id TEXT NOT NULL,
	actor_type TEXT NOT NULL,
	actor_id TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	t INTEGER NOT NULL,
	detail TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_candidate_exam ON candidate(exam_id);
CREATE INDEX IF NOT EXISTS idx_push_candidate ON push_event(candidate_id, received_at);
CREATE INDEX IF NOT EXISTS idx_judge_candidate ON judge_result(candidate_id);
CREATE INDEX IF NOT EXISTS idx_notice_exam ON notice(exam_id, t);
`

// Open 打开（必要时创建）数据库并建表。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据库目录: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化表结构: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，供备份使用。
func (s *Store) DB() *sql.DB { return s.db }

func toMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// CreateExam 新建场次。
func (s *Store) CreateExam(e *Exam) error {
	if e.Status == "" {
		e.Status = "draft"
	}
	if e.Type == "" {
		e.Type = "exam"
	}
	if e.Timezone == "" {
		e.Timezone = "Asia/Shanghai"
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO exam (id,title,type,status,starts_at,ends_at,timezone,created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		e.ID, e.Title, e.Type, e.Status, toMS(e.StartsAt), toMS(e.EndsAt), e.Timezone, toMS(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("创建场次 %s: %w", e.ID, err)
	}
	return nil
}

// GetExam 按 id 取场次。
func (s *Store) GetExam(id string) (*Exam, error) {
	row := s.db.QueryRow(`SELECT id,title,type,status,starts_at,ends_at,timezone,created_at FROM exam WHERE id=?`, id)
	e := &Exam{}
	var starts, ends, created int64
	if err := row.Scan(&e.ID, &e.Title, &e.Type, &e.Status, &starts, &ends, &e.Timezone, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.StartsAt, e.EndsAt, e.CreatedAt = fromMS(starts), fromMS(ends), fromMS(created)
	return e, nil
}

// ListExams 列出所有场次（新场次在前）。
func (s *Store) ListExams() ([]Exam, error) {
	rows, err := s.db.Query(`SELECT id,title,type,status,starts_at,ends_at,timezone,created_at FROM exam ORDER BY starts_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exam
	for rows.Next() {
		var e Exam
		var starts, ends, created int64
		if err := rows.Scan(&e.ID, &e.Title, &e.Type, &e.Status, &starts, &ends, &e.Timezone, &created); err != nil {
			return nil, err
		}
		e.StartsAt, e.EndsAt, e.CreatedAt = fromMS(starts), fromMS(ends), fromMS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetExamStatus 更新场次状态。
func (s *Store) SetExamStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE exam SET status=? WHERE id=?`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetExamTimes 更新起止时间。
func (s *Store) SetExamTimes(id string, starts, ends time.Time) error {
	_, err := s.db.Exec(`UPDATE exam SET starts_at=?, ends_at=? WHERE id=?`, toMS(starts), toMS(ends), id)
	return err
}

// UpsertProblem 按 (exam,key) 覆盖写入题目，保留 problem id，避免旧判题结果错位。
func (s *Store) UpsertProblem(p *Problem) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`SELECT id FROM problem WHERE exam_id=? AND key=?`, p.ExamID, p.Key).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now()
		}
		res, err := tx.Exec(`INSERT INTO problem (exam_id,key,title,statement_md,score,time_limit_ms,memory_limit_mb,created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			p.ExamID, p.Key, p.Title, p.StatementMD, p.Score, p.TimeLimitMS, p.MemoryLimitMB, toMS(p.CreatedAt))
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		if _, err := tx.Exec(`UPDATE problem SET title=?,statement_md=?,score=?,time_limit_ms=?,memory_limit_mb=? WHERE id=?`,
			p.Title, p.StatementMD, p.Score, p.TimeLimitMS, p.MemoryLimitMB, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM variant WHERE problem_id=?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM test_case WHERE problem_id=?`, id); err != nil {
			return 0, err
		}
	}

	for _, v := range p.Variants {
		if _, err := tx.Exec(`INSERT INTO variant (problem_id,seed,statement_md,assets_dir) VALUES (?,?,?,?)`,
			id, v.Seed, v.StatementMD, v.AssetsDir); err != nil {
			return 0, err
		}
	}
	for _, c := range p.Cases {
		if _, err := tx.Exec(`INSERT INTO test_case (problem_id,variant_id,case_no,input_path,expected_path,score,is_sample,sort)
			VALUES (?,?,?,?,?,?,?,?)`,
			id, c.VariantID, c.CaseNo, c.InputPath, c.ExpectedPath, c.Score, boolInt(c.IsSample), c.Sort); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// ListProblems 返回场次下全部题目，含变体和用例。
func (s *Store) ListProblems(examID string) ([]Problem, error) {
	rows, err := s.db.Query(`SELECT id,exam_id,key,title,statement_md,score,time_limit_ms,memory_limit_mb,created_at
		FROM problem WHERE exam_id=? ORDER BY key`, examID)
	if err != nil {
		return nil, err
	}
	var problems []Problem
	byID := map[int64]*Problem{}
	for rows.Next() {
		var p Problem
		var created int64
		if err := rows.Scan(&p.ID, &p.ExamID, &p.Key, &p.Title, &p.StatementMD, &p.Score,
			&p.TimeLimitMS, &p.MemoryLimitMB, &created); err != nil {
			rows.Close()
			return nil, err
		}
		p.CreatedAt = fromMS(created)
		problems = append(problems, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range problems {
		byID[problems[i].ID] = &problems[i]
	}

	vrows, err := s.db.Query(`SELECT v.id,v.problem_id,v.seed,v.statement_md,v.assets_dir
		FROM variant v JOIN problem p ON p.id=v.problem_id WHERE p.exam_id=? ORDER BY v.seed`, examID)
	if err != nil {
		return nil, err
	}
	for vrows.Next() {
		var v Variant
		if err := vrows.Scan(&v.ID, &v.ProblemID, &v.Seed, &v.StatementMD, &v.AssetsDir); err != nil {
			vrows.Close()
			return nil, err
		}
		if p := byID[v.ProblemID]; p != nil {
			p.Variants = append(p.Variants, v)
		}
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}

	crows, err := s.db.Query(`SELECT c.id,c.problem_id,c.variant_id,c.case_no,c.input_path,c.expected_path,c.score,c.is_sample,c.sort
		FROM test_case c JOIN problem p ON p.id=c.problem_id WHERE p.exam_id=? ORDER BY c.problem_id,c.sort,c.case_no`, examID)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var c TestCase
		var sample int
		if err := crows.Scan(&c.ID, &c.ProblemID, &c.VariantID, &c.CaseNo, &c.InputPath,
			&c.ExpectedPath, &c.Score, &sample, &c.Sort); err != nil {
			crows.Close()
			return nil, err
		}
		c.IsSample = sample != 0
		if p := byID[c.ProblemID]; p != nil {
			p.Cases = append(p.Cases, c)
		}
	}
	crows.Close()
	return problems, crows.Err()
}

// ProblemByKey 按 key 取题目（含变体和用例）。
func (s *Store) ProblemByKey(examID, key string) (*Problem, error) {
	problems, err := s.ListProblems(examID)
	if err != nil {
		return nil, err
	}
	for i := range problems {
		if problems[i].Key == key {
			return &problems[i], nil
		}
	}
	return nil, ErrNotFound
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
