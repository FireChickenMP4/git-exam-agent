package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// InitWorkspace 首次把远端仓库拉到工作区，考生机器不需要安装 git。
func InitWorkspace(ctx context.Context, cfg *Config, out io.Writer) error {
	dir, err := cfg.WorkspaceDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建工作区 %s: %w", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("工作区已经是 git 仓库，无需初始化: %s", dir)
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			switch e.Name() {
			case ".exam-agent.json", ".exam-agent.events.jsonl", ".exam-agent.log":
				continue
			}
			return fmt.Errorf("工作区非空，请把 workspace 指向空目录或先移走文件: %s", dir)
		}
	}

	var caBundle []byte
	if cfg.CAFile != "" {
		raw, err := os.ReadFile(cfg.CAFilePath())
		if err != nil {
			return fmt.Errorf("读取 ca_file: %w", err)
		}
		caBundle = raw
	}

	_, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:             cfg.Repo,
		Auth:            authFor(cfg),
		ReferenceName:   plumbing.NewBranchReferenceName(branchName),
		SingleBranch:    true,
		Tags:            git.NoTags,
		CABundle:        caBundle,
		InsecureSkipTLS: cfg.InsecureSkipVerify,
	})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		repo, _, initErr := openRepo(dir)
		if initErr != nil {
			return initErr
		}
		if err := ensureRemote(repo, cfg.Repo); err != nil {
			return err
		}
		if out != nil {
			fmt.Fprintf(out, "远端为空，已初始化本地仓库: %s\n", dir)
		}
		return nil
	}
	if err != nil {
		// 克隆失败时清理半成品，避免下次提示“工作区非空”或“已是 git 仓库”。
		_ = os.RemoveAll(filepath.Join(dir, ".git"))
		if entries, readErr := os.ReadDir(dir); readErr == nil {
			for _, e := range entries {
				switch e.Name() {
				case ".exam-agent.json", ".exam-agent.events.jsonl", ".exam-agent.log":
					continue
				}
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
		return fmt.Errorf("拉取仓库 %s: %w", cfg.Repo, err)
	}
	if out != nil {
		fmt.Fprintf(out, "已拉取 %s 到 %s\n", cfg.Repo, dir)
	}
	return nil
}
