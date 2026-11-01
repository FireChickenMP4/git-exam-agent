// Package provision 负责按名单批量开通 Gitea 用户、仓库、令牌与 webhook。
package provision

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"exam.local/tools/internal/config"
	"exam.local/tools/internal/gitea"
	"exam.local/tools/internal/roster"
	"exam.local/tools/internal/store"
)

//go:embed template/go.mod.tmpl template/main.go template/gitignore
var templateFiles embed.FS

// Result 是单个考生的开通结果。
type Result struct {
	StudentNo   string
	Name        string
	RepoURL     string
	Token       string
	UserCreated bool
	RepoSeeded  bool
	Err         error
}

// Summary 是批量开通汇总。
type Summary struct {
	Results []Result
}

// Failed 返回失败的考生数。
func (s *Summary) Failed() int {
	n := 0
	for _, r := range s.Results {
		if r.Err != nil {
			n++
		}
	}
	return n
}

// Slips 返回可打印凭条（只含有新令牌的考生）。
func (s *Summary) Slips() []roster.Slip {
	var out []roster.Slip
	for _, r := range s.Results {
		if r.Err != nil || r.Token == "" {
			continue
		}
		out = append(out, roster.Slip{StudentNo: r.StudentNo, Name: r.Name, RepoURL: r.RepoURL, Token: r.Token})
	}
	return out
}

// Provisioner 执行开通流程。
type Provisioner struct {
	cfg  *config.Config
	st   *store.Store
	gc   *gitea.Client
	logf func(format string, args ...any)
}

// New 创建开通器。
func New(cfg *config.Config, st *store.Store, gc *gitea.Client, logf func(string, ...any)) *Provisioner {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Provisioner{cfg: cfg, st: st, gc: gc, logf: logf}
}

// Run 为场次内全部考生开通账号与仓库。
func (p *Provisioner) Run(ctx context.Context, examID string) (*Summary, error) {
	exam, err := p.st.GetExam(examID)
	if err != nil {
		return nil, err
	}
	if p.cfg.Gitea.URL == "" {
		return nil, fmt.Errorf("未配置 gitea.url")
	}
	if p.cfg.Gitea.AdminToken == "" && p.cfg.Gitea.AdminPassword == "" {
		return nil, fmt.Errorf("未配置 gitea.admin_token 或 gitea.admin_password")
	}

	candidates, err := p.st.ListCandidates(exam.ID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("场次 %s 没有考生，请先导入名单", exam.ID)
	}

	webhookURL := webhookEndpoint(p.cfg)
	summary := &Summary{}
	for _, c := range candidates {
		res := p.provisionOne(ctx, &c, webhookURL)
		if res.Err != nil {
			p.logf("provision %s failed: %v", c.StudentNo, res.Err)
		} else {
			p.logf("provision %s ok repo=%s", c.StudentNo, res.RepoURL)
		}
		summary.Results = append(summary.Results, res)
	}
	return summary, nil
}

