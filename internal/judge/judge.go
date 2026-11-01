// Package judge 实现考后批量判题：clone、checkout、编译、逐用例运行。
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"exam.local/tools/internal/store"
)

// Options 是判题运行参数。
type Options struct {
	Runtime            string // docker | local
	Image              string
	GoBin              string
	WorkDir            string
	DataDir            string
	CompileTimeout     time.Duration
	DefaultRunTimeout  time.Duration
	MemoryMB           int
	CompileConcurrency int
	RunConcurrency     int
	CloneAuth          transport.AuthMethod
	CABundle           []byte
	InsecureSkipTLS    bool
	Logf               func(format string, args ...any)
}

// CaseResult 是单个用例结果。
type CaseResult struct {
	CaseNo int    `json:"case_no"`
	Status string `json:"status"`
	TimeMS int64  `json:"time_ms"`
	Detail string `json:"detail,omitempty"`
}

// ProblemResult 是一道题的结果。
type ProblemResult struct {
	ProblemID     int64        `json:"problem_id"`
	ProblemKey    string       `json:"problem_key"`
	Status        string       `json:"status"`
	Passed        int          `json:"passed"`
	Total         int          `json:"total"`
	Score         int          `json:"score"`
	CompileOutput string       `json:"compile_output,omitempty"`
	Cases         []CaseResult `json:"cases,omitempty"`
}

// CandidateResult 是一个考生的判题结果。
type CandidateResult struct {
	StudentNo string          `json:"student_no"`
	Name      string          `json:"name"`
	Track     string          `json:"track"`
	Status    string          `json:"status"`
	Error     string          `json:"error,omitempty"`
	FinalSHA  string          `json:"final_sha,omitempty"`
	Score     int             `json:"score"`
	Problems  []ProblemResult `json:"problems"`
}

// Runner 执行判题。
type Runner struct {
	opts Options
	st   *store.Store
	run  chan struct{}
}

// New 创建 Runner。
func New(opts Options, st *store.Store) *Runner {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.CompileConcurrency < 1 {
		opts.CompileConcurrency = 2
	}
	if opts.RunConcurrency < 1 {
		opts.RunConcurrency = 8
	}
	if opts.CompileTimeout <= 0 {
		opts.CompileTimeout = 10 * time.Second
	}
	if opts.DefaultRunTimeout <= 0 {
		opts.DefaultRunTimeout = 3 * time.Second
	}
	return &Runner{opts: opts, st: st, run: make(chan struct{}, opts.RunConcurrency)}
}

// RunExam 判整个场次，onlyStudent 非空时只判一人。
func (r *Runner) RunExam(ctx context.Context, examID, onlyStudent string) ([]CandidateResult, error) {
	if _, err := r.st.GetExam(examID); err != nil {
		return nil, err
	}
	problems, err := r.st.ListProblems(examID)
	if err != nil {
		return nil, err
	}
	candidates, err := r.st.ListCandidates(examID)
	if err != nil {
		return nil, err
	}

	type slot struct {
		result *CandidateResult
	}
	slots := make([]slot, len(candidates))
	sem := make(chan struct{}, r.opts.CompileConcurrency)
	var wg sync.WaitGroup
	for i := range candidates {
		if onlyStudent != "" && candidates[i].StudentNo != onlyStudent {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res := r.judgeCandidate(ctx, &candidates[i], problems)
			slots[i].result = &res

			if len(res.Problems) == 0 {
				return
			}
			rows := make([]store.JudgeResult, 0, len(res.Problems))
			for _, p := range res.Problems {
				detail, _ := json.Marshal(p)
				rows = append(rows, store.JudgeResult{
					CandidateID: candidates[i].ID,
					ProblemID:   p.ProblemID,
					JudgedAt:    time.Now(),
					Status:      p.Status,
					Passed:      p.Passed,
					Total:       p.Total,
					Score:       p.Score,
					DetailJSON:  string(detail),
				})
			}
			if err := r.st.ReplaceJudgeResults(candidates[i].ID, rows); err != nil {
				r.opts.Logf("judge %s: 保存结果失败: %v", candidates[i].StudentNo, err)
			}
		}(i)
	}
	wg.Wait()

	var out []CandidateResult
	for _, s := range slots {
		if s.result != nil {
			out = append(out, *s.result)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StudentNo < out[j].StudentNo })
	return out, nil
}

