// Package httpapi 提供考生页、管理端 API 与 Gitea 钩子。
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"

	"exam.local/tools/internal/backup"
	"exam.local/tools/internal/config"
	"exam.local/tools/internal/gitea"
	"exam.local/tools/internal/judge"
	"exam.local/tools/internal/provision"
	"exam.local/tools/internal/store"
	"exam.local/tools/web"
)

const (
	candidateCookie = "exam_session"
	adminCookie     = "exam_admin"
)

// Server 是 HTTP 服务。
type Server struct {
	cfg        *config.Config
	st         *store.Store
	gc         *gitea.Client
	judgeOpts  judge.Options
	md         goldmark.Markdown
	logf       func(format string, args ...any)
	freezeMu   sync.Mutex
	loginLimit *limiter
}

// New 创建 HTTP 服务。
func New(cfg *config.Config, st *store.Store, gc *gitea.Client, judgeOpts judge.Options, logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		cfg:        cfg,
		st:         st,
		gc:         gc,
		judgeOpts:  judgeOpts,
		md:         goldmark.New(),
		logf:       logf,
		loginLimit: newLimiter(10, time.Minute),
	}
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleCandidatePage)
	mux.HandleFunc("GET /admin", s.handleAdminPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now()})
	})

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/problems", s.handleProblems)
	mux.HandleFunc("GET /api/problems/{key}", s.handleProblem)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/notice", s.handleNotices)
	mux.HandleFunc("GET /api/consent", s.handleConsentGet)
	mux.HandleFunc("POST /api/consent", s.handleConsentPost)
	mux.HandleFunc("POST /api/agent/events", s.handleAgentEvents)

	mux.HandleFunc("POST /api/admin/auth/login", s.handleAdminLogin)
	mux.HandleFunc("POST /api/admin/auth/logout", s.handleAdminLogout)
	mux.HandleFunc("GET /api/admin/exams", s.adminOnly(s.handleAdminListExams))
	mux.HandleFunc("POST /api/admin/exams", s.adminOnly(s.handleAdminCreateExam))
	mux.HandleFunc("POST /api/admin/exams/{id}/roster", s.adminOnly(s.handleAdminRoster))
	mux.HandleFunc("POST /api/admin/exams/{id}/provision", s.adminOnly(s.handleAdminProvision))
	mux.HandleFunc("POST /api/admin/exams/{id}/start", s.adminOnly(s.handleAdminStart))
	mux.HandleFunc("POST /api/admin/exams/{id}/freeze", s.adminOnly(s.handleAdminFreeze))
	mux.HandleFunc("POST /api/admin/exams/{id}/close", s.adminOnly(s.handleAdminClose))
	mux.HandleFunc("POST /api/admin/exams/{id}/notice", s.adminOnly(s.handleAdminNotice))
	mux.HandleFunc("GET /api/admin/exams/{id}/live", s.adminOnly(s.handleAdminLive))
	mux.HandleFunc("GET /api/admin/exams/{id}/export.csv", s.adminOnly(s.handleAdminExport))
	mux.HandleFunc("POST /api/admin/exams/{id}/judge", s.adminOnly(s.handleAdminJudge))
	mux.HandleFunc("POST /api/admin/exams/{id}/backup", s.adminOnly(s.handleAdminBackup))

	mux.HandleFunc("POST /api/hooks/gitea", s.handleGiteaWebhook)
	mux.HandleFunc("POST /api/hooks/pre-receive", s.handlePreReceive)

	return s.logRequests(mux)
}

func (s *Server) handleCandidatePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(web.Candidate())
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(web.Admin())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	return dec.Decode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Server) secureCookies() bool {
	return strings.HasPrefix(strings.ToLower(s.cfg.PublicURL), "https://")
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secureCookies(),
		MaxAge:   maxAge,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	s.setCookie(w, name, "", -1)
}

type ctxKey int

const adminKey ctxKey = 1

func adminFrom(r *http.Request) *store.Admin {
	v := r.Context().Value(adminKey)
	if v == nil {
		return nil
	}
	a, _ := v.(*store.Admin)
	return a
}

func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}
		admin, err := s.st.AdminBySession(cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "会话已过期，请重新登录")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), adminKey, admin)))
	}
}

