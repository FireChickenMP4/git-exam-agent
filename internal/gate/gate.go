// Package gate 实现推送白名单与大小限制（pre-receive）。
package gate

import (
	"fmt"
	"strings"

	"exam.local/tools/internal/paths"
)

// Rules 是推送限制。
type Rules struct {
	MaxFileBytes int64
	MaxNewBytes  int64
	MaxRepoBytes int64
	AllowForce   bool
}

// DefaultRules 对应 PLAN.md 7.5：单文件 5MB、单次新增 20MB、仓库 200MB。
func DefaultRules() Rules {
	return Rules{
		MaxFileBytes: 5 << 20,
		MaxNewBytes:  20 << 20,
		MaxRepoBytes: 200 << 20,
	}
}

// File 是推送后提交树里的一个文件。
type File struct {
	Path string
	Size int64
}

// Push 是一次推送的检查输入。
type Push struct {
	Ref          string
	OldSHA       string
	NewSHA       string
	ForcePush    bool
	Files        []File
	AddedBytes   int64
	RepoBytes    int64
	FilesChanged int
	LinesAdded   int
	LinesDeleted int
}

// Check 校验推送，返回拒绝原因（中文）。nil 表示允许。
func Check(p Push, r Rules) error {
	if p.Ref != "refs/heads/main" {
		return fmt.Errorf("只允许推送到 main 分支，当前为 %s", p.Ref)
	}
	if isZeroSHA(p.NewSHA) {
		return fmt.Errorf("不允许删除 main 分支")
	}
	if p.ForcePush && !r.AllowForce {
		return fmt.Errorf("不允许 force-push")
	}
	for _, f := range p.Files {
		if !paths.Allowed(f.Path) {
			return fmt.Errorf("只能提交 %s，不允许提交 %s", paths.Describe(), f.Path)
		}
		if r.MaxFileBytes > 0 && f.Size > r.MaxFileBytes {
			return fmt.Errorf("文件 %s 超过 %d MB", f.Path, r.MaxFileBytes>>20)
		}
	}
	if r.MaxNewBytes > 0 && p.AddedBytes > r.MaxNewBytes {
		return fmt.Errorf("单次新增内容超过 %d MB", r.MaxNewBytes>>20)
	}
	if r.MaxRepoBytes > 0 && p.RepoBytes > r.MaxRepoBytes {
		return fmt.Errorf("仓库超过 %d MB", r.MaxRepoBytes>>20)
	}
	return nil
}

func isZeroSHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

// ZeroSHA 判断是否为全零（新建或删除）的 SHA。
func ZeroSHA(sha string) bool { return isZeroSHA(sha) }
