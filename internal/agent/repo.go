package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"exam.local/tools/internal/paths"
)

const branchName = "main"

const (
	initialCommitMessage    = "agent: initial commit"
	autoCommitMessage       = "agent: auto-commit"
	heartbeatCommitMessage  = "agent: heartbeat"
	checkpointCommitMessage = "agent: checkpoint"
	recoverCommitMessage    = "agent: recover onto remote main"
)

// 提交白名单见 internal/paths，与 pre-receive 钩子保持一致。
type commitResult struct {
	SHA     string
	Staged  []string
	Skipped []string
}

func allowedPath(p string) bool {
	return paths.Allowed(p)
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func openRepo(dir string) (*git.Repository, *git.Worktree, error) {
	r, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		r, err = git.PlainInitWithOptions(dir, &git.PlainInitOptions{
			InitOptions: git.InitOptions{
				DefaultBranch: plumbing.NewBranchReferenceName(branchName),
			},
		})
	}
	if err != nil {
		return nil, nil, fmt.Errorf("打开或初始化仓库 %s: %w", dir, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		return nil, nil, fmt.Errorf("获取工作区: %w", err)
	}
	return r, wt, nil
}

// ensureRemote 保证 origin 指向配置里的仓库；配置换仓库时就地更新。
func ensureRemote(r *git.Repository, url string) error {
	remote, err := r.Remote("origin")
	if errors.Is(err, git.ErrRemoteNotFound) {
		_, err = r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
		if err != nil {
			return fmt.Errorf("创建 origin: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 origin: %w", err)
	}
	if len(remote.Config().URLs) == 1 && remote.Config().URLs[0] == url {
		return nil
	}
	if err := r.DeleteRemote("origin"); err != nil {
		return fmt.Errorf("删除旧 origin: %w", err)
	}
	if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		return fmt.Errorf("更新 origin: %w", err)
	}
	return nil
}

// authFor 只对 HTTP(S) 远端带凭据；本地路径（测试用）不需要认证。
func authFor(cfg *Config) transport.AuthMethod {
	if strings.HasPrefix(cfg.Repo, "http://") || strings.HasPrefix(cfg.Repo, "https://") {
		return &http.BasicAuth{Username: cfg.Username, Password: cfg.Token}
	}
	return nil
}

// commitAll 暂存白名单内的改动并提交。allowEmpty 为 true 时即使没有改动也提交（心跳用）。
func (a *Agent) commitAll(message string, when time.Time, allowEmpty bool) (commitResult, error) {
	var res commitResult

	status, err := a.wt.Status()
	if err != nil {
		return res, fmt.Errorf("读取工作区状态: %w", err)
	}

	for p := range status {
		if !allowedPath(p) {
			res.Skipped = append(res.Skipped, p)
			continue
		}
		// Add 同时处理新增、修改和删除。
		if _, err := a.wt.Add(p); err != nil {
			return res, fmt.Errorf("暂存 %s: %w", p, err)
		}
		res.Staged = append(res.Staged, p)
	}
	sort.Strings(res.Staged)
	sort.Strings(res.Skipped)

	if len(res.Staged) == 0 && !allowEmpty {
		return res, nil
	}

	sig := &object.Signature{
		Name:  a.cfg.Username,
		Email: a.cfg.Username + "@exam.local",
		When:  when,
	}
	hash, err := a.wt.Commit(message, &git.CommitOptions{
		Author:            sig,
		Committer:         sig,
		AllowEmptyCommits: allowEmpty,
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			return res, nil
		}
		return res, fmt.Errorf("提交: %w", err)
	}
	res.SHA = hash.String()
	return res, nil
}

// commitEmpty 只创建空提交，不暂存工作区，避免把真实改动包进心跳。
func (a *Agent) commitEmpty(message string, when time.Time) (commitResult, error) {
	sig := &object.Signature{
		Name:  a.cfg.Username,
		Email: a.cfg.Username + "@exam.local",
		When:  when,
	}
	hash, err := a.wt.Commit(message, &git.CommitOptions{
		Author:            sig,
		Committer:         sig,
		AllowEmptyCommits: true,
	})
	if err != nil {
		return commitResult{}, fmt.Errorf("提交: %w", err)
	}
	return commitResult{SHA: hash.String()}, nil
}

// pushRemote 把本地 main 推到 origin。远端已是最新不算失败。
func (a *Agent) pushRemote() error {
	err := a.repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		RefSpecs:        []config.RefSpec{"refs/heads/" + branchName + ":refs/heads/" + branchName},
		Auth:            a.auth,
		CABundle:        a.caBundle,
		InsecureSkipTLS: a.cfg.InsecureSkipVerify,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

func isNonFastForward(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "non-fast-forward")
}

// pushWithRecovery 推送；遇到历史分叉时自动备份并恢复到远端 main。
func (a *Agent) pushWithRecovery(now time.Time) error {
	err := a.pushRemote()
	if err == nil {
		return nil
	}
	if !isNonFastForward(err) {
		return err
	}
	var oldHead string
	if head, headErr := a.repo.Head(); headErr == nil {
		oldHead = head.Hash().String()
	}
	a.logf("push rejected (history diverged), trying recovery")
	backup, recErr := a.recoverDiverged(now)
	if recErr != nil {
		a.logf("recovery failed: %v", recErr)
		a.events.Write("push_recover_fail", map[string]any{
			"backup_branch": backup,
			"old_head":      oldHead,
			"error":         recErr.Error(),
		})
		return fmt.Errorf("%w; 自动恢复失败: %v", err, recErr)
	}
	a.logf("recovered onto remote main, local backup branch %s", backup)
	a.events.Write("push_recover", map[string]any{
		"backup_branch": backup,
		"old_head":      oldHead,
		"commit":        a.commitSHA,
	})
	return nil
}

// recoverDiverged 处理历史分叉：备份本地 main，重置到远端 main，再提交工作区并推送。
func (a *Agent) recoverDiverged(now time.Time) (string, error) {
	head, err := a.repo.Head()
	if err != nil {
		return "", fmt.Errorf("读取本地 HEAD: %w", err)
	}
	backup := "agent-backup-" + now.Format("20060102-150405")
	if err := a.repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName(backup), head.Hash())); err != nil {
		return "", fmt.Errorf("创建备份分支: %w", err)
	}
	if err := a.repo.Fetch(&git.FetchOptions{
		RemoteName:      "origin",
		RefSpecs:        []config.RefSpec{"+refs/heads/" + branchName + ":refs/remotes/origin/" + branchName},
		Auth:            a.auth,
		Tags:            git.NoTags,
		CABundle:        a.caBundle,
		InsecureSkipTLS: a.cfg.InsecureSkipVerify,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return backup, fmt.Errorf("拉取远端 main: %w", err)
	}
	remoteRef, err := a.repo.Reference(plumbing.NewRemoteReferenceName("origin", branchName), true)
	if err != nil {
		return backup, fmt.Errorf("读取远端 main: %w", err)
	}
	if err := a.wt.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.MixedReset}); err != nil {
		return backup, fmt.Errorf("重置到远端 main: %w", err)
	}
	res, err := a.commitAll(recoverCommitMessage, now, false)
	if err != nil {
		return backup, err
	}
	if res.SHA != "" {
		a.commitSHA = res.SHA
		a.lastCommit = now
	}
	if err := a.pushRemote(); err != nil {
		return backup, fmt.Errorf("恢复后推送: %w", err)
	}
	return backup, nil
}
