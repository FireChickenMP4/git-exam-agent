package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"exam.local/tools/internal/judge"
	"exam.local/tools/internal/report"
	"exam.local/tools/internal/roster"
	"exam.local/tools/internal/store"
)

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow("admin:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	var req adminLoginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	admin, err := s.st.GetAdmin(req.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if admin.Disabled {
		writeError(w, http.StatusForbidden, "账号已禁用")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成会话失败")
		return
	}
	expires := time.Now().Add(12 * time.Hour)
	if err := s.st.CreateAdminSession(admin.ID, token, expires, clientIP(r), r.UserAgent()); err != nil {
		writeError(w, http.StatusInternalServerError, "保存会话失败")
		return
	}
	s.setCookie(w, adminCookie, token, int(time.Until(expires).Seconds()))
	_ = s.st.AddAudit("", "admin", req.Username, "login", "", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"username": admin.Username})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminCookie); err == nil {
		_ = s.st.DeleteSession("admin_session", cookie.Value)
	}
	s.clearCookie(w, adminCookie)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func examJSON(e store.Exam) map[string]any {
	return map[string]any{
		"id":        e.ID,
		"title":     e.Title,
		"status":    e.Status,
		"starts_at": e.StartsAt,
		"ends_at":   e.EndsAt,
		"timezone":  e.Timezone,
	}
}

func (s *Server) handleAdminListExams(w http.ResponseWriter, r *http.Request) {
	exams, err := s.st.ListExams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(exams))
	for _, e := range exams {
		out = append(out, examJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"exams": out, "now": time.Now()})
}

type createExamRequest struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func (s *Server) handleAdminCreateExam(w http.ResponseWriter, r *http.Request) {
	var req createExamRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !validID(req.ID) {
		writeError(w, http.StatusBadRequest, "场次 ID 只能包含字母、数字、下划线和短横线")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "标题不能为空")
		return
	}
	starts, err := parseTime(req.StartsAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "开始时间格式错误")
		return
	}
	ends, err := parseTime(req.EndsAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "结束时间格式错误")
		return
	}
	if starts.IsZero() {
		starts = time.Now()
	}
	if ends.IsZero() {
		ends = starts.Add(2 * time.Hour)
	}
	if !ends.After(starts) {
		writeError(w, http.StatusBadRequest, "结束时间必须晚于开始时间")
		return
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	exam := &store.Exam{ID: req.ID, Title: strings.TrimSpace(req.Title), StartsAt: starts, EndsAt: ends}
	if err := s.st.CreateExam(exam); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "exam.create", exam.ID, "")
	writeJSON(w, http.StatusOK, examJSON(*exam))
}

func (s *Server) examFromPath(w http.ResponseWriter, r *http.Request) (*store.Exam, bool) {
	exam, err := s.st.GetExam(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "场次不存在")
		return nil, false
	}
	return exam, true
}

type rosterRequest struct {
	CSV string `json:"csv"`
}

func (s *Server) handleAdminRoster(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	var req rosterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	parsed, err := roster.Parse(strings.NewReader(req.CSV))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	imported := 0
	for _, row := range parsed.Rows {
		_, err := s.st.UpsertCandidate(&store.Candidate{
			ExamID:    exam.ID,
			StudentNo: row.StudentNo,
			Name:      row.Name,
			Track:     row.Track,
			Variant:   row.Variant,
		})
		if err != nil {
			parsed.Errors = append(parsed.Errors, roster.RowError{Line: row.Line, Reason: err.Error()})
			continue
		}
		imported++
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "roster.import", exam.ID, fmt.Sprintf("imported=%d errors=%d", imported, len(parsed.Errors)))

	errs := make([]map[string]any, 0, len(parsed.Errors))
	for _, e := range parsed.Errors {
		errs = append(errs, map[string]any{"line": e.Line, "reason": e.Reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "errors": errs})
}

func (s *Server) handleAdminProvision(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	if s.gc == nil {
		writeError(w, http.StatusBadRequest, "未配置 Gitea")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()

	summary, err := s.Provisioner().Run(ctx, exam.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	type item struct {
		StudentNo   string `json:"student_no"`
		Name        string `json:"name"`
		RepoURL     string `json:"repo_url"`
		Token       string `json:"token,omitempty"`
		UserCreated bool   `json:"user_created"`
		RepoSeeded  bool   `json:"repo_seeded"`
		Error       string `json:"error,omitempty"`
	}
	items := make([]item, 0, len(summary.Results))
	created, seeded := 0, 0
	for _, res := range summary.Results {
		it := item{
			StudentNo:   res.StudentNo,
			Name:        res.Name,
			RepoURL:     res.RepoURL,
			Token:       res.Token,
			UserCreated: res.UserCreated,
			RepoSeeded:  res.RepoSeeded,
		}
		if res.Err != nil {
			it.Error = res.Err.Error()
		}
		if res.UserCreated {
			created++
		}
		if res.RepoSeeded {
			seeded++
		}
		items = append(items, it)
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "provision", exam.ID, fmt.Sprintf("total=%d failed=%d", len(items), summary.Failed()))
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   len(items),
		"created": created,
		"seeded":  seeded,
		"failed":  summary.Failed(),
		"slips":   summary.Slips(),
		"results": items,
	})
}

func (s *Server) handleAdminStart(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	if exam.Status != "draft" {
		writeError(w, http.StatusBadRequest, "只有未开始的场次可以开考")
		return
	}
	if !exam.EndsAt.IsZero() && time.Now().After(exam.EndsAt) {
		writeError(w, http.StatusBadRequest, "结束时间已过，请先调整场次时间")
		return
	}
	if err := s.st.SetExamStatus(exam.ID, "running"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "exam.start", exam.ID, "")
	exam.Status = "running"
	writeJSON(w, http.StatusOK, examJSON(*exam))
}

func (s *Server) handleAdminFreeze(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	finalized, archived, err := s.FreezeExam(r.Context(), exam.ID, actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"finalized": finalized,
		"archived":  archived,
		"status":    "frozen",
	})
}

