// exam-hook 是 Gitea 的 pre-receive 钩子：本地做路径/大小/force-push 检查，
// 再向 examd 询问是否允许推送（冻结判断 + 记录推送事件）。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"exam.local/tools/internal/gate"
)

func main() {
	var (
		examdURL = flag.String("examd", envOr("EXAM_HOOK_URL", "http://127.0.0.1:8080"), "examd 地址")
		token    = flag.String("token", os.Getenv("EXAM_HOOK_TOKEN"), "hook 共享令牌")
		failOpen = flag.Bool("fail-open", false, "examd 不可达时放行（默认拒绝）")
	)
	flag.Parse()

	if err := run(*examdURL, *token, *failOpen); err != nil {
		fmt.Fprintf(os.Stderr, "推送被拒绝: %v\n", err)
		os.Exit(1)
	}
}

func run(examdURL, token string, failOpen bool) error {
	repo, err := git.PlainOpen(".")
	if err != nil {
		return fmt.Errorf("打开仓库失败: %w", err)
	}
	repoPath, err := filepath.Abs(".")
	if err != nil {
		repoPath = "."
	}
	repoBytes := dirSize(repoPath)

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("读取推送信息失败: %w", err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) == 1 && len(bytes.TrimSpace(lines[0])) == 0 {
		return nil
	}

	for _, line := range lines {
		fields := bytes.Fields(line)
		if len(fields) != 3 {
			return fmt.Errorf("无法解析推送信息: %q", string(line))
		}
		p := gate.Push{
			OldSHA:    string(fields[0]),
			NewSHA:    string(fields[1]),
			Ref:       string(fields[2]),
			RepoBytes: repoBytes,
		}
		if err := inspect(repo, &p); err != nil {
			return err
		}
		if err := gate.Check(p, gate.DefaultRules()); err != nil {
			return err
		}
		if err := askExamd(examdURL, token, repoPath, p, failOpen); err != nil {
			return err
		}
	}
	return nil
}

func inspect(repo *git.Repository, p *gate.Push) error {
	if gate.ZeroSHA(p.NewSHA) {
		return nil // 删除分支交给 gate.Check 拒绝
	}
	commit, err := repo.CommitObject(plumbing.NewHash(p.NewSHA))
	if err != nil {
		return fmt.Errorf("读取提交失败: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("读取提交树失败: %w", err)
	}

	var addedBytes int64
	err = tree.Files().ForEach(func(f *object.File) error {
		size, err := blobSize(repo, f.Hash)
		if err != nil {
			return err
		}
		p.Files = append(p.Files, gate.File{Path: f.Name, Size: size})
		return nil
	})
	if err != nil {
		return fmt.Errorf("遍历文件失败: %w", err)
	}

	if gate.ZeroSHA(p.OldSHA) {
		for _, f := range p.Files {
			addedBytes += f.Size
		}
	} else {
		oldCommit, err := repo.CommitObject(plumbing.NewHash(p.OldSHA))
		if err != nil {
			return fmt.Errorf("读取旧提交失败: %w", err)
		}
		ancestor, err := isAncestor(repo, oldCommit.Hash, commit.Hash)
		if err != nil {
			return err
		}
		if !ancestor {
			p.ForcePush = true
		}
		oldTree, err := oldCommit.Tree()
		if err != nil {
			return fmt.Errorf("读取旧提交树失败: %w", err)
		}
		changes, err := object.DiffTree(oldTree, tree)
		if err != nil {
			return fmt.Errorf("计算差异失败: %w", err)
		}
		p.FilesChanged = len(changes)
		for _, ch := range changes {
			if ch.To.TreeEntry.Hash.IsZero() {
				continue
			}
			size, err := blobSize(repo, ch.To.TreeEntry.Hash)
			if err != nil {
				continue
			}
			addedBytes += size
		}
	}
	if p.FilesChanged == 0 {
		p.FilesChanged = len(p.Files)
	}
	p.AddedBytes = addedBytes
	return nil
}

func isAncestor(repo *git.Repository, old, new plumbing.Hash) (bool, error) {
	if old == new {
		return true, nil
	}
	iter, err := repo.Log(&git.LogOptions{From: new})
	if err != nil {
		return false, fmt.Errorf("遍历提交历史: %w", err)
	}
	defer iter.Close()

	found := false
	visited := 0
	err = iter.ForEach(func(c *object.Commit) error {
		visited++
		if c.Hash == old {
			found = true
			return storer.ErrStop
		}
		if visited > 200000 {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return false, err
	}
	return found, nil
}

func blobSize(repo *git.Repository, hash plumbing.Hash) (int64, error) {
	obj, err := repo.Storer.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		return 0, err
	}
	return obj.Size(), nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

type checkRequest struct {
	RepoPath     string `json:"repo_path"`
	Ref          string `json:"ref"`
	OldSHA       string `json:"old_sha"`
	NewSHA       string `json:"new_sha"`
	FilesChanged int    `json:"files_changed"`
	LinesAdded   int    `json:"lines_added"`
	LinesDeleted int    `json:"lines_deleted"`
	AddedBytes   int64  `json:"added_bytes"`
	RepoBytes    int64  `json:"repo_bytes"`
}

type checkResponse struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
}

func askExamd(examdURL, token, repoPath string, p gate.Push, failOpen bool) error {
	payload, err := json.Marshal(checkRequest{
		RepoPath:     repoPath,
		Ref:          p.Ref,
		OldSHA:       p.OldSHA,
		NewSHA:       p.NewSHA,
		FilesChanged: p.FilesChanged,
		LinesAdded:   p.LinesAdded,
		LinesDeleted: p.LinesDeleted,
		AddedBytes:   p.AddedBytes,
		RepoBytes:    p.RepoBytes,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, examdURL+"/api/hooks/pre-receive", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Exam-Hook-Token", token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if failOpen {
			fmt.Fprintf(os.Stderr, "警告: examd 不可达（%v），已按 fail-open 放行\n", err)
			return nil
		}
		return fmt.Errorf("examd 不可达: %w", err)
	}
	defer resp.Body.Close()

	var out checkResponse
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("examd 返回异常（HTTP %d）", resp.StatusCode)
	}
	if !out.Allow {
		if out.Reason == "" {
			out.Reason = "examd 拒绝推送"
		}
		return errors.New(out.Reason)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
