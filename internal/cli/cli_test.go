package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := `
listen: "127.0.0.1:0"
public_url: "http://127.0.0.1:8080"
db: "data/examd.db"
data_dir: "data"
judge:
  runtime: local
  allow_local: true
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := Run(args, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, out, _ := run("version"); code != 0 || !strings.Contains(out, "examd") {
		t.Fatalf("version code=%d out=%s", code, out)
	}
	if code, _, _ := run("nope"); code != 2 {
		t.Fatalf("未知命令退出码 = %d", code)
	}
	if code, _, errOut := run("admin", "-config", cfgPath, "-username", "admin", "-password", "pass"); code != 0 {
		t.Fatalf("创建管理员失败: %s", errOut)
	}
	if code, out, errOut := run("exam", "-config", cfgPath, "-id", "2027-spring",
		"-title", "测试场次", "-starts-at", "2027-02-15T14:00:00+08:00",
		"-ends-at", "2027-02-15T16:00:00+08:00"); code != 0 {
		t.Fatalf("创建场次失败: %s %s", out, errOut)
	}

	rosterPath := filepath.Join(dir, "roster.csv")
	rosterCSV := "student_no,name,track,variant\n20231234,张三,A,A\n20231235,李四,B,B\n2023999,,C,C\n"
	if err := os.WriteFile(rosterPath, []byte(rosterCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run("roster", "-config", cfgPath, "-exam", "2027-spring", "-csv", rosterPath)
	if code != 0 {
		t.Fatalf("导入名单失败: %s", errOut)
	}
	if !strings.Contains(out, "imported=2 skipped=1") {
		t.Fatalf("名单统计错误: %s", out)
	}

	problemRoot := filepath.Join(dir, "problems")
	for _, f := range []struct{ name, content string }{
		{"sum/problem.md", "# 求和\n"},
		{"sum/cases/01.in", "1 2\n"},
		{"sum/cases/01.out", "3\n"},
	} {
		p := filepath.Join(problemRoot, f.name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errOut := run("problem", "-config", cfgPath, "-exam", "2027-spring", "-dir", problemRoot); code != 0 {
		t.Fatalf("导入题目失败: %s %s", out, errOut)
	}

	reportPath := filepath.Join(dir, "report.csv")
	if code, _, errOut := run("export", "-config", cfgPath, "-exam", "2027-spring", "-out", reportPath); code != 0 {
		t.Fatalf("导出报表失败: %s", errOut)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "student_no") || !strings.Contains(string(report), "20231234") {
		t.Fatalf("报表内容错误:\n%s", report)
	}

	backupPath := filepath.Join(dir, "backup.tar.gz")
	if code, _, errOut := run("backup", "-config", cfgPath, "-out", backupPath); code != 0 {
		t.Fatalf("备份失败: %s", errOut)
	}
	if info, err := os.Stat(backupPath); err != nil || info.Size() == 0 {
		t.Fatalf("备份文件无效: %v", err)
	}

	restoreDir := t.TempDir()
	restoreCfg := filepath.Join(restoreDir, "config.yaml")
	restoreYAML := `
listen: "127.0.0.1:0"
public_url: "http://127.0.0.1:8080"
db: "data/examd.db"
data_dir: "data"
judge:
  runtime: local
  allow_local: true
`
	if err := os.WriteFile(restoreCfg, []byte(restoreYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run("restore", "-config", restoreCfg, "-in", backupPath); code != 0 {
		t.Fatalf("恢复失败: %s", errOut)
	}
	if code, _, errOut := run("export", "-config", restoreCfg, "-exam", "2027-spring", "-out", "-"); code != 0 {
		t.Fatalf("恢复后导出失败: %s", errOut)
	}
}