func (s *Server) handleAdminClose(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	if err := s.st.SetExamStatus(exam.ID, "closed"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "exam.close", exam.ID, "")
	exam.Status = "closed"
	writeJSON(w, http.StatusOK, examJSON(*exam))
}

type noticeRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleAdminNotice(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	var req noticeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "公告内容不能为空")
		return
	}
	admin := adminFrom(r)
	var author int64
	if admin != nil {
		author = admin.ID
	}
	if err := s.st.AddNotice(exam.ID, strings.TrimSpace(req.Content), author); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminLive(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	rows, err := s.st.LiveRows(exam.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		level := ""
		switch {
		case row.Archived:
			level = "ok"
		case row.LastPushAt.IsZero():
			if exam.Status == "running" && !exam.StartsAt.IsZero() && now.Sub(exam.StartsAt) > 2*time.Minute {
				level = "warn"
			}
		default:
			gap := now.Sub(row.LastPushAt)
			switch {
			case gap > 3*time.Minute:
				level = "gap"
			case gap > 2*time.Minute:
				level = "warn"
			}
		}
		gapSeconds := 0
		if !row.LastPushAt.IsZero() {
			gapSeconds = int(now.Sub(row.LastPushAt).Seconds())
		}
		out = append(out, map[string]any{
			"student_no":      row.StudentNo,
			"name":            row.Name,
			"track":           row.Track,
			"status":          row.Status,
			"repo_url":        row.RepoURL,
			"final_commit":    row.FinalCommit,
			"last_push_at":    row.LastPushAt,
			"push_count":      row.PushCount,
			"gap_seconds":     gapSeconds,
			"level":           level,
			"last_agent_at":   row.LastAgentAt,
			"last_agent_type": row.LastAgentType,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out, "now": now, "exam": examJSON(*exam)})
}

func (s *Server) handleAdminExport(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	data, err := report.Build(s.st, exam.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+exam.ID+`-report.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleAdminJudge(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()

	results, err := s.NewJudgeRunner().RunExam(ctx, exam.ID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	problems, err := s.st.ListProblems(exam.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dir := filepath.Join(s.cfg.DataDirPath(), "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	csvPath := filepath.Join(dir, exam.ID+"-"+time.Now().Format("20060102-150405")+".csv")
	f, err := os.Create(csvPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	csvErr := judge.WriteCSV(f, results, problems)
	_ = f.Close()
	if csvErr != nil {
		writeError(w, http.StatusInternalServerError, csvErr.Error())
		return
	}

	accepted := 0
	for _, res := range results {
		if judge.Accepted(res) {
			accepted++
		}
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "judge", exam.ID, fmt.Sprintf("candidates=%d", len(results)))
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": len(results),
		"ok":         accepted,
		"csv":        csvPath,
	})
}

func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	exam, ok := s.examFromPath(w, r)
	if !ok {
		return
	}
	dir := filepath.Join(s.cfg.DataDirPath(), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := filepath.Join(dir, fmt.Sprintf("%s-%s.tar.gz", exam.ID, time.Now().Format("20060102-150405")))
	if err := s.Backupper(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	admin := adminFrom(r)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	_ = s.st.AddAudit(exam.ID, "admin", actor, "backup", out, "")
	writeJSON(w, http.StatusOK, map[string]any{"path": out})
}