func (s *Server) currentCandidate(r *http.Request) (*store.Candidate, *store.Exam, error) {
	cookie, err := r.Cookie(candidateCookie)
	if err != nil {
		return nil, nil, store.ErrNotFound
	}
	c, e, err := s.st.CandidateBySession(cookie.Value)
	if err != nil {
		return nil, nil, err
	}
	return c, e, nil
}

// AutoFreezeLoop 到点自动冻结场次。
func (s *Server) AutoFreezeLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.autoFreeze(ctx)
		}
	}
}

func (s *Server) autoFreeze(ctx context.Context) {
	if !s.cfg.Exam.AutoFreeze {
		return
	}
	exams, err := s.st.ListExams()
	if err != nil {
		return
	}
	now := time.Now()
	grace := time.Duration(s.cfg.Exam.FreezeGraceSeconds) * time.Second
	for _, e := range exams {
		if e.Status != "running" || e.EndsAt.IsZero() {
			continue
		}
		if now.After(e.EndsAt.Add(grace)) {
			if _, _, err := s.FreezeExam(ctx, e.ID, "auto"); err != nil {
				s.logf("auto freeze %s failed: %v", e.ID, err)
			} else {
				s.logf("auto freeze %s done", e.ID)
			}
		}
	}
}

// FreezeExam 冻结场次、记录 final commit 并归档仓库。
func (s *Server) FreezeExam(ctx context.Context, examID, actor string) (int64, int, error) {
	s.freezeMu.Lock()
	defer s.freezeMu.Unlock()

	exam, err := s.st.GetExam(examID)
	if err != nil {
		return 0, 0, err
	}
	if exam.Status == "frozen" || exam.Status == "closed" {
		return 0, 0, nil
	}
	candidates, err := s.st.ListCandidates(examID)
	if err != nil {
		return 0, 0, err
	}

	// 以 Gitea 上的实际分支头为准记录 final commit：webhook 是异步的，
	// 手动冻结时可能还没投递到最后一次推送。
	var finalized int64
	if s.gc != nil {
		for _, c := range candidates {
			if c.GiteaUser == "" || c.RepoName == "" {
				continue
			}
			sha, err := s.gc.BranchHead(ctx, c.GiteaUser, c.RepoName, "main")
			if err != nil {
				s.logf("freeze: 读取 %s/%s 分支头失败: %v", c.GiteaUser, c.RepoName, err)
				continue
			}
			if sha == "" {
				continue
			}
			if err := s.st.SetRepoFinalCommit(c.ID, sha); err != nil {
				s.logf("freeze: 记录 %s 的 final commit 失败: %v", c.StudentNo, err)
				continue
			}
			finalized++
		}
	}
	filled, err := s.st.FinalizeExam(examID)
	if err != nil {
		return finalized, 0, err
	}
	finalized += filled

	archived := 0
	for _, c := range candidates {
		if c.GiteaUser == "" || c.RepoName == "" {
			continue
		}
		if s.gc == nil {
			break
		}
		if err := s.gc.Archive(ctx, c.GiteaUser, c.RepoName); err != nil {
			s.logf("archive %s/%s failed: %v", c.GiteaUser, c.RepoName, err)
			continue
		}
		_ = s.st.MarkRepoArchived(c.ID)
		archived++
	}
	if err := s.st.SetExamStatus(examID, "frozen"); err != nil {
		return finalized, archived, err
	}
	_ = s.st.AddAudit(examID, "admin", actor, "freeze", examID,
		fmt.Sprintf("finalized=%d archived=%d", finalized, archived))
	return finalized, archived, nil
}

// NewJudgeRunner 构造判题器（HTTP 与 CLI 共用）。
func (s *Server) NewJudgeRunner() *judge.Runner {
	return judge.New(s.judgeOpts, s.st)
}

// Provisioner 构造开通器。
func (s *Server) Provisioner() *provision.Provisioner {
	return provision.New(s.cfg, s.st, s.gc, s.logf)
}

// Backupper 执行备份。
func (s *Server) Backupper(out string) error {
	return backup.Create(s.st, s.cfg.DataDirPath(), out)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if sw.status >= 400 || r.Method != http.MethodGet {
			s.logf("%s %s -> %d", r.Method, r.URL.Path, sw.status)
		}
	})
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04", value)
}
