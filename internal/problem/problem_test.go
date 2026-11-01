package problem

import (
	"os"
	"path/filepath"
	"testing"

	"exam.local/tools/internal/store"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sum/problem.md", "# 求和\n\n读入两个整数，输出和。\n")
	write(t, root, "sum/meta.json", `{"title":"求和","score":101,"time_limit_ms":1500,"memory_limit_mb":128}`)
	write(t, root, "sum/variants/A.md", "# 求和（A 卷）\n")
	write(t, root, "sum/variants/B.md", "# 求和（B 卷）\n")
	write(t, root, "sum/cases/01.in", "1 2\n")
	write(t, root, "sum/cases/01.out", "3\n")
	write(t, root, "sum/cases/02.in", "2 2\n")
	write(t, root, "sum/cases/02.out", "4\n")
	write(t, root, "sum/samples/01.in", "3 3\n")
	write(t, root, "sum/samples/01.out", "6\n")

	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateExam(&store.Exam{ID: "exam", Title: "exam"}); err != nil {
		t.Fatal(err)
	}

	count, err := Import(st, dataDir, "exam", root)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}

	problems, err := st.ListProblems("exam")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %d", len(problems))
	}
	p := problems[0]
	if p.Title != "求和" || p.Score != 101 || p.TimeLimitMS != 1500 || p.MemoryLimitMB != 128 {
		t.Fatalf("meta 未生效: %+v", p)
	}
	if len(p.Variants) != 2 || p.Variants[0].Seed != "A" {
		t.Fatalf("变体错误: %+v", p.Variants)
	}
	if len(p.Cases) != 3 {
		t.Fatalf("cases = %d, want 3", len(p.Cases))
	}

	var hiddenScore, sampleCount int
	for _, c := range p.Cases {
		if c.IsSample {
			sampleCount++
			continue
		}
		hiddenScore += c.Score
		if _, err := os.Stat(filepath.Join(dataDir, c.InputPath)); err != nil {
			t.Fatalf("用例未复制: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, c.ExpectedPath)); err != nil {
			t.Fatalf("期望输出未复制: %v", err)
		}
	}
	if hiddenScore != 101 {
		t.Fatalf("隐藏用例总分 = %d, want 101", hiddenScore)
	}
	if sampleCount != 1 {
		t.Fatalf("样例数 = %d", sampleCount)
	}
}

func TestImportRejectsMissingExpectedOutput(t *testing.T) {
	root := t.TempDir()
	write(t, root, "bad/cases/01.in", "1\n")
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateExam(&store.Exam{ID: "exam", Title: "exam"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(st, t.TempDir(), "exam", root); err == nil {
		t.Fatal("缺少 .out 应报错")
	}
}
