package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"exam.local/tools/internal/store"
)

func ensureGit(t *testing.T) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 不可用，跳过 review 集成测试")
	}
	dirs := []string{filepath.Dir(gitBin)}
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		dirs = append(dirs, execPath, filepath.Join(filepath.Dir(filepath.Dir(execPath)), "bin"))
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join(dirs, sep)+sep+os.Getenv("PATH"))
}

func TestReviewTimeline(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInitWithOptions(work, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2027, 2, 15, 10, 0, 0, 0, time.Local)
	commit := func(msg, content string, allowEmpty bool) {
		t.Helper()
		if content != "" {
			if err := os.WriteFile(filepath.Join(work, "main.go"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := wt.Add("main.go"); err != nil {
				t.Fatal(err)
			}
		}
		sig := &object.Signature{Name: "20231234", Email: "20231234@exam.local", When: base}
		if _, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: allowEmpty}); err != nil {
			t.Fatal(err)
		}
		base = base.Add(time.Minute)
	}

	commit("agent: initial commit", "package main\n", false)
	commit("agent: auto-commit", "package main\n\nfunc a() {}\n", false)
	commit("agent: heartbeat", "", true)
	commit("agent: auto-commit", "package main\n\nfunc a() {}\n\n"+strings.Repeat("// bulk\n", 300), false)
	commit("agent: checkpoint", "package main\n\nfunc a() {}\n", false)

	st, err := store.Open(filepath.Join(root, "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateExam(&store.Exam{
		ID: "exam", Title: "review", StartsAt: base.Add(-10 * time.Minute), EndsAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cid, err := st.UpsertCandidate(&store.Candidate{ExamID: "exam", StudentNo: "20231234", Name: "张三"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRepo(cid, work); err != nil {
		t.Fatal(err)
	}

	opts := Options{WorkDir: t.TempDir(), BulkLines: 200, MaxGap: 5 * time.Minute, Logf: t.Logf}
	rows, err := Run(context.Background(), st, "exam", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("默认应跳过心跳，rows=%d: %+v", len(rows), rows)
	}
	kinds := []string{}
	bulk := false
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
		if r.Kind == "auto" && r.Insertions >= 300 {
			bulk = true
			if !strings.Contains(r.Flags, "bulk_change") {
				t.Fatalf("大改动未标记: %+v", r)
			}
		}
	}
	want := []string{"initial", "auto", "auto", "checkpoint"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	if !bulk {
		t.Fatalf("未统计到 bulk 提交: %+v", rows)
	}

	opts.IncludeHeartbeat = true
	rows, err = Run(context.Background(), st, "exam", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "heartbeat" {
			found = true
		}
	}
	if !found {
		t.Fatal("include-heartbeat 未包含心跳提交")
	}
}
