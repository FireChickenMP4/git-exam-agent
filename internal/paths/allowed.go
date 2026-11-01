// Package paths 定义考生仓库允许提交的文件白名单。
// 与 pre-receive 钩子、agent 共用同一套规则，避免两边不一致。
package paths

import (
	"path"
	"strings"
)

// 与 PLAN.md 7.5 一致：只允许 Go 源码和构建元数据。
var allowedFiles = map[string]bool{
	"go.mod":             true,
	"go.sum":             true,
	"vendor/modules.txt": true,
	".gitignore":         true,
}

var allowedExtensions = map[string]bool{".go": true}

// Allowed 判断仓库内相对路径是否允许提交。
func Allowed(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean(p)
	if p == "." || strings.HasPrefix(p, "../") {
		return false
	}
	if allowedFiles[p] {
		return true
	}
	return allowedExtensions[strings.ToLower(path.Ext(p))]
}

// Describe 返回白名单的人类可读说明。
func Describe() string {
	return "*.go、go.mod、go.sum、vendor/modules.txt、.gitignore"
}
