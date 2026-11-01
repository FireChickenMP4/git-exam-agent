package agent

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// ensureGit 把 git 的 bin 目录加进 PATH：go-git 的本地路径传输依赖 git-receive-pack。
// 真实考场走 HTTP(S)，不需要本机 git。
func ensureGit(t *testing.T) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 不可用，跳过本地 push 测试")
	}
	var dirs []string
	dirs = append(dirs, filepath.Dir(gitBin))
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		dirs = append(dirs, execPath, filepath.Join(filepath.Dir(filepath.Dir(execPath)), "bin"))
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join(dirs, sep)+sep+os.Getenv("PATH"))
}

func newTestAgent(t *testing.T) (*Agent, string, string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	remote := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Repo:      remote,
		Username:  "20231234",
		Token:     "test-token",
		Workspace: "work",
		dir:       root,
	}
	cfg.applyDefaults()
	a, err := newAgent(cfg, io.Discard)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	return a, work, remote
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remoteHead(t *testing.T, remote string) string {
	t.Helper()
	r, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("打开远端: %v", err)
	}
	ref, err := r.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		t.Fatalf("读取远端 main: %v", err)
	}
	return ref.Hash().String()
}

func TestCommitPushAndWhitelist(t *testing.T) {
	ensureGit(t)
	a, work, remote := newTestAgent(t)
	writeFile(t, work, "go.mod", "module exam\n\ngo 1.26\n")
	writeFile(t, work, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, work, "notes.md", "不该被提交\n")

	res, err := a.commitAll(initialCommitMessage, time.Now(), false)
	if err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	if res.SHA == "" {
		t.Fatal("应产生提交")
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "notes.md" {
		t.Fatalf("被跳过的文件 = %v, want [notes.md]", res.Skipped)
	}
	if err := a.pushRemote(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := remoteHead(t, remote); got != res.SHA {
		t.Fatalf("远端 HEAD = %s, want %s", got, res.SHA)
	}
}

func TestHeartbeatCreatesEmptyCommit(t *testing.T) {
	a, work, _ := newTestAgent(t)
	writeFile(t, work, "main.go", "package main\n")
	first, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil || first.SHA == "" {
		t.Fatalf("首个提交失败: %v", err)
	}

	hb, err := a.commitAll(heartbeatCommitMessage, time.Now(), true)
	if err != nil {
		t.Fatalf("心跳提交: %v", err)
	}
	if hb.SHA == "" || hb.SHA == first.SHA {
		t.Fatalf("心跳应产生新的空提交: first=%s heartbeat=%s", first.SHA, hb.SHA)
	}
	if len(hb.Staged) != 0 {
		t.Fatalf("心跳不应暂存文件: %v", hb.Staged)
	}

	// 没有任何改动时普通提交不应产生 commit
	none, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if none.SHA != "" {
		t.Fatalf("无改动不应提交，实际 %s", none.SHA)
	}
}

func TestRunOnceCheckpoint(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	remote := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Repo: remote, Username: "20231234", Token: "tok", Workspace: "work", dir: root}
	cfg.applyDefaults()
	writeFile(t, work, "main.go", "package main\n")

	var out bytes.Buffer
	if err := RunOnce(context.Background(), cfg, &out); err != nil {
		t.Fatalf("RunOnce: %v\n%s", err, out.String())
	}
	if remoteHead(t, remote) == "" {
		t.Fatal("检查点未推送")
	}
	// 第二次没有改动也应成功（推送 already up to date）
	if err := RunOnce(context.Background(), cfg, &out); err != nil {
		t.Fatalf("第二次 RunOnce: %v", err)
	}
}

func TestHeartbeatDoesNotSwallowChanges(t *testing.T) {
	a, work, _ := newTestAgent(t)
	writeFile(t, work, "main.go", "package main\n")
	first, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil || first.SHA == "" {
		t.Fatalf("首个提交失败: %v", err)
	}
	headBefore, err := a.repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, work, "main.go", "package main\n\n// 真实改动\n")
	a.lastCommit = time.Now().Add(-2 * time.Minute)
	a.heartbeat(time.Now())

	headAfter, err := a.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if headAfter.Hash() != headBefore.Hash() {
		t.Fatal("有未提交改动时心跳不应提交")
	}
	res, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA == "" || res.SHA == first.SHA {
		t.Fatalf("真实改动未被 auto-commit 提交: %+v", res)
	}
}

func TestHeartbeatOnEmptyRepo(t *testing.T) {
	a, _, _ := newTestAgent(t)
	hb, err := a.commitAll(heartbeatCommitMessage, time.Now(), true)
	if err != nil {
		t.Fatalf("空仓库心跳提交: %v", err)
	}
	if hb.SHA == "" {
		t.Fatal("空仓库心跳应产生提交")
	}
}

func TestDeletedFileIsRecorded(t *testing.T) {
	a, work, _ := newTestAgent(t)
	writeFile(t, work, "main.go", "package main\n")
	first, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil || first.SHA == "" {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(work, "main.go")); err != nil {
		t.Fatal(err)
	}
	del, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil {
		t.Fatalf("删除后提交: %v", err)
	}
	if del.SHA == "" || del.SHA == first.SHA {
		t.Fatalf("删除应产生新提交: %+v", del)
	}

	head, err := a.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := a.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.File("main.go"); err == nil {
		t.Fatal("删除的文件仍在提交树中")
	}
}
