// Package gitea 封装开通流程用到的 Gitea Admin API。
package gitea

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client 是 Gitea API 客户端。
type Client struct {
	baseURL  string
	token    string
	user     string
	password string
	http     *http.Client
	caBundle []byte
	insecure bool
}

// HTTPError 表示 Gitea 返回的非 2xx 响应。
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gitea 返回 %d: %s", e.Status, e.Message)
}

// New 创建客户端。token 为空时回退到用户名密码；caFile 为可选的私有 CA。
func New(baseURL, token, user, password, caFile string, insecureSkipVerify bool) (*Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	var caBundle []byte
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 文件 %s: %w", caFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("CA 文件 %s 中没有可用证书", caFile)
		}
		tlsConfig.RootCAs = pool
		caBundle = raw
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		user:     user,
		password: password,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
		caBundle: caBundle,
		insecure: insecureSkipVerify,
	}, nil
}

// CABundle 返回配置的 CA 证书，供 go-git 使用。
func (c *Client) CABundle() []byte { return c.caBundle }

// InsecureSkipTLS 返回是否跳过证书校验，供 go-git 使用。
func (c *Client) InsecureSkipTLS() bool { return c.insecure }

// BasicAuth 返回推送用的用户名和口令（令牌作为口令）。
func (c *Client) BasicAuth() (string, string) {
	pass := c.token
	if pass == "" {
		pass = c.password
	}
	return c.user, pass
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, sudo string) error {
	return c.doWithAuth(ctx, method, path, body, out, "", "", sudo)
}

func (c *Client) doWithAuth(ctx context.Context, method, path string, body any, out any, basicUser, basicPass, sudo string) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case basicUser != "":
		req.SetBasicAuth(basicUser, basicPass)
	case c.token != "":
		req.Header.Set("Authorization", "token "+c.token)
	case c.user != "":
		req.SetBasicAuth(c.user, c.password)
	}
	if sudo != "" {
		req.Header.Set("Sudo", sudo)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return &HTTPError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析 %s 响应: %w", path, err)
		}
	}
	return nil
}

// IsStatus 判断错误是否为指定 HTTP 状态码。
func IsStatus(err error, status int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == status
}

// Repo 是 Gitea 仓库信息。
type Repo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	HTMLURL  string `json:"html_url"`
	Private  bool   `json:"private"`
	Empty    bool   `json:"empty"`
}

// CreateUser 创建用户；已存在时返回 false。
func (c *Client) CreateUser(ctx context.Context, username, fullName, email, password string) (bool, error) {
	body := map[string]any{
		"username":             username,
		"full_name":            fullName,
		"email":                email,
		"password":             password,
		"must_change_password": false,
		"send_notify":          false,
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/admin/users", body, nil, "")
	if err == nil {
		return true, nil
	}
	if IsStatus(err, http.StatusUnprocessableEntity) || IsStatus(err, http.StatusConflict) {
		return false, nil
	}
	return false, err
}

// CreateRepo 在指定用户名下创建私有仓库；已存在时返回现有仓库。
func (c *Client) CreateRepo(ctx context.Context, owner, name string) (*Repo, error) {
	body := map[string]any{
		"name":           name,
		"private":        true,
		"auto_init":      false,
		"default_branch": "main",
	}
	var repo Repo
	err := c.do(ctx, http.MethodPost, "/api/v1/admin/users/"+owner+"/repos", body, &repo, "")
	if err == nil {
		return &repo, nil
	}
	if IsStatus(err, http.StatusConflict) || IsStatus(err, http.StatusUnprocessableEntity) {
		return c.GetRepo(ctx, owner, name)
	}
	return nil, err
}

// GetRepo 读取仓库信息。
func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repo, error) {
	var repo Repo
	if err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+owner+"/"+name, nil, &repo, ""); err != nil {
		return nil, err
	}
	return &repo, nil
}

// SetPassword 重置考生密码（重复开通、令牌回退时使用）。
func (c *Client) SetPassword(ctx context.Context, username, password string) error {
	body := map[string]any{
		"login_name":           username,
		"source_id":            0,
		"password":             password,
		"must_change_password": false,
	}
	return c.do(ctx, http.MethodPatch, "/api/v1/admin/users/"+username, body, nil, "")
}

