package judge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exam.local/tools/internal/store"
)

func writeCase(t *testing.T, dataDir, rel, content string) {
	t.Helper()
	p := filepath.Join(dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSolution(t *testing.T, dir, pkg, code string) {
	t.Helper()
	base := filepath.Join(dir, "problems", pkg)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module exam\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newLocalRunner(t *testing.T, dataDir string) *Runner {
	t.Helper()
	return New(Options{
		Runtime:            "local",
		GoBin:              "go",
		WorkDir:            t.TempDir(),
		DataDir:            dataDir,
		CompileTimeout:     30 * time.Second,
		DefaultRunTimeout:  3 * time.Second,
		MemoryMB:           256,
		CompileConcurrency: 2,
		RunConcurrency:     2,
	}, nil)
}

func TestJudgeProblemLocal(t *testing.T) {
	cases := []struct {
		name     string
		code     string
		status   string
		score    int
		extraRun time.Duration
	}{
		{
			name: "accepted",
			code: `package main
import "fmt"
func main() { var a, b int; fmt.Scan(&a, &b); fmt.Println(a + b) }
`,
			status: "accepted",
			score:  100,
		},
		{
			name: "wrong_answer",
			code: `package main
import "fmt"
func main() { var a, b int; fmt.Scan(&a, &b); fmt.Println(a * b) }
`,
			status: "failed",
			score:  0,
		},
		{
			name: "compile_error",
			code: `package main
func main() { this is not go }
`,
			status: "compile_error",
			score:  0,
		},
		{
			name: "timeout",
			code: `package main
import "time"
func main() { time.Sleep(5 * time.Second) }
`,
			status:   "failed",
			score:    0,
			extraRun: 200 * time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srcDir := t.TempDir()
			writeSolution(t, srcDir, "sum", tc.code)
			dataDir := t.TempDir()
			writeCase(t, dataDir, "sum/1.in", "1 2\n")
			writeCase(t, dataDir, "sum/1.out", "3\n")
			writeCase(t, dataDir, "sum/2.in", "10 20\n")
			writeCase(t, dataDir, "sum/2.out", "30\n")

			timeLimit := 2000
			if tc.extraRun > 0 {
				timeLimit = int(tc.extraRun.Milliseconds())
			}
			problem := store.Problem{
				ID: 1, Key: "sum", Score: 100, TimeLimitMS: timeLimit,
				Cases: []store.TestCase{
					{CaseNo: 1, InputPath: "sum/1.in", ExpectedPath: "sum/1.out", Score: 60},
					{CaseNo: 2, InputPath: "sum/2.in", ExpectedPath: "sum/2.out", Score: 40},
				},
			}

			res := newLocalRunner(t, dataDir).judgeProblem(context.Background(), srcDir, problem)
			if res.Status != tc.status {
				t.Fatalf("status = %s, want %s (compile=%s cases=%+v)", res.Status, tc.status, res.CompileOutput, res.Cases)
			}
			if res.Score != tc.score {
				t.Fatalf("score = %d, want %d", res.Score, tc.score)
			}
			if tc.status == "accepted" {
				if res.Passed != 2 || res.Total != 2 {
					t.Fatalf("passed = %d/%d", res.Passed, res.Total)
				}
				for _, c := range res.Cases {
					if c.Status != "pass" {
						t.Fatalf("用例状态 %+v", c)
					}
				}
			}
		})
	}
}

func TestJudgeProblemMissingSolution(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module exam\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problem := store.Problem{ID: 1, Key: "sum", TimeLimitMS: 1000, Cases: []store.TestCase{{CaseNo: 1, InputPath: "x", ExpectedPath: "y"}}}
	res := newLocalRunner(t, t.TempDir()).judgeProblem(context.Background(), srcDir, problem)
	if res.Status != "missing_solution" {
		t.Fatalf("status = %s, want missing_solution", res.Status)
	}
}

func ensureGit(t *testing.T) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 不可用，跳过 clone 集成测试")
	}
	dirs := []string{filepath.Dir(gitBin)}
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		dirs = append(dirs, execPath, filepath.Join(filepath.Dir(filepath.Dir(execPath)), "bin"))
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join(dirs, sep)+sep+os.Getenv("PATH"))
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunExamEndToEnd(t *testing.T) {
	ensureGit(t)

	work := t.TempDir()
	writeSolution(t, work, "sum", `package main
import "fmt"
func main() { var a, b int; fmt.Scan(&a, &b); fmt.Println(a + b) }
`)
	gitRun(t, work, "init", "-b", "main")
	gitRun(t, work, "-c", "user.name=test", "-c", "user.email=t@example.com", "add", ".")
	gitRun(t, work, "-c", "user.name=test", "-c", "user.email=t@example.com", "commit", "-m", "init")
	sha := gitRun(t, work, "rev-parse", "HEAD")

	bare := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, t.TempDir(), "clone", "--bare", work, bare)

	st, err := store.Open(filepath.Join(t.TempDir(), "judge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateExam(&store.Exam{
		ID:       "e2e",
		Title:    "e2e",
		StartsAt: time.Now().Add(-time.Hour),
		EndsAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cid, err := st.UpsertCandidate(&store.Candidate{ExamID: "e2e", StudentNo: "20231234", Name: "张三", Track: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRepo(cid, bare); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRepoFinalCommit(cid, sha); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	writeCase(t, dataDir, "sum/1.in", "1 2\n")
	writeCase(t, dataDir, "sum/1.out", "3\n")
	if _, err := st.UpsertProblem(&store.Problem{
		ExamID: "e2e", Key: "sum", Title: "求和", Score: 100, TimeLimitMS: 2000,
		Cases: []store.TestCase{{CaseNo: 1, InputPath: "sum/1.in", ExpectedPath: "sum/1.out", Score: 100}},
	}); err != nil {
		t.Fatal(err)
	}

	runner := New(Options{
		Runtime: "local", GoBin: "go", WorkDir: t.TempDir(), DataDir: dataDir,
		CompileTimeout: 60 * time.Second, DefaultRunTimeout: 3 * time.Second,
		MemoryMB: 256, CompileConcurrency: 1, RunConcurrency: 2,
	}, st)

	results, err := runner.RunExam(context.Background(), "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	res := results[0]
	if res.Status != "ok" || len(res.Problems) != 1 {
		t.Fatalf("结果错误: %+v", res)
	}
	if res.Problems[0].Status != "accepted" || res.Score != 100 {
		t.Fatalf("判题错误: %+v", res.Problems[0])
	}
	if res.FinalSHA != sha {
		t.Fatalf("final sha = %s, want %s", res.FinalSHA, sha)
	}

	rows, err := st.ListJudgeResults("e2e")
	if err != nil || len(rows) != 1 {
		t.Fatalf("落库失败: %v %d", err, len(rows))
	}
	if rows[0].Status != "accepted" || rows[0].Score != 100 {
		t.Fatalf("落库内容错误: %+v", rows[0])
	}
}