func (r *Runner) judgeCandidate(ctx context.Context, c *store.Candidate, problems []store.Problem) CandidateResult {
	res := CandidateResult{StudentNo: c.StudentNo, Name: c.Name, Track: c.Track, Status: "ok"}

	repoRec, err := r.st.GetRepoByCandidate(c.ID)
	if err != nil || repoRec.URL == "" {
		res.Status = "no_repo"
		res.Error = "没有仓库记录"
		return res
	}
	if err := os.MkdirAll(r.opts.WorkDir, 0o755); err != nil {
		res.Status = "error"
		res.Error = err.Error()
		return res
	}

	dir := filepath.Join(r.opts.WorkDir, "repos", sanitize(c.StudentNo))
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	repo, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:             repoRec.URL,
		Auth:            r.opts.CloneAuth,
		Depth:           0,
		Tags:            git.NoTags,
		CABundle:        r.opts.CABundle,
		InsecureSkipTLS: r.opts.InsecureSkipTLS,
	})
	if err != nil {
		res.Status = "clone_error"
		res.Error = err.Error()
		return res
	}

	head, err := repo.Head()
	if err != nil {
		res.Status = "clone_error"
		res.Error = fmt.Sprintf("读取 HEAD: %v", err)
		return res
	}
	if repoRec.FinalCommit != "" && head.Hash().String() != repoRec.FinalCommit {
		wt, err := repo.Worktree()
		if err != nil {
			res.Status = "commit_mismatch"
			res.Error = err.Error()
			return res
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(repoRec.FinalCommit)}); err != nil {
			res.Status = "commit_mismatch"
			res.Error = fmt.Sprintf("checkout %s: %v", repoRec.FinalCommit, err)
			return res
		}
		head, err = repo.Head()
		if err != nil || head.Hash().String() != repoRec.FinalCommit {
			res.Status = "commit_mismatch"
			res.Error = "HEAD 与 final_commit 不一致"
			return res
		}
	}
	res.FinalSHA = head.Hash().String()

	if len(problems) == 0 {
		res.Status = "no_problems"
		return res
	}
	for _, p := range problems {
		pr := r.judgeProblem(ctx, dir, p)
		res.Problems = append(res.Problems, pr)
		res.Score += pr.Score
	}
	return res
}

func (r *Runner) judgeProblem(ctx context.Context, srcDir string, p store.Problem) ProblemResult {
	res := ProblemResult{ProblemID: p.ID, ProblemKey: p.Key}
	var cases []store.TestCase
	for _, c := range p.Cases {
		if !c.IsSample {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		res.Status = "no_cases"
		return res
	}
	pkg, ok := packageDir(srcDir, p.Key)
	if !ok {
		res.Status = "missing_solution"
		return res
	}

	outDir := filepath.Join(srcDir, ".judge")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		res.Status = "error"
		res.CompileOutput = err.Error()
		return res
	}
	binary := sanitize(p.Key) + ".bin"
	if output, err := r.compile(ctx, srcDir, outDir, pkg, binary); err != nil {
		res.Status = "compile_error"
		res.CompileOutput = truncate(output, 2000)
		return res
	}

	timeout := time.Duration(p.TimeLimitMS) * time.Millisecond
	if timeout <= 0 {
		timeout = r.opts.DefaultRunTimeout
	}

	res.Total = len(cases)
	for _, c := range cases {
		score := c.Score
		if score <= 0 {
			score = 1
		}
		cr := r.runCase(ctx, outDir, binary, c, timeout)
		res.Cases = append(res.Cases, cr)
		if cr.Status == "pass" {
			res.Passed++
			res.Score += score
		}
	}

	switch {
	case res.Passed == res.Total:
		res.Status = "accepted"
	case res.Passed == 0:
		res.Status = "failed"
	default:
		res.Status = "partial"
	}
	return res
}

func (r *Runner) compile(ctx context.Context, srcDir, outDir, pkg, binary string) (string, error) {
	if r.opts.Runtime == "docker" {
		cacheDir := filepath.Join(r.opts.WorkDir, "gocache")
		goPathDir := filepath.Join(r.opts.WorkDir, "gopath")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", err
		}
		if err := os.MkdirAll(goPathDir, 0o755); err != nil {
			return "", err
		}
		name := "exam-build-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		args := []string{
			"run", "--rm", "--name", name,
			"--network", "none", "--memory", "1g", "--cpus", "1",
			"-v", filepath.ToSlash(srcDir) + ":/src:ro",
			"-v", filepath.ToSlash(outDir) + ":/out",
			"-v", filepath.ToSlash(cacheDir) + ":/tmp/gocache",
			"-v", filepath.ToSlash(goPathDir) + ":/tmp/gopath",
			"-e", "GOCACHE=/tmp/gocache",
			"-e", "GOPATH=/tmp/gopath",
			"-e", "GOPROXY=off",
			"-e", "CGO_ENABLED=0",
			"-w", "/src",
			r.opts.Image,
			"go", "build", "-o", "/out/" + binary, pkg,
		}
		out := runCommand(ctx, "docker", args, nil, "", nil, r.opts.CompileTimeout)
		if out.err != nil {
			cleanupDocker(name)
			return string(out.stdout) + string(out.stderr), out.err
		}
		return string(out.stdout) + string(out.stderr), nil
	}

	bin := filepath.Join(outDir, binary)
	env := append(os.Environ(),
		"CGO_ENABLED=0",
		"GOPROXY=off",
		"GOCACHE="+filepath.Join(r.opts.WorkDir, "gocache"),
	)
	out := runCommand(ctx, r.opts.GoBin, []string{"build", "-o", bin, pkg}, nil, srcDir, env, r.opts.CompileTimeout)
	if out.err != nil {
		return string(out.stdout) + string(out.stderr), out.err
	}
	return string(out.stdout) + string(out.stderr), nil
}