// CreateToken 以管理员 Sudo 身份为考生创建访问令牌。
func (c *Client) CreateToken(ctx context.Context, username, name string) (string, error) {
	return c.createToken(ctx, username, "", "", username, name)
}

// CreateTokenBasic 以考生本人口令创建访问令牌，用于 Gitea 不支持 Sudo 的场景。
func (c *Client) CreateTokenBasic(ctx context.Context, username, password, name string) (string, error) {
	return c.createToken(ctx, username, username, password, "", name)
}

func (c *Client) createToken(ctx context.Context, username, basicUser, basicPass, sudo, name string) (string, error) {
	body := map[string]any{
		"name":   name,
		"scopes": []string{"write:repository"},
	}
	var out struct {
		SHA1 string `json:"sha1"`
	}
	if err := c.doWithAuth(ctx, http.MethodPost, "/api/v1/users/"+username+"/tokens", body, &out, basicUser, basicPass, sudo); err != nil {
		return "", err
	}
	if out.SHA1 == "" {
		return "", fmt.Errorf("gitea 未返回令牌")
	}
	return out.SHA1, nil
}

type hook struct {
	ID     int64             `json:"id"`
	Type   string            `json:"type"`
	Active bool              `json:"active"`
	Config map[string]string `json:"config"`
}

// EnsureWebhook 确保仓库上存在指向 examd 的 push webhook。
func (c *Client) EnsureWebhook(ctx context.Context, owner, repo, url, secret string) error {
	var hooks []hook
	if err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+owner+"/"+repo+"/hooks", nil, &hooks, ""); err != nil {
		return err
	}
	for _, h := range hooks {
		if strings.EqualFold(h.Config["url"], url) {
			return nil
		}
	}
	body := map[string]any{
		"type":   "gitea",
		"active": true,
		"events": []string{"push"},
		"config": map[string]string{
			"url":          url,
			"content_type": "json",
			"secret":       secret,
			"http_method":  "post",
		},
	}
	return c.do(ctx, http.MethodPost, "/api/v1/repos/"+owner+"/"+repo+"/hooks", body, nil, "")
}

// BranchExists 判断仓库是否已有指定分支（用于判断是否需要写入模板）。
func (c *Client) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	var branches []struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+owner+"/"+repo+"/branches", nil, &branches, ""); err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, b := range branches {
		if b.Name == branch {
			return true, nil
		}
	}
	return false, nil
}

// BranchHead 返回分支最新提交，用于冻结时记录权威的 final commit。
func (c *Client) BranchHead(ctx context.Context, owner, repo, branch string) (string, error) {
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+owner+"/"+repo+"/branches/"+branch, nil, &out, ""); err != nil {
		return "", err
	}
	return out.Commit.ID, nil
}

// ProtectMain 保护 main 分支：禁止 force-push 与删除。
func (c *Client) ProtectMain(ctx context.Context, owner, repo string) error {
	body := map[string]any{
		"branch_name":                       "main",
		"enable_push":                       true,
		"enable_force_push":                 false,
		"enable_push_whitelist":             false,
		"enable_approvals_whitelist":        false,
		"enable_merge_whitelist":            false,
		"required_approvals":                0,
		"enable_status_check":               false,
		"block_on_rejected_reviews":         false,
		"block_on_official_review_requests": false,
		"block_on_outdated_branch":          false,
		"dismiss_stale_approvals":           false,
		"require_signed_commits":            false,
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/repos/"+owner+"/"+repo+"/branch_protections", body, nil, "")
	if err != nil && (IsStatus(err, http.StatusConflict) || IsStatus(err, http.StatusUnprocessableEntity)) {
		return nil
	}
	return err
}

// Archive 归档（只读）仓库。
func (c *Client) Archive(ctx context.Context, owner, repo string) error {
	body := map[string]any{"archived": true}
	return c.do(ctx, http.MethodPatch, "/api/v1/repos/"+owner+"/"+repo, body, nil, "")
}
