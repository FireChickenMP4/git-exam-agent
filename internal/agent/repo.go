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
	initialCommitMessage   = "agent: initial commit"
	autoCommitMessage      = "agent: auto-commit"
	heartbeatCommitMessage = "agent: heartbeat"
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
