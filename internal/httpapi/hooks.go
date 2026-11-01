package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuin/goldmark"

	"exam.local/tools/internal/store"
)

func renderMarkdown(md goldmark.Markdown, src string) (string, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type giteaPushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Commits []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) handleGiteaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求失败")
		return
	}
	if s.cfg.Gitea.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "webhook_secret 未配置")
		return
	}
	signature := r.Header.Get("X-Gitea-Signature")
	if !validSignature(body, signature, s.cfg.Gitea.WebhookSecret) {
		writeError(w, http.StatusUnauthorized, "签名校验失败")
		return
	}
	if event := r.Header.Get("X-Gitea-Event"); event != "" && event != "push" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}

	var payload giteaPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "解析 payload 失败")
		return
	}
	owner, repoName := splitFullName(payload.Repository.FullName)
	if owner == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	candidate, err := s.st.CandidateByRepo(owner, repoName)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}

	sha := payload.After
	if gateZero(sha) && len(payload.Commits) > 0 {
		sha = payload.Commits[len(payload.Commits)-1].ID
	}
	files := map[string]struct{}{}
	for _, c := range payload.Commits {
		for _, p := range c.Added {
			files[p] = struct{}{}
		}
		for _, p := range c.Removed {
			files[p] = struct{}{}
		}
		for _, p := range c.Modified {
			files[p] = struct{}{}
		}
	}

	if err := s.st.AddPushEvent(&store.PushEvent{
		CandidateID:  candidate.ID,
		Ref:          payload.Ref,
		CommitSHA:    sha,
		ReceivedAt:   time.Now(),
		FilesChanged: len(files),
		Source:       "webhook",
	}); err != nil {
		s.logf("webhook: 记录推送失败: %v", err)
		writeError(w, http.StatusInternalServerError, "记录推送失败")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func validSignature(body []byte, signature, secret string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(signature)), []byte(want))
}

func splitFullName(full string) (string, string) {
	parts := strings.Split(full, "/")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func gateZero(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

type preReceiveRequest struct {
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

func (s *Server) handlePreReceive(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gitea.HookToken != "" {
		got := r.Header.Get("X-Exam-Hook-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Gitea.HookToken)) != 1 {
			writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "钩子令牌无效"})
			return
		}
	}

	var req preReceiveRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "推送信息格式错误"})
		return
	}

	owner, repoName := parseRepoPath(req.RepoPath)
	candidate, err := s.st.CandidateByRepo(owner, repoName)
	if err != nil {
		if s.cfg.Gitea.AllowUnknownRepos {
			writeJSON(w, http.StatusOK, map[string]any{"allow": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "未登记的仓库"})
		return
	}

	exam, err := s.st.GetExam(candidate.ExamID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "场次不存在"})
		return
	}
	now := time.Now()
	grace := time.Duration(s.cfg.Exam.FreezeGraceSeconds) * time.Second
	switch {
	case exam.Status == "frozen" || exam.Status == "closed":
		writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "考试已结束，推送被拒绝"})
		return
	case !exam.EndsAt.IsZero() && now.After(exam.EndsAt.Add(grace)):
		writeJSON(w, http.StatusOK, map[string]any{"allow": false, "reason": "考试已结束，推送被拒绝"})
		return
	}

	// 推送记录由 Gitea webhook（post 阶段）写入：pre-receive 阶段 ref 还没更新，
	// 在这里记录会把并发冲突中失败的推送也写成 push_event。
	writeJSON(w, http.StatusOK, map[string]any{"allow": true})
}

func (s *Server) handlePostReceive(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gitea.HookToken != "" {
		got := r.Header.Get("X-Exam-Hook-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Gitea.HookToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "钩子令牌无效")
			return
		}
	}
	var req preReceiveRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "推送信息格式错误")
		return
	}
	owner, repoName := parseRepoPath(req.RepoPath)
	candidate, err := s.st.CandidateByRepo(owner, repoName)
	if err != nil {
		// 练习仓库等未登记仓库不记录
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	if err := s.st.UpsertPushEvent(&store.PushEvent{
		CandidateID:  candidate.ID,
		Ref:          req.Ref,
		CommitSHA:    req.NewSHA,
		ReceivedAt:   time.Now(),
		LinesAdded:   req.LinesAdded,
		LinesDeleted: req.LinesDeleted,
		FilesChanged: req.FilesChanged,
		Source:       "post-receive",
	}); err != nil {
		s.logf("post-receive: 记录推送失败: %v", err)
		writeError(w, http.StatusInternalServerError, "记录推送失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func parseRepoPath(p string) (string, string) {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(filepath.ToSlash(p), "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}
