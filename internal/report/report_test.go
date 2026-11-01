package report

import (
	"path/filepath"
	"strings"
	"testing"

	"exam.local/tools/internal/store"
)

func TestBuildJudgeStatus(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateExam(&store.Exam{ID: "e", Title: "e"}); err != nil {
		t.Fatal(err)
	}
	pid, err := st.UpsertProblem(&store.Problem{ExamID: "e", Key: "sum", Title: "sum", Score: 100})
	if err != nil {
		t.Fatal(err)
	}
	okID, err := st.UpsertCandidate(&store.Candidate{ExamID: "e", StudentNo: "20230001", Name: "正常"})
	if err != nil {
		t.Fatal(err)
	}
	badID, err := st.UpsertCandidate(&store.Candidate{ExamID: "e", StudentNo: "20230002", Name: "失败"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceJudgeResults(okID, []store.JudgeResult{
		{CandidateID: okID, ProblemID: pid, Status: "accepted", Passed: 1, Total: 1, Score: 100, DetailJSON: "{}"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceJudgeResults(badID, []store.JudgeResult{
		{CandidateID: badID, ProblemID: 0, Status: "clone_error", DetailJSON: "{}"},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := Build(st, "e")
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "judge_status") {
		t.Fatalf("缺少 judge_status 列:\n%s", out)
	}
	if !strings.Contains(out, "20230001,正常") || !strings.Contains(out, "ok") {
		t.Fatalf("成功判题应为 ok:\n%s", out)
	}
	if !strings.Contains(out, "clone_error") {
		t.Fatalf("失败判题状态缺失:\n%s", out)
	}
}
