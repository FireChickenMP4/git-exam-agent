package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func buildHook(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "exam-hook.exe")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("构建 exam-hook: %v\n%s", err, out)
	}
	return bin
}

// newRepo 创建带一个提交的仓库，返回目录与 HEAD。
func newRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "test", Email: "t@example.com", When: time.Now()}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	return dir, head.Hash().String()
}

type hookRun struct {
	code   int
	output string
}

func runHook(t *testing.T, bin, repoDir, stdin string, args ...string) hookRun {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = repoDir
	cmd.Stdin = strings.NewReader(stdin)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("运行 exam-hook: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return hookRun{code: code, output: buf.String()}
}

func TestHookAllowsGoPush(t *testing.T) {
	bin := buildHook(t)
	repoDir, sha := newRepo(t, map[string]string{"main.go": "package main\n", "go.mod": "module exam\n"})

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Exam-Hook-Token") != "hook-token" {
			t.Errorf("缺少钩子令牌")
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"allow":true}`))
	}))
	defer srv.Close()

	run := runHook(t, bin, repoDir, strings.Repeat("0", 40)+" "+sha+" refs/heads/main\n",
		"--examd", srv.URL, "--token", "hook-token")
	if run.code != 0 {
		t.Fatalf("应放行，exit=%d output=%s", run.code, run.output)
	}
	if got == nil {
		t.Fatal("未请求 examd")
	}
	if got["new_sha"] != sha || got["ref"] != "refs/heads/main" {
		t.Fatalf("请求内容错误: %+v", got)
	}
	if got["files_changed"].(float64) < 2 {
		t.Fatalf("files_changed = %v", got["files_changed"])
	}
}

func commitMore(t *testing.T, dir, name, content string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "test", Email: "t@example.com", When: time.Now()}
	hash, err := wt.Commit("more", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

func TestHookPostReceiveReportsStats(t *testing.T) {
	bin := buildHook(t)
	dir, old := newRepo(t, map[string]string{"main.go": "package main\n"})
	newSHA := commitMore(t, dir, "main.go", "package main\n\nfunc a() {}\nfunc b() {}\nfunc c() {}\n")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	run := runHook(t, bin, dir, old+" "+newSHA+" refs/heads/main\n",
		"--post-receive", "--examd", srv.URL, "--token", "hook-token")
	if run.code != 0 {
		t.Fatalf("post-receive 应始终成功，exit=%d output=%s", run.code, run.output)
	}
	if got == nil {
		t.Fatal("未上报统计")
	}
	if got["new_sha"] != newSHA {
		t.Fatalf("new_sha = %v", got["new_sha"])
	}
	if added, _ := got["lines_added"].(float64); added < 3 {
		t.Fatalf("lines_added = %v", got["lines_added"])
	}
	if files, _ := got["files_changed"].(float64); files < 1 {
		t.Fatalf("files_changed = %v", got["files_changed"])
	}
}

func TestHookRejectsNonGoFile(t *testing.T) {
	bin := buildHook(t)
	repoDir, sha := newRepo(t, map[string]string{"main.go": "package main\n", "notes.md": "hello\n"})
	run := runHook(t, bin, repoDir, strings.Repeat("0", 40)+" "+sha+" refs/heads/main\n",
		"--examd", "http://127.0.0.1:1", "--token", "x")
	if run.code == 0 {
		t.Fatal("含非 Go 文件应被拒绝")
	}
	if !strings.Contains(run.output, "notes.md") {
		t.Fatalf("拒绝原因应包含文件名: %s", run.output)
	}
}

func TestHookRejectsNonMainBranch(t *testing.T) {
	bin := buildHook(t)
	repoDir, sha := newRepo(t, map[string]string{"main.go": "package main\n"})
	run := runHook(t, bin, repoDir, strings.Repeat("0", 40)+" "+sha+" refs/heads/dev\n",
		"--examd", "http://127.0.0.1:1", "--token", "x")
	if run.code == 0 {
		t.Fatal("非 main 分支应被拒绝")
	}
	if !strings.Contains(run.output, "main") {
		t.Fatalf("拒绝原因错误: %s", run.output)
	}
}

func TestHookRejectsWhenExamdDenies(t *testing.T) {
	bin := buildHook(t)
	repoDir, sha := newRepo(t, map[string]string{"main.go": "package main\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"allow":false,"reason":"考试已结束，推送被拒绝"}`))
	}))
	defer srv.Close()

	run := runHook(t, bin, repoDir, strings.Repeat("0", 40)+" "+sha+" refs/heads/main\n",
		"--examd", srv.URL, "--token", "x")
	if run.code == 0 {
		t.Fatal("examd 拒绝时钩子应失败")
	}
	if !strings.Contains(run.output, "考试已结束") {
		t.Fatalf("未透传拒绝原因: %s", run.output)
	}
}

func TestHookFailClosedWhenExamdDown(t *testing.T) {
	bin := buildHook(t)
	repoDir, sha := newRepo(t, map[string]string{"main.go": "package main\n"})
	run := runHook(t, bin, repoDir, strings.Repeat("0", 40)+" "+sha+" refs/heads/main\n",
		"--examd", "http://127.0.0.1:1", "--token", "x")
	if run.code == 0 {
		t.Fatal("examd 不可达时应拒绝")
	}
	if !strings.Contains(run.output, "examd 不可达") {
		t.Fatalf("错误信息错误: %s", run.output)
	}
}
