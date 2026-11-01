// Package review 生成考生提交时间线，用于考后人工复核。
package review

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"exam.local/tools/internal/store"
)

// Options 是复核参数。
type Options struct {
	WorkDir          string
	Auth             transport.AuthMethod
	CABundle         []byte
	InsecureSkipTLS  bool
	IncludeHeartbeat bool
	BulkLines        int
	MaxGap           time.Duration
	Logf             func(format string, args ...any)
}

// Row 是提交时间线中的一行。
type Row struct {
	StudentNo  string
	Time       time.Time
	SHA        string
	Kind       string
	Files      int
	Insertions int
	Deletions  int
	GapSeconds int
	Flags      string
}

const (
	kindHeartbeat  = "heartbeat"
	kindInitial    = "initial"
	kindTemplate   = "template"
	kindAuto       = "auto"
	kindCheckpoint = "checkpoint"
	kindEmpty      = "empty"
	kindOther      = "other"
)

// Run 生成场次（或单个考生）的提交时间线。
func Run(ctx context.Context, st *store.Store, examID, studentNo string, opts Options) ([]Row, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.BulkLines <= 0 {
		opts.BulkLines = 200
	}
	if opts.MaxGap <= 0 {
		opts.MaxGap = 10 * time.Minute
	}
	exam, err := st.GetExam(examID)
	if err != nil {
		return nil, err
	}
	candidates, err := st.ListCandidates(examID)
	if err != nil {
		return nil, err
	}

	var rows []Row
	for _, c := range candidates {
		if studentNo != "" && c.StudentNo != studentNo {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		repoRec, err := st.GetRepoByCandidate(c.ID)
		if err != nil || repoRec.URL == "" {
			opts.Logf("review %s: 没有仓库记录，跳过", c.StudentNo)
			continue
		}
		candidateRows, err := reviewRepo(ctx, c.StudentNo, repoRec.URL, exam, opts)
		if err != nil {
			opts.Logf("review %s: %v", c.StudentNo, err)
			continue
		}
		rows = append(rows, candidateRows...)
	}
	return rows, nil
}

func reviewRepo(ctx context.Context, studentNo, url string, exam *store.Exam, opts Options) ([]Row, error) {
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, err
	}
	dir := filepath.Join(opts.WorkDir, "review", sanitize(studentNo))
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:             url,
		Auth:            opts.Auth,
		Depth:           0,
		Tags:            git.NoTags,
		CABundle:        opts.CABundle,
		InsecureSkipTLS: opts.InsecureSkipTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("读取 HEAD: %w", err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var commits []*object.Commit
	if err := iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, c)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(commits, func(i, j int) bool {
		return commits[i].Committer.When.Before(commits[j].Committer.When)
	})

	windowStart := exam.StartsAt.Add(-5 * time.Minute)
	windowEnd := exam.EndsAt.Add(30 * time.Minute)
	var rows []Row
	prevAt := time.Time{}
	for _, commit := range commits {
		stats, err := commit.Stats()
		if err != nil {
			opts.Logf("review %s: 统计 %s 失败: %v", studentNo, commit.Hash.String()[:7], err)
		}
		files, insertions, deletions := 0, 0, 0
		for _, s := range stats {
			files++
			insertions += s.Addition
			deletions += s.Deletion
		}
		kind := classify(commit.Message, files == 0)
		if kind == kindHeartbeat && !opts.IncludeHeartbeat {
			prevAt = commit.Committer.When
			continue
		}

		row := Row{
			StudentNo:  studentNo,
			Time:       commit.Committer.When,
			SHA:        commit.Hash.String()[:7],
			Kind:       kind,
			Files:      files,
			Insertions: insertions,
			Deletions:  deletions,
		}
		if !prevAt.IsZero() {
			row.GapSeconds = int(commit.Committer.When.Sub(prevAt).Seconds())
		}
		var flags []string
		if files == 0 {
			flags = append(flags, "empty")
		}
		if insertions+deletions >= opts.BulkLines || files >= 15 {
			flags = append(flags, "bulk_change")
		}
		if row.GapSeconds > int(opts.MaxGap.Seconds()) {
			flags = append(flags, "long_gap")
		}
		if !exam.StartsAt.IsZero() && commit.Committer.When.Before(windowStart) {
			flags = append(flags, "before_window")
		}
		if !exam.EndsAt.IsZero() && commit.Committer.When.After(windowEnd) {
			flags = append(flags, "after_window")
		}
		row.Flags = strings.Join(flags, ";")
		rows = append(rows, row)
		prevAt = commit.Committer.When
	}
	return rows, nil
}

func classify(message string, empty bool) string {
	msg := strings.TrimSpace(message)
	switch {
	case strings.HasPrefix(msg, "agent: heartbeat"):
		return kindHeartbeat
	case strings.HasPrefix(msg, "agent: initial commit"):
		return kindInitial
	case strings.HasPrefix(msg, "template: exam skeleton"):
		return kindTemplate
	case strings.HasPrefix(msg, "agent: checkpoint"):
		return kindCheckpoint
	case strings.HasPrefix(msg, "agent: auto-commit"):
		return kindAuto
	case empty:
		return kindEmpty
	default:
		return kindOther
	}
}

func sanitize(s string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if clean == "" {
		return "x"
	}
	return clean
}

// WriteCSV 输出提交时间线 CSV。
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"student_no", "time", "sha", "kind", "files", "insertions", "deletions", "gap_seconds", "flags"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.StudentNo,
			r.Time.Format(time.RFC3339),
			r.SHA,
			r.Kind,
			strconv.Itoa(r.Files),
			strconv.Itoa(r.Insertions),
			strconv.Itoa(r.Deletions),
			strconv.Itoa(r.GapSeconds),
			r.Flags,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
