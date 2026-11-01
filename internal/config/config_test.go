package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
public_url: "${TEST_PUBLIC_URL}"
db: "db/examd.db"
data_dir: "data"
gitea:
  url: "http://127.0.0.1:3000"
  admin_password: "${TEST_ADMIN_PASSWORD}"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PUBLIC_URL", "http://10.0.0.10:8080")
	t.Setenv("TEST_ADMIN_PASSWORD", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "http://10.0.0.10:8080" || cfg.Gitea.AdminPassword != "secret" {
		t.Fatalf("环境变量展开失败: %+v", cfg)
	}
	if cfg.Listen != ":8080" || cfg.Judge.Runtime != "docker" || cfg.Exam.FreezeGraceSeconds != 30 {
		t.Fatalf("默认值未生效: %+v", cfg)
	}
	if cfg.DBPath() != filepath.Join(dir, "db", "examd.db") {
		t.Fatalf("DBPath = %s", cfg.DBPath())
	}
	if cfg.JudgeWorkDir() != filepath.Join(dir, "data", "judge") {
		t.Fatalf("JudgeWorkDir = %s", cfg.JudgeWorkDir())
	}
	missing := cfg.SessionSecretMissing()
	if len(missing) != 2 {
		t.Fatalf("应提示缺少两个密钥: %v", missing)
	}
}

func TestLoadValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	base := `
listen: ":0"
db: "examd.db"
data_dir: "data"
gitea:
  webhook_secret: "a"
  hook_token: "b"
judge:
  runtime: local
  allow_local: true
`
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}

	badRuntime := `
listen: ":0"
db: "examd.db"
data_dir: "data"
gitea:
  webhook_secret: "a"
  hook_token: "b"
judge:
  runtime: wasm
`
	if err := os.WriteFile(path, []byte(badRuntime), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("错误 runtime 应被拒绝")
	}

	noAllow := strings.Replace(base, "  allow_local: true\n", "", 1)
	if err := os.WriteFile(path, []byte(noAllow), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("runtime=local 未开启 allow_local 应被拒绝")
	}
}