func (r *Runner) runCase(ctx context.Context, outDir, binary string, c store.TestCase, timeout time.Duration) CaseResult {
	cr := CaseResult{CaseNo: c.CaseNo}
	input, err := os.ReadFile(r.dataPath(c.InputPath))
	if err != nil {
		cr.Status = "error"
		cr.Detail = "读取输入失败: " + err.Error()
		return cr
	}
	expected, err := os.ReadFile(r.dataPath(c.ExpectedPath))
	if err != nil {
		cr.Status = "error"
		cr.Detail = "读取期望输出失败: " + err.Error()
		return cr
	}

	select {
	case r.run <- struct{}{}:
	case <-ctx.Done():
		cr.Status = "error"
		cr.Detail = "判题已取消"
		return cr
	}
	defer func() { <-r.run }()

	start := time.Now()
	var out runOutput
	if r.opts.Runtime == "docker" {
		name := "exam-run-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		args := []string{
			"run", "--rm", "--name", name,
			"--network", "none",
			"--memory", fmt.Sprintf("%dm", r.opts.MemoryMB),
			"--cpus", "1",
			"-i",
			"-v", filepath.ToSlash(outDir) + ":/out:ro",
			r.opts.Image,
		}
		args = append(args, "/out/"+binary)
		out = runCommand(ctx, "docker", args, input, "", nil, timeout)
		if out.timedOut {
			cleanupDocker(name)
		}
	} else {
		bin := filepath.Join(outDir, binary)
		out = runCommand(ctx, bin, nil, input, "", nil, timeout)
	}
	cr.TimeMS = time.Since(start).Milliseconds()

	switch {
	case out.timedOut:
		cr.Status = "timeout"
	case out.err != nil:
		cr.Status = "runtime_error"
		cr.Detail = truncate(string(out.stderr), 200)
	case normalize(out.stdout) == normalize(expected):
		cr.Status = "pass"
	default:
		cr.Status = "wrong_answer"
		cr.Detail = diffHint(out.stdout, expected)
	}
	return cr
}

func (r *Runner) dataPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.opts.DataDir, p)
}

// packageDir 返回题目包路径：优先 problems/<key>，否则仓库根目录。
func packageDir(srcDir, key string) (string, bool) {
	sub := filepath.Join(srcDir, "problems", key)
	if hasGoFile(sub) {
		return "./problems/" + key, true
	}
	if hasGoFile(srcDir) {
		return ".", true
	}
	return "", false
}

func hasGoFile(dir string) bool {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return false
	}
	for _, m := range matches {
		if !strings.HasSuffix(m, "_test.go") {
			return true
		}
	}
	return false
}

type runOutput struct {
	stdout   []byte
	stderr   []byte
	err      error
	timedOut bool
}

func runCommand(parent context.Context, name string, args []string, stdin []byte, dir string, env []string, timeout time.Duration) runOutput {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := runOutput{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
	if ctx.Err() == context.DeadlineExceeded {
		out.timedOut = true
		out.err = context.DeadlineExceeded
	}
	return out
}

func cleanupDocker(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func normalize(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func diffHint(actual, expected []byte) string {
	return fmt.Sprintf("输出不一致（实际 %d 字节，期望 %d 字节）", len(actual), len(expected))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

func sanitize(s string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if clean == "" {
		return "x"
	}
	return clean
}

// Accepted 判断考生是否所有题目都通过。
func Accepted(r CandidateResult) bool {
	if r.Status != "ok" || len(r.Problems) == 0 {
		return false
	}
	for _, p := range r.Problems {
		if p.Status != "accepted" {
			return false
		}
	}
	return true
}

// WriteCSV 输出判题结果 CSV（每个考生一行，每道题一列）。
func WriteCSV(w io.Writer, results []CandidateResult, problems []store.Problem) error {
	keys := make([]string, 0, len(problems))
	for _, p := range problems {
		keys = append(keys, p.Key)
	}
	header := append([]string{"student_no", "name", "track", "status", "final_sha"}, keys...)
	header = append(header, "score", "detail", "error")
	if _, err := fmt.Fprintln(w, joinCSV(header)); err != nil {
		return err
	}
	for _, r := range results {
		scores := map[string]string{}
		for _, p := range r.Problems {
			scores[p.ProblemKey] = fmt.Sprintf("%d/%d", p.Passed, p.Total)
		}
		row := []string{r.StudentNo, r.Name, r.Track, r.Status, r.FinalSHA}
		for _, k := range keys {
			row = append(row, scores[k])
		}
		row = append(row, strconv.Itoa(r.Score), statusSummary(r), r.Error)
		if _, err := fmt.Fprintln(w, joinCSV(row)); err != nil {
			return err
		}
	}
	return nil
}

func joinCSV(fields []string) string {
	for i, f := range fields {
		if strings.ContainsAny(f, ",\"\n") {
			fields[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		}
	}
	return strings.Join(fields, ",")
}

func statusSummary(r CandidateResult) string {
	var parts []string
	for _, p := range r.Problems {
		parts = append(parts, fmt.Sprintf("%s=%s", p.ProblemKey, p.Status))
	}
	return strings.Join(parts, ";")
}
