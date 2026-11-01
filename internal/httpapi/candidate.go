package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"exam.local/tools/internal/store"
)

type meResponse struct {
	StudentNo  string    `json:"student_no"`
	Name       string    `json:"name"`
	Track      string    `json:"track"`
	Variant    string    `json:"variant"`
	ExamID     string    `json:"exam_id"`
	ExamTitle  string    `json:"exam_title"`
	ExamStatus string    `json:"exam_status"`
	RepoURL    string    `json:"repo_url"`
	ServerTime time.Time `json:"server_time"`
}

type loginRequest struct {
	StudentNo string `json:"student_no"`
	Token     string `json:"token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow("candidate:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.StudentNo == "" || req.Token == "" {
		writeError(w, http.StatusBadRequest, "请输入学号和令牌")
		return
	}

	candidate, exam, err := s.st.FindCandidateByStudentNo(req.StudentNo)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "学号或令牌错误")
		return
	}
	if candidate.TokenHash == "" {
		writeError(w, http.StatusForbidden, "账号尚未开通，请联系监考")
		return
	}
	if subtle.ConstantTimeCompare([]byte(sha256Hex(req.Token)), []byte(candidate.TokenHash)) != 1 {
		writeError(w, http.StatusUnauthorized, "学号或令牌错误")
		return
	}
	if exam.Status == "closed" {
		writeError(w, http.StatusForbidden, "考试已结束")
		return
	}

	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成会话失败")
		return
	}
	expires := time.Now().Add(12 * time.Hour)
	if !exam.EndsAt.IsZero() && exam.EndsAt.Add(2*time.Hour).Before(expires) {
		expires = exam.EndsAt.Add(2 * time.Hour)
	}
	if err := s.st.CreateCandidateSession(candidate.ID, token, expires, clientIP(r), r.UserAgent()); err != nil {
		writeError(w, http.StatusInternalServerError, "保存会话失败")
		return
	}
	s.setCookie(w, candidateCookie, token, int(time.Until(expires).Seconds()))
	writeJSON(w, http.StatusOK, s.mePayload(candidate, exam))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(candidateCookie); err == nil {
		_ = s.st.DeleteSession("candidate_session", cookie.Value)
	}
	s.clearCookie(w, candidateCookie)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) mePayload(c *store.Candidate, e *store.Exam) meResponse {
	out := meResponse{
		StudentNo:  c.StudentNo,
		Name:       c.Name,
		Track:      c.Track,
		Variant:    c.Variant,
		ExamID:     e.ID,
		ExamTitle:  e.Title,
		ExamStatus: e.Status,
		ServerTime: time.Now(),
	}
	if repo, err := s.st.GetRepoByCandidate(c.ID); err == nil {
		out.RepoURL = repo.URL
	}
	return out
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, e, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	writeJSON(w, http.StatusOK, s.mePayload(c, e))
}

func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	_, e, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	problems, err := s.st.ListProblems(e.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		Key   string `json:"key"`
		Title string `json:"title"`
		Score int    `json:"score"`
	}
	items := make([]item, 0, len(problems))
	for _, p := range problems {
		items = append(items, item{Key: p.Key, Title: p.Title, Score: p.Score})
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": items})
}

func (s *Server) handleProblem(w http.ResponseWriter, r *http.Request) {
	c, e, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	p, err := s.st.ProblemByKey(e.ID, r.PathValue("key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "题目不存在")
		return
	}

	statement := p.StatementMD
	for _, v := range p.Variants {
		if v.Seed == c.Variant {
			statement = v.StatementMD
			break
		}
	}
	html, err := renderMarkdown(s.md, statement)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "渲染题面失败")
		return
	}

	type sample struct {
		CaseNo   int    `json:"case_no"`
		Input    string `json:"input"`
		Expected string `json:"expected"`
	}
	var samples []sample
	for _, tc := range p.Cases {
		if !tc.IsSample {
			continue
		}
		samples = append(samples, sample{
			CaseNo:   tc.CaseNo,
			Input:    readCapped(s.dataPath(tc.InputPath), 64<<10),
			Expected: readCapped(s.dataPath(tc.ExpectedPath), 64<<10),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"key":            p.Key,
		"title":          p.Title,
		"score":          p.Score,
		"statement_html": html,
		"samples":        samples,
	})
}

func (s *Server) dataPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.cfg.DataDirPath(), p)
}

func readCapped(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	return string(buf[:n])
}

func statusText(status string) string {
	switch status {
	case "running":
		return "进行中"
	case "frozen":
		return "已结束（已冻结）"
	case "closed":
		return "已封存"
	default:
		return "未开始"
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	c, e, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	secondsLeft := 0
	if !e.EndsAt.IsZero() {
		secondsLeft = int(time.Until(e.EndsAt).Seconds())
		if secondsLeft < 0 {
			secondsLeft = 0
		}
	}

	var lastPush *time.Time
	pushCount := 0
	if repo, err := s.st.GetRepoByCandidate(c.ID); err == nil && !repo.LastPushAt.IsZero() {
		t := repo.LastPushAt
		lastPush = &t
	}
	if n, err := s.st.CountPushEvents(c.ID); err == nil {
		pushCount = n
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"exam": map[string]any{
			"id":          e.ID,
			"title":       e.Title,
			"status":      e.Status,
			"status_text": statusText(e.Status),
			"starts_at":   e.StartsAt,
			"ends_at":     e.EndsAt,
		},
		"now":          time.Now(),
		"seconds_left": secondsLeft,
		"last_push_at": lastPush,
		"push_count":   pushCount,
	})
}

func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	_, e, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	notices, err := s.st.ListNotices(e.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		T       time.Time `json:"t"`
		Content string    `json:"content"`
	}
	items := make([]item, 0, len(notices))
	for _, n := range notices {
		items = append(items, item{T: n.T, Content: n.Content})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notices": items})
}

func (s *Server) handleConsentGet(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	accepted, err := s.st.HasConsent(c.ID, s.cfg.Exam.ConsentVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.cfg.Exam.ConsentVersion,
		"accepted": accepted,
	})
}

func (s *Server) handleConsentPost(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.currentCandidate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	if err := s.st.AcceptConsent(c.ID, s.cfg.Exam.ConsentVersion, clientIP(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.Exam.ConsentVersion})
}

type agentEventPayload struct {
	T       string         `json:"t"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

type agentEventsRequest struct {
	StudentNo string              `json:"student_no"`
	Token     string              `json:"token"`
	Events    []agentEventPayload `json:"events"`
}

// handleAgentEvents 接收 agent 上报的运行事件（用考生令牌鉴权，与页面会话无关）。
func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	var req agentEventsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.StudentNo == "" || req.Token == "" || len(req.Events) == 0 {
		writeError(w, http.StatusBadRequest, "缺少学号、令牌或事件")
		return
	}
	if len(req.Events) > 100 {
		req.Events = req.Events[:100]
	}

	candidate, _, err := s.st.FindCandidateByStudentNo(req.StudentNo)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "学号或令牌错误")
		return
	}
	if subtle.ConstantTimeCompare([]byte(sha256Hex(req.Token)), []byte(candidate.TokenHash)) != 1 {
		writeError(w, http.StatusUnauthorized, "学号或令牌错误")
		return
	}

	stored := 0
	for _, ev := range req.Events {
		if ev.Type == "" {
			continue
		}
		when := time.Now()
		if ev.T != "" {
			if parsed, err := time.Parse(time.RFC3339, ev.T); err == nil {
				when = parsed
			}
		}
		payload, _ := json.Marshal(ev.Payload)
		if err := s.st.AddAgentEvent(candidate.ID, when, ev.Type, string(payload)); err != nil {
			s.logf("agent event: 保存失败: %v", err)
			continue
		}
		stored++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stored": stored})
}
