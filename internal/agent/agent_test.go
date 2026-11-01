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
	"github.com/go-git/go-git/v5/plumbing/object"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

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

func TestInitWorkspace(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()

	// 非空目录应拒绝
	nonEmpty := filepath.Join(root, "nonempty")
	if err := os.MkdirAll(nonEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nonEmpty, "x.txt", "x")
	cfg := &Config{Repo: filepath.Join(root, "remote.git"), Username: "u", Token: "t", Workspace: nonEmpty, dir: root}
	cfg.applyDefaults()
	if err := InitWorkspace(context.Background(), cfg, &bytes.Buffer{}); err == nil {
		t.Fatal("非空工作区应报错")
	}

	// 从已有提交的本地仓库 clone
	src := filepath.Join(root, "src")
	if _, err := git.PlainInitWithOptions(src, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, "main.go", "package main\n")
	seedRepo, err := git.PlainOpen(src)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := seedRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("main.go"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, ".exam-agent.json", "{}")
	cloneCfg := &Config{Repo: src, Username: "u", Token: "t", Workspace: dst, dir: root}
	cloneCfg.applyDefaults()
	if err := InitWorkspace(context.Background(), cloneCfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("InitWorkspace clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.go")); err != nil {
		t.Fatalf("clone 后缺少文件: %v", err)
	}

	// 已初始化的目录不应重复 clone
	if err := InitWorkspace(context.Background(), cloneCfg, &bytes.Buffer{}); err == nil {
		t.Fatal("已有 .git 应报错")
	}
}

func seededRemote(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "init", "-b", "main")
	writeFile(t, seed, "template.go", "package main\n")
	gitRun(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "add", ".")
	gitRun(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-m", "template")
	gitRun(t, root, "init", "--bare", "-b", "main", remote)
	gitRun(t, seed, "remote", "add", "origin", remote)
	gitRun(t, seed, "push", "-q", "origin", "main")
	return root, remote
}

func TestRecoverDiverged(t *testing.T) {
	ensureGit(t)
	root, remote := seededRemote(t)

	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	cfg := &Config{Repo: remote, Username: "20231234", Token: "t", Workspace: work, dir: root}
	cfg.applyDefaults()
	a, err := newAgent(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "candidate.go", "package main\n\n// candidate\n")
	res, err := a.commitAll(autoCommitMessage, time.Now(), false)
	if err != nil || res.SHA == "" {
		t.Fatal(err)
	}
	if err := a.pushRemote(); !isNonFastForward(err) {
		t.Fatalf("期望 non-fast-forward，实际 %v", err)
	}
	a.commitSHA = res.SHA
	a.tryPush(time.Now())

	// 远端 main 应包含 candidate.go
	r, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.File("candidate.go"); err != nil {
		t.Fatal("恢复后远端缺少 candidate.go")
	}
	if a.fails != 0 {
		t.Fatalf("恢复后 fails=%d", a.fails)
	}
}

func TestRunOnceRecoversDiverged(t *testing.T) {
	ensureGit(t)
	root, remote := seededRemote(t)
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	writeFile(t, work, "candidate.go", "package main\n\n// checkpoint\n")
	cfg := &Config{Repo: remote, Username: "20231234", Token: "t", Workspace: work, dir: root}
	cfg.applyDefaults()

	var out bytes.Buffer
	if err := RunOnce(context.Background(), cfg, &out); err != nil {
		t.Fatalf("RunOnce 应自动恢复: %v\n%s", err, out.String())
	}

	r, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.File("candidate.go"); err != nil {
		t.Fatal("RunOnce 恢复后远端缺少 candidate.go")
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