func (p *Provisioner) provisionOne(ctx context.Context, c *store.Candidate, webhookURL string) Result {
	res := Result{StudentNo: c.StudentNo, Name: c.Name}
	repoName := c.RepoName
	if repoName == "" {
		repoName = p.cfg.Exam.RepoPrefix + c.StudentNo
	}

	password := randomHex(16)
	created, err := p.gc.CreateUser(ctx, c.StudentNo, c.Name, c.StudentNo+"@exam.local", password)
	if err != nil {
		res.Err = fmt.Errorf("创建用户: %w", err)
		return res
	}
	res.UserCreated = created
	if !created {
		// 重复开通时同步密码，保证令牌创建的回退路径可用。
		if err := p.gc.SetPassword(ctx, c.StudentNo, password); err != nil {
			p.logf("warn: 重置密码失败（%s）: %v", c.StudentNo, err)
		}
	}

	repo, err := p.gc.CreateRepo(ctx, c.StudentNo, repoName)
	if err != nil {
		res.Err = fmt.Errorf("创建仓库: %w", err)
		return res
	}
	repoURL := repo.CloneURL
	if repoURL == "" {
		repoURL = strings.TrimRight(p.cfg.Gitea.URL, "/") + "/" + c.StudentNo + "/" + repoName + ".git"
	}
	res.RepoURL = repoURL

	if err := p.st.SetCandidateRepo(c.ID, c.StudentNo, repoName); err != nil {
		res.Err = fmt.Errorf("记录仓库: %w", err)
		return res
	}
	if err := p.st.UpsertRepo(c.ID, repoURL); err != nil {
		res.Err = fmt.Errorf("记录仓库地址: %w", err)
		return res
	}

	if repo.Empty {
		if err := p.seedRepo(repoURL); err != nil {
			res.Err = fmt.Errorf("写入仓库模板: %w", err)
			return res
		}
		res.RepoSeeded = true
	} else {
		hasMain, err := p.gc.BranchExists(ctx, c.StudentNo, repoName, "main")
		if err != nil {
			res.Err = fmt.Errorf("检查默认分支: %w", err)
			return res
		}
		if !hasMain {
			if err := p.seedRepo(repoURL); err != nil {
				res.Err = fmt.Errorf("写入仓库模板: %w", err)
				return res
			}
			res.RepoSeeded = true
		}
	}

	if c.TokenHash == "" {
		tokenName := fmt.Sprintf("exam-agent-%d", time.Now().UnixNano())
		token, err := p.gc.CreateToken(ctx, c.StudentNo, tokenName)
		if err != nil {
			// 部分 Gitea 版本不允许管理员 Sudo 创建令牌，回退为考生本人口令。
			token, err = p.gc.CreateTokenBasic(ctx, c.StudentNo, password, tokenName)
		}
		if err != nil {
			res.Err = fmt.Errorf("创建令牌: %w", err)
			return res
		}
		if err := p.st.SetCandidateToken(c.ID, sha256Hex(token)); err != nil {
			res.Err = fmt.Errorf("保存令牌: %w", err)
			return res
		}
		res.Token = token
	}

	if p.cfg.Gitea.WebhookSecret == "" {
		p.logf("warn: gitea.webhook_secret 未配置，跳过 webhook（%s）", c.StudentNo)
	} else if err := p.gc.EnsureWebhook(ctx, c.StudentNo, repoName, webhookURL, p.cfg.Gitea.WebhookSecret); err != nil {
		res.Err = fmt.Errorf("配置 webhook: %w", err)
		return res
	}

	if err := p.gc.ProtectMain(ctx, c.StudentNo, repoName); err != nil {
		p.logf("warn: 保护 main 失败（%s）: %v", c.StudentNo, err)
	}
	return res
}

// ResetToken 为单个考生重新生成令牌，旧令牌保留在 Gitea 侧。
func (p *Provisioner) ResetToken(ctx context.Context, examID, studentNo string) (string, error) {
	candidates, err := p.st.ListCandidates(examID)
	if err != nil {
		return "", err
	}
	for _, c := range candidates {
		if c.StudentNo != studentNo {
			continue
		}
		tokenName := fmt.Sprintf("exam-agent-%d", time.Now().UnixNano())
		token, err := p.gc.CreateToken(ctx, c.StudentNo, tokenName)
		if err != nil {
			password := randomHex(16)
			if err := p.gc.SetPassword(ctx, c.StudentNo, password); err != nil {
				return "", fmt.Errorf("重置密码: %w", err)
			}
			token, err = p.gc.CreateTokenBasic(ctx, c.StudentNo, password, tokenName)
		}
		if err != nil {
			return "", err
		}
		if err := p.st.SetCandidateToken(c.ID, sha256Hex(token)); err != nil {
			return "", err
		}
		return token, nil
	}
	return "", fmt.Errorf("场次 %s 中找不到学号 %s", examID, studentNo)
}

// webhookEndpoint 返回 Gitea 回调 examd 的地址：优先 webhook_url，否则用 public_url。
func webhookEndpoint(cfg *config.Config) string {
	if cfg.Gitea.WebhookURL != "" {
		return cfg.Gitea.WebhookURL
	}
	return strings.TrimRight(cfg.PublicURL, "/") + "/api/hooks/gitea"
}

func (p *Provisioner) seedRepo(repoURL string) error {
	dir, err := os.MkdirTemp("", "exam-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		return err
	}

	files := map[string]string{
		"go.mod":     "template/go.mod.tmpl",
		"main.go":    "template/main.go",
		".gitignore": "template/gitignore",
	}
	for dst, src := range files {
		raw, err := templateFiles.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, dst), raw, 0o644); err != nil {
			return err
		}
	}

	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	sig := &object.Signature{Name: "exam-setup", Email: "exam@exam.local", When: time.Now()}
	if _, err := wt.Commit("template: exam skeleton", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		return err
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{repoURL}}); err != nil {
		return err
	}
	user, pass := p.gc.BasicAuth()
	err = repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		RefSpecs:        []gitconfig.RefSpec{"refs/heads/main:refs/heads/main"},
		Auth:            &http.BasicAuth{Username: user, Password: pass},
		CABundle:        p.gc.CABundle(),
		InsecureSkipTLS: p.gc.InsecureSkipTLS(),
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("pw-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
