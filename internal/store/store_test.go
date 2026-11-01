package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedExam(t *testing.T, st *Store) *Exam {
	t.Helper()
	exam := &Exam{
		ID:       "2027-spring",
		Title:    "2027 春季招新机试",
		StartsAt: time.Now().Add(-time.Hour),
		EndsAt:   time.Now().Add(time.Hour),
	}
	if err := st.CreateExam(exam); err != nil {
		t.Fatalf("CreateExam: %v", err)
	}
	return exam
}

func TestExamCandidateLifecycle(t *testing.T) {
	st := newTestStore(t)
	seedExam(t, st)

	if _, err := st.GetExam("missing"); err != ErrNotFound {
		t.Fatalf("不存在的场次应返回 ErrNotFound, got %v", err)
	}

	id, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231234", Name: "张三", Track: "A", Variant: "A"})
	if err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	// 重复导入应更新而不是新增
	if _, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231234", Name: "张三丰", Track: "B", Variant: "B"}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListCandidates("2027-spring")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("candidates = %d, want 1", len(list))
	}
	if list[0].Name != "张三丰" || list[0].Track != "B" || list[0].Variant != "B" {
		t.Fatalf("更新未生效: %+v", list[0])
	}

	if err := st.SetCandidateToken(id, "hash-value"); err != nil {
		t.Fatal(err)
	}
	c, exam, err := st.FindCandidateByStudentNo("20231234")
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenHash != "hash-value" || exam.ID != "2027-spring" {
		t.Fatalf("查找结果错误: %+v %+v", c, exam)
	}

	token := "session-token-0123456789"
	expires := time.Now().Add(time.Hour)
	if err := st.CreateCandidateSession(c.ID, token, expires, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	got, gotExam, err := st.CandidateBySession(token)
	if err != nil {
		t.Fatalf("会话查找: %v", err)
	}
	if got.ID != c.ID || gotExam.ID != exam.ID {
		t.Fatalf("会话指向错误: %+v", got)
	}
	if _, _, err := st.CandidateBySession("wrong"); err != ErrNotFound {
		t.Fatalf("错误会话应返回 ErrNotFound, got %v", err)
	}

	if err := st.DeleteSession("candidate_session", token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CandidateBySession(token); err != ErrNotFound {
		t.Fatalf("删除后会话仍有效")
	}
}

func TestPushEventsAndFinalize(t *testing.T) {
	st := newTestStore(t)
	seedExam(t, st)
	id, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231235", Name: "李四"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCandidateRepo(id, "20231235", "exam-2027-20231235"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRepo(id, "http://gitea/20231235/exam.git"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := st.AddPushEvent(&PushEvent{CandidateID: id, Ref: "refs/heads/main", CommitSHA: "aaa", ReceivedAt: now.Add(-2 * time.Minute), Source: "webhook"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddPushEvent(&PushEvent{CandidateID: id, Ref: "refs/heads/main", CommitSHA: "bbb", ReceivedAt: now.Add(-time.Minute), Source: "hook"}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountPushEvents(id); err != nil || n != 2 {
		t.Fatalf("push count = %d, %v", n, err)
	}
	last, err := st.LastPushEvent(id)
	if err != nil || last.CommitSHA != "bbb" {
		t.Fatalf("last push = %+v, %v", last, err)
	}
	repo, err := st.GetRepoByCandidate(id)
	if err != nil {
		t.Fatal(err)
	}
	if repo.LastPushAt.IsZero() {
		t.Fatal("last_push_at 未更新")
	}

	if _, err := st.FinalizeExam("2027-spring"); err != nil {
		t.Fatal(err)
	}
	repo, err = st.GetRepoByCandidate(id)
	if err != nil {
		t.Fatal(err)
	}
	if repo.FinalCommit != "bbb" {
		t.Fatalf("final_commit = %s, want bbb", repo.FinalCommit)
	}
	// 已经有 final_commit 时不应被推送事件覆盖（冻结时以分支头为准）
	if err := st.SetRepoFinalCommit(id, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinalizeExam("2027-spring"); err != nil {
		t.Fatal(err)
	}
	repo, err = st.GetRepoByCandidate(id)
	if err != nil {
		t.Fatal(err)
	}
	if repo.FinalCommit != "manual" {
		t.Fatalf("final_commit 被覆盖为 %s", repo.FinalCommit)
	}

	rows, err := st.LiveRows("2027-spring")
	if err != nil || len(rows) != 1 {
		t.Fatalf("live rows = %d, %v", len(rows), err)
	}
	if rows[0].PushCount != 2 || rows[0].FinalCommit != "manual" {
		t.Fatalf("live row 错误: %+v", rows[0])
	}
}

func TestPushEventUpsert(t *testing.T) {
	st := newTestStore(t)
	seedExam(t, st)
	id, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231238", Name: "钱七"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRepo(id, "http://gitea/20231238/exam.git"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// webhook 先到：只有文件数
	if err := st.AddPushEvent(&PushEvent{CandidateID: id, Ref: "refs/heads/main", CommitSHA: "sha1", ReceivedAt: now, FilesChanged: 2, Source: "webhook"}); err != nil {
		t.Fatal(err)
	}
	// post-receive 后到：补全行数，不新增记录
	if err := st.AddPushEvent(&PushEvent{CandidateID: id, Ref: "refs/heads/main", CommitSHA: "sha1", ReceivedAt: now.Add(time.Second), LinesAdded: 42, LinesDeleted: 7, FilesChanged: 2, Source: "post-receive"}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountPushEvents(id); err != nil || n != 1 {
		t.Fatalf("同 commit 应去重，count=%d err=%v", n, err)
	}
	ev, err := st.LastPushEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	if ev.LinesAdded != 42 || ev.LinesDeleted != 7 || ev.FilesChanged != 2 {
		t.Fatalf("行数未补全: %+v", ev)
	}

	// 新 commit 正常插入
	if err := st.AddPushEvent(&PushEvent{CandidateID: id, Ref: "refs/heads/main", CommitSHA: "sha2", ReceivedAt: now.Add(2 * time.Second), LinesAdded: 3, FilesChanged: 1, Source: "post-receive"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountPushEvents(id); n != 2 {
		t.Fatalf("新 commit 未插入，count=%d", n)
	}
}

func TestJudgeLock(t *testing.T) {
	st := newTestStore(t)
	ok, err := st.AcquireJudgeLock("judge:e1", "run-a", time.Hour)
	if err != nil || !ok {
		t.Fatalf("首次加锁失败: %v %v", ok, err)
	}
	ok, err = st.AcquireJudgeLock("judge:e1", "run-b", time.Hour)
	if err != nil || ok {
		t.Fatalf("并发加锁应失败: %v %v", ok, err)
	}
	if err := st.ReleaseJudgeLock("judge:e1", "run-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = st.AcquireJudgeLock("judge:e1", "run-b", time.Hour)
	if err != nil || !ok {
		t.Fatalf("释放后加锁失败: %v %v", ok, err)
	}

	// 过期锁可以被抢占
	if _, err := st.AcquireJudgeLock("judge:e2", "old", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	ok, err = st.AcquireJudgeLock("judge:e2", "new", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("过期锁应可抢占: %v %v", ok, err)
	}
	if err := st.ReleaseJudgeLock("judge:e2", "old"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentEvents(t *testing.T) {
	st := newTestStore(t)
	seedExam(t, st)
	id, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231237", Name: "赵六"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddAgentEvent(id, time.Now().Add(-time.Minute), "start", `{"repo":"x"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.AddAgentEvent(id, time.Now(), "network_change", `{"to":"wifi=exam"}`); err != nil {
		t.Fatal(err)
	}
	ev, err := st.LastAgentEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "network_change" {
		t.Fatalf("最后事件 = %s", ev.Type)
	}
	if _, err := st.LastAgentEvent(99999); err != ErrNotFound {
		t.Fatalf("不存在的事件应返回 ErrNotFound, got %v", err)
	}
}

func TestJudgeResultsReplace(t *testing.T) {
	st := newTestStore(t)
	seedExam(t, st)
	id, err := st.UpsertCandidate(&Candidate{ExamID: "2027-spring", StudentNo: "20231236", Name: "王五"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceJudgeResults(id, []JudgeResult{
		{CandidateID: id, ProblemID: 1, Status: "accepted", Passed: 2, Total: 2, Score: 100, DetailJSON: "{}"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceJudgeResults(id, []JudgeResult{
		{CandidateID: id, ProblemID: 1, Status: "failed", Passed: 1, Total: 2, Score: 50, DetailJSON: "{}"},
		{CandidateID: id, ProblemID: 2, Status: "accepted", Passed: 1, Total: 1, Score: 100, DetailJSON: "{}"},
	}); err != nil {
		t.Fatal(err)
	}
	results, err := st.ListJudgeResults("2027-spring")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Score != 50 || results[1].Score != 100 {
		t.Fatalf("结果覆盖错误: %+v", results)
	}
}
