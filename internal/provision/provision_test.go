package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"exam.local/tools/internal/config"
	"exam.local/tools/internal/gitea"
	"exam.local/tools/internal/store"
)

func ensureGit(t *testing.T) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 不可用，跳过开通集成测试")
	}
	dirs := []string{filepath.Dir(gitBin)}
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		dirs = append(dirs, execPath, filepath.Join(filepath.Dir(filepath.Dir(execPath)), "bin"))
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join(dirs, sep)+sep+os.Getenv("PATH"))
}

func TestWebhookEndpoint(t *testing.T) {
	cfg := &config.Config{PublicURL: "http://10.0.0.10:8080"}
	if got := webhookEndpoint(cfg); got != "http://10.0.0.10:8080/api/hooks/gitea" {
		t.Fatalf("默认 webhook = %s", got)
	}
	cfg.Gitea.WebhookURL = "http://examd:8080/api/hooks/gitea"
	if got := webhookEndpoint(cfg); got != cfg.Gitea.WebhookURL {
		t.Fatalf("覆盖 webhook = %s", got)
	}
}

func TestProvisionAgainstMockGitea(t *testing.T) {
	ensureGit(t)

	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatal(err)
	}

	var (
		gotSudo    string
		gotHook    map[string]any
		gotProtect bool
		gotUser    bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		gotUser = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"login":"20231234"}`))
	})
	mux.HandleFunc("POST /api/v1/admin/users/{username}/repos", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "exam-2027-20231234",
			"full_name": "20231234/exam-2027-20231234",
			"clone_url": filepath.ToSlash(bare),
			"empty":     true,
			"private":   true,
		})
	})
	mux.HandleFunc("POST /api/v1/users/{username}/tokens", func(w http.ResponseWriter, r *http.Request) {
		if s := r.Header.Get("Sudo"); s != "" {
			gotSudo = s
		}
		if gotSudo != "" && r.Header.Get("Sudo") != "" {
			// 模拟不支持 Sudo 的 Gitea 版本，迫使客户端走本人口令回退。
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"sudo not supported"}`))
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "20231234" || pass == "" {
			t.Logf("token basic auth failed: ok=%v user=%q passLen=%d", ok, user, len(pass))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha1": "token-from-gitea"})
	})
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/hooks", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /api/v1/repos/{owner}/{repo}/hooks", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotHook)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/v1/repos/{owner}/{repo}/branch_protections", func(w http.ResponseWriter, r *http.Request) {
		gotProtect = true
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfgPath := filepath.Join(root, "config.yaml")
	cfgYAML := `
listen: "127.0.0.1:0"
public_url: "http://exam.local:8080"
db: "data/examd.db"
data_dir: "data"
gitea:
  url: "` + srv.URL + `"
  admin_user: "exam-admin"
  admin_password: "admin-pass"
  webhook_secret: "hook-secret"
  hook_token: "hook-token"
exam:
  repo_prefix: "exam-2027-"
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
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
	defer st.Close()
	if err := st.CreateExam(&store.Exam{ID: "2027-spring", Title: "e2e"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertCandidate(&store.Candidate{
		ExamID: "2027-spring", StudentNo: "20231234", Name: "张三", Track: "A", Variant: "A",
	}); err != nil {
		t.Fatal(err)
	}

	gc, err := gitea.New(srv.URL, "", "exam-admin", "admin-pass", "", false)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg, st, gc, t.Logf)
	summary, err := p.Run(context.Background(), "2027-spring")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Failed() != 0 {
		t.Fatalf("failed=%d: %+v", summary.Failed(), summary.Results)
	}
	if !gotUser || !gotProtect {
		t.Fatalf("user=%v protect=%v", gotUser, gotProtect)
	}
	if gotSudo != "20231234" {
		t.Fatalf("创建令牌未使用 Sudo 头: %q", gotSudo)
	}
	if gotHook == nil {
		t.Fatal("未创建 webhook")
	}
	hookCfg, _ := gotHook["config"].(map[string]any)
	if hookCfg["url"] != "http://exam.local:8080/api/hooks/gitea" || hookCfg["secret"] != "hook-secret" {
		t.Fatalf("webhook 配置错误: %+v", hookCfg)
	}

	slips := summary.Slips()
	if len(slips) != 1 || slips[0].Token != "token-from-gitea" {
		t.Fatalf("凭条错误: %+v", slips)
	}

	candidate, err := st.GetCandidate(1)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.TokenHash == "" || candidate.GiteaUser != "20231234" || candidate.RepoName != "exam-2027-20231234" {
		t.Fatalf("考生记录错误: %+v", candidate)
	}
	repoRec, err := st.GetRepoByCandidate(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repoRec.URL != filepath.ToSlash(bare) {
		t.Fatalf("仓库地址 = %s", repoRec.URL)
	}

	// 模板应已推送到远端 main
	r, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("远端没有 main: %v", err)
	}
	commit, err := r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "main.go", ".gitignore"} {
		if _, err := tree.File(name); err != nil {
			t.Fatalf("模板缺少 %s", name)
		}
	}
}
