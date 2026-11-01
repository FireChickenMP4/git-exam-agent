package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"exam.local/tools/internal/config"
	"exam.local/tools/internal/judge"
	"exam.local/tools/internal/store"
)

type testEnv struct {
	srv       *httptest.Server
	handler   *Server
	st        *store.Store
	cfg       *config.Config
	candidate *store.Candidate
	exam      *store.Exam
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	yaml := `
listen: "127.0.0.1:0"
public_url: "http://example.test"
db: "examd.db"
data_dir: "data"
gitea:
  url: ""
  webhook_secret: "webhook-secret"
  hook_token: "hook-secret"
judge:
  runtime: local
  allow_local: true
  go_bin: go
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAdmin("admin", string(hash)); err != nil {
		t.Fatal(err)
	}

	exam := &store.Exam{
		ID: "2027-spring", Title: "2027 春季招新机试",
		Status: "running", StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour),
	}
	if err := st.CreateExam(exam); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertProblem(&store.Problem{
		ExamID: exam.ID, Key: "sum", Title: "求和", Score: 100, TimeLimitMS: 2000,
		StatementMD: "# 求和\n\n读入两个整数。",
		Variants:    []store.Variant{{Seed: "A", StatementMD: "# 求和 A 卷\n\nA 卷内容"}},
		Cases:       []store.TestCase{{CaseNo: 1, InputPath: "sum/1.in", ExpectedPath: "sum/1.out", Score: 100, IsSample: true}},
	}); err != nil {
		t.Fatal(err)
	}
	cid, err := st.UpsertCandidate(&store.Candidate{
		ExamID: exam.ID, StudentNo: "20231234", Name: "张三", Track: "A", Variant: "A",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCandidateToken(cid, sha256Hex("token-abc")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCandidateRepo(cid, "20231234", "exam-2027-20231234"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRepo(cid, "http://gitea/20231234/exam-2027-20231234.git"); err != nil {
		t.Fatal(err)
	}
	candidate, err := st.GetCandidate(cid)
	if err != nil {
		t.Fatal(err)
	}

	handler := New(cfg, st, nil, judge.Options{Runtime: "local", GoBin: "go"}, t.Logf)
	ts := httptest.NewServer(handler.Handler())
	t.Cleanup(ts.Close)

	return &testEnv{srv: ts, handler: handler, st: st, cfg: cfg, candidate: candidate, exam: exam}
}

func (e *testEnv) client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Exam-Admin", "1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func TestCandidateFlow(t *testing.T) {
	env := newTestEnv(t)
	client := env.client(t)

	status, _, _ := doJSON(t, client, http.MethodPost, env.srv.URL+"/api/login", map[string]string{
		"student_no": "20231234", "token": "wrong",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("错误令牌登录返回 %d", status)
	}

	status, me, _ := doJSON(t, client, http.MethodPost, env.srv.URL+"/api/login", map[string]string{
		"student_no": "20231234", "token": "token-abc",
	})
	if status != http.StatusOK || me["student_no"] != "20231234" || me["exam_title"] != env.exam.Title {
		t.Fatalf("登录失败: %d %+v", status, me)
	}

	status, problems, _ := doJSON(t, client, http.MethodGet, env.srv.URL+"/api/problems", nil)
	if status != http.StatusOK {
		t.Fatalf("题目列表 %d", status)
	}
	list, _ := problems["problems"].([]any)
	if len(list) != 1 {
		t.Fatalf("problems = %v", problems)
	}

	status, detail, header := doJSON(t, client, http.MethodGet, env.srv.URL+"/api/problems/sum", nil)
	if status != http.StatusOK {
		t.Fatalf("题面 %d", status)
	}
	html, _ := detail["statement_html"].(string)
	if !strings.Contains(html, "A 卷内容") {
		t.Fatalf("未返回本人变体: %s", html)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", header.Get("Cache-Control"))
	}

	status, consent, _ := doJSON(t, client, http.MethodGet, env.srv.URL+"/api/consent", nil)
	if status != http.StatusOK || consent["accepted"] != false {
		t.Fatalf("consent 初始状态错误: %+v", consent)
	}
	if status, _, _ := doJSON(t, client, http.MethodPost, env.srv.URL+"/api/consent", nil); status != http.StatusOK {
		t.Fatalf("同意返回 %d", status)
	}
	_, consent, _ = doJSON(t, client, http.MethodGet, env.srv.URL+"/api/consent", nil)
	if consent["accepted"] != true {
		t.Fatalf("同意未记录: %+v", consent)
	}

	status, st, _ := doJSON(t, client, http.MethodGet, env.srv.URL+"/api/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if secs, _ := st["seconds_left"].(float64); secs <= 0 {
		t.Fatalf("seconds_left = %v", st["seconds_left"])
	}

	status, _, _ = doJSON(t, client, http.MethodGet, env.srv.URL+"/api/me", nil)
	if status != http.StatusOK {
		t.Fatalf("me %d", status)
	}

	// agent 事件上报：令牌鉴权，写入 agent_event
	eventBody := map[string]any{
		"student_no": "20231234",
		"token":      "token-abc",
		"events": []map[string]any{{
			"t":       time.Now().Format(time.RFC3339),
			"type":    "start",
			"payload": map[string]any{"repo": "exam"},
		}},
	}
	status, evRes, _ := doJSON(t, client, http.MethodPost, env.srv.URL+"/api/agent/events", eventBody)
	if status != http.StatusOK || evRes["stored"].(float64) != 1 {
		t.Fatalf("agent 事件上报失败: %d %+v", status, evRes)
	}
	lastEvent, err := env.st.LastAgentEvent(env.candidate.ID)
	if err != nil || lastEvent.Type != "start" {
		t.Fatalf("agent_event 未落库: %+v %v", lastEvent, err)
	}

	eventBody["token"] = "wrong"
	status, _, _ = doJSON(t, client, http.MethodPost, env.srv.URL+"/api/agent/events", eventBody)
	if status != http.StatusUnauthorized {
		t.Fatalf("错误令牌上报应 401，实际 %d", status)
	}
}

func TestAdminFlowAndHooks(t *testing.T) {
	env := newTestEnv(t)
	admin := env.client(t)

	status, _, _ := doJSON(t, admin, http.MethodPost, env.srv.URL+"/api/admin/auth/login", map[string]string{
		"username": "admin", "password": "wrong",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("错误密码返回 %d", status)
	}
	if status, _, _ := doJSON(t, admin, http.MethodPost, env.srv.URL+"/api/admin/auth/login", map[string]string{
		"username": "admin", "password": "admin-pass",
	}); status != http.StatusOK {
		t.Fatalf("管理员登录 %d", status)
	}

	// 名单导入
	status, rosterRes, _ := doJSON(t, admin, http.MethodPost, env.srv.URL+"/api/admin/exams/2027-spring/roster", map[string]string{
		"csv": "student_no,name,track,variant\n20231235,李四,B,B\n20231236,,B,B\n",
	})
	if status != http.StatusOK {
		t.Fatalf("名单导入 %d", status)
	}
	if rosterRes["imported"].(float64) != 1 {
		t.Fatalf("imported = %v", rosterRes)
	}
	errs, _ := rosterRes["errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("errors = %v", rosterRes["errors"])
	}

	// 公告
	if status, _, _ := doJSON(t, admin, http.MethodPost, env.srv.URL+"/api/admin/exams/2027-spring/notice", map[string]string{
		"content": "距离结束还有 30 分钟",
	}); status != http.StatusOK {
		t.Fatalf("发布公告 %d", status)
	}

	// Gitea webhook：错误签名拒绝
	payload := []byte(`{"ref":"refs/heads/main","after":"deadbeef","commits":[{"id":"deadbeef","modified":["main.go"]}],"repository":{"full_name":"20231234/exam-2027-20231234"}}`)
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/gitea", bytes.NewReader(payload))
	req.Header.Set("X-Gitea-Signature", "bad")
	resp, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误签名返回 %d", resp.StatusCode)
	}

	mac := hmac.New(sha256.New, []byte(env.cfg.Gitea.WebhookSecret))
	mac.Write(payload)
	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/gitea", bytes.NewReader(payload))
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Gitea-Event", "push")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("正确签名返回 %d", resp.StatusCode)
	}
	if n, err := env.st.CountPushEvents(env.candidate.ID); err != nil || n != 1 {
		t.Fatalf("push events = %d, %v", n, err)
	}

	// 非 main 分支的 webhook 应被忽略
	devPayload := []byte(`{"ref":"refs/heads/dev","after":"deadbeef","commits":[{"id":"deadbeef","modified":["main.go"]}],"repository":{"full_name":"20231234/exam-2027-20231234"}}`)
	devMac := hmac.New(sha256.New, []byte(env.cfg.Gitea.WebhookSecret))
	devMac.Write(devPayload)
	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/gitea", bytes.NewReader(devPayload))
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(devMac.Sum(nil)))
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("非 main webhook 返回 %d", resp.StatusCode)
	}
	if n, _ := env.st.CountPushEvents(env.candidate.ID); n != 1 {
		t.Fatalf("非 main webhook 不应记录，count=%d", n)
	}

	// 管理端写接口缺少自定义头应 403（CSRF 防护）
	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/admin/exams/2027-spring/start", nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无 X-Exam-Admin 头应 403，实际 %d", resp.StatusCode)
	}

	// post-receive 后到：补全行数但不重复记录
	pushStats := map[string]any{
		"repo_path":     "/data/git/repositories/20231234/exam-2027-20231234.git",
		"ref":           "refs/heads/main",
		"old_sha":       strings.Repeat("1", 40),
		"new_sha":       "deadbeef",
		"files_changed": 1,
		"lines_added":   42,
		"lines_deleted": 7,
	}
	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/push", mustJSON(t, pushStats))
	req.Header.Set("X-Exam-Hook-Token", "hook-secret")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-receive 返回 %d", resp.StatusCode)
	}
	if n, _ := env.st.CountPushEvents(env.candidate.ID); n != 1 {
		t.Fatalf("post-receive 不应新增记录，count=%d", n)
	}
	lastPush, err := env.st.LastPushEvent(env.candidate.ID)
	if err != nil || lastPush.LinesAdded != 42 || lastPush.LinesDeleted != 7 {
		t.Fatalf("行数未补全: %+v %v", lastPush, err)
	}

	// pre-receive：考试进行中允许
	pre := map[string]any{
		"repo_path":     "/data/git/repositories/20231234/exam-2027-20231234.git",
		"ref":           "refs/heads/main",
		"old_sha":       strings.Repeat("1", 40),
		"new_sha":       "deadbeef",
		"files_changed": 1,
	}
	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/pre-receive", mustJSON(t, pre))
	req.Header.Set("X-Exam-Hook-Token", "hook-secret")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var preRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&preRes)
	resp.Body.Close()
	if preRes["allow"] != true {
		t.Fatalf("进行中应允许推送: %+v", preRes)
	}

	// 看板 + 报表
	status, live, _ := doJSON(t, admin, http.MethodGet, env.srv.URL+"/api/admin/exams/2027-spring/live", nil)
	if status != http.StatusOK {
		t.Fatalf("live %d", status)
	}
	rows, _ := live["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("live rows = %d", len(rows))
	}
	status, _, header := doJSON(t, admin, http.MethodGet, env.srv.URL+"/api/admin/exams/2027-spring/export.csv", nil)
	if status != http.StatusOK || !strings.Contains(header.Get("Content-Type"), "text/csv") {
		t.Fatalf("export %d %q", status, header.Get("Content-Type"))
	}

	// 冻结：记录 final commit，之后推送被拒
	finalized, archived, err := env.handler.FreezeExam(context.Background(), "2027-spring", "test")
	if err != nil {
		t.Fatal(err)
	}
	if finalized != 1 || archived != 0 {
		t.Fatalf("finalized=%d archived=%d", finalized, archived)
	}
	repo, err := env.st.GetRepoByCandidate(env.candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.FinalCommit != "deadbeef" {
		t.Fatalf("final_commit = %s", repo.FinalCommit)
	}

	// 模拟冻结中途失败：状态停在 freezing、final 为空，重试应能补齐
	if err := env.st.SetExamStatus("2027-spring", "freezing"); err != nil {
		t.Fatal(err)
	}
	if err := env.st.SetRepoFinalCommit(env.candidate.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.handler.FreezeExam(context.Background(), "2027-spring", "retry"); err != nil {
		t.Fatalf("续跑冻结失败: %v", err)
	}
	resumed, err := env.st.GetRepoByCandidate(env.candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.FinalCommit != "deadbeef" {
		t.Fatalf("续跑后 final_commit = %s", resumed.FinalCommit)
	}
	examNow, err := env.st.GetExam("2027-spring")
	if err != nil || examNow.Status != "frozen" {
		t.Fatalf("续跑后状态 = %+v %v", examNow, err)
	}

	req, _ = http.NewRequest(http.MethodPost, env.srv.URL+"/api/hooks/pre-receive", mustJSON(t, pre))
	req.Header.Set("X-Exam-Hook-Token", "hook-secret")
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	preRes = nil
	_ = json.NewDecoder(resp.Body).Decode(&preRes)
	resp.Body.Close()
	if preRes["allow"] != false {
		t.Fatalf("冻结后应拒绝推送: %+v", preRes)
	}
	if reason, _ := preRes["reason"].(string); !strings.Contains(reason, "考试已结束") {
		t.Fatalf("拒绝原因 = %q", reason)
	}
}

func TestAdminRequiresSession(t *testing.T) {
	env := newTestEnv(t)
	client := env.client(t)
	status, _, _ := doJSON(t, client, http.MethodGet, env.srv.URL+"/api/admin/exams", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录访问管理端返回 %d", status)
	}
	status, _, _ = doJSON(t, client, http.MethodGet, env.srv.URL+"/api/me", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录访问考生端返回 %d", status)
	}
}

func TestProblemsVisible(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	now := time.Now()
	cases := []struct {
		name  string
		exam  store.Exam
		visit bool
	}{
		{"draft 未开始", store.Exam{Status: "draft", StartsAt: now.Add(-time.Hour)}, false},
		{"running 未到时间", store.Exam{Status: "running", StartsAt: now.Add(time.Hour)}, false},
		{"running 已到时间", store.Exam{Status: "running", StartsAt: now.Add(-time.Minute)}, true},
		{"frozen 已开考", store.Exam{Status: "frozen", StartsAt: now.Add(-time.Hour)}, true},
	}
	for _, c := range cases {
		if got := s.problemsVisible(&c.exam); got != c.visit {
			t.Errorf("%s: visible=%v want %v", c.name, got, c.visit)
		}
	}
	s.cfg.Exam.ProblemVisibleBeforeMin = 10
	if !s.problemsVisible(&store.Exam{Status: "running", StartsAt: now.Add(5 * time.Minute)}) {
		t.Error("配置提前 10 分钟可见时应可见")
	}
}

func mustJSON(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}
