// Package cli 实现 examd 的命令行入口。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/crypto/bcrypt"

	"exam.local/tools/internal/backup"
	"exam.local/tools/internal/config"
	"exam.local/tools/internal/gitea"
	"exam.local/tools/internal/httpapi"
	"exam.local/tools/internal/judge"
	"exam.local/tools/internal/problem"
	"exam.local/tools/internal/provision"
	"exam.local/tools/internal/report"
	"exam.local/tools/internal/review"
	"exam.local/tools/internal/roster"
	"exam.local/tools/internal/store"
)

// Version 由构建参数注入。
var Version = "dev"

// Run 执行命令，返回进程退出码。
func Run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(out)
		return 2
	}
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "serve":
		err = cmdServe(rest, out, errOut)
	case "admin":
		err = cmdAdmin(rest, out, errOut)
	case "exam":
		err = cmdExam(rest, out, errOut)
	case "roster":
		err = cmdRoster(rest, out, errOut)
	case "problem":
		err = cmdProblem(rest, out, errOut)
	case "provision":
		err = cmdProvision(rest, out, errOut)
	case "token":
		err = cmdToken(rest, out, errOut)
	case "judge":
		err = cmdJudge(rest, out, errOut)
	case "review":
		err = cmdReview(rest, out, errOut)
	case "export":
		err = cmdExport(rest, out, errOut)
	case "backup":
		err = cmdBackup(rest, out, errOut)
	case "restore":
		err = cmdRestore(rest, out, errOut)
	case "version", "--version", "-v":
		fmt.Fprintf(out, "examd %s\n", Version)
		return 0
	case "help", "-h", "--help":
		usage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "未知命令: %s\n\n", cmd)
		usage(errOut)
		return 2
	}
	if err != nil {
		fmt.Fprintf(errOut, "错误: %v\n", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `examd - 考场 Git + 判题服务

用法:
  examd serve      -config config.yaml
  examd admin      -config config.yaml -username admin -password 密码
  examd exam       -config config.yaml -id 2027-spring -title "2027 春季招新机试" -starts-at "2027-02-15T14:00:00+08:00" -ends-at "2027-02-15T16:00:00+08:00"
  examd roster     -config config.yaml -exam 2027-spring -csv roster.csv
  examd problem    -config config.yaml -exam 2027-spring -dir problems/
  examd provision  -config config.yaml -exam 2027-spring -slips slips.csv
  examd token      -config config.yaml -exam 2027-spring -student 20231234
  examd judge      -config config.yaml -exam 2027-spring -out results.csv [-student 20231234]
  examd review     -config config.yaml -exam 2027-spring -out review.csv [-student 20231234] [-include-heartbeat]
  examd export     -config config.yaml -exam 2027-spring -out report.csv
  examd backup     -config config.yaml -out backup.tar.gz
  examd restore    -config config.yaml -in backup.tar.gz [-force]
  examd version
`)
}

func newFlags(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	return fs
}

func loggerTo(w io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(w, "[examd] "+format+"\n", args...)
	}
}

func openStore(cfg *config.Config) (*store.Store, error) {
	return store.Open(cfg.DBPath())
}

func newGitea(cfg *config.Config) (*gitea.Client, error) {
	if cfg.Gitea.URL == "" {
		return nil, nil
	}
	return gitea.New(cfg.Gitea.URL, cfg.Gitea.AdminToken, cfg.Gitea.AdminUser,
		cfg.Gitea.AdminPassword, cfg.Gitea.CAFile, cfg.Gitea.InsecureSkipVerify)
}

func judgeOptions(cfg *config.Config, gc *gitea.Client, logf func(string, ...any)) judge.Options {
	var auth transport.AuthMethod
	var caBundle []byte
	var insecure bool
	if gc != nil {
		user, pass := gc.BasicAuth()
		auth = &githttp.BasicAuth{Username: user, Password: pass}
		caBundle = gc.CABundle()
		insecure = gc.InsecureSkipTLS()
	}
	return judge.Options{
		Runtime:            cfg.Judge.Runtime,
		Image:              cfg.Judge.Image,
		GoBin:              cfg.Judge.GoBin,
		WorkDir:            cfg.JudgeWorkDir(),
		DataDir:            cfg.DataDirPath(),
		CompileTimeout:     cfg.CompileTimeout(),
		DefaultRunTimeout:  cfg.RunTimeout(),
		MemoryMB:           cfg.Judge.MemoryMB,
		CompileConcurrency: cfg.Judge.CompileConcurrency,
		RunConcurrency:     cfg.Judge.RunConcurrency,
		CloneAuth:          auth,
		CABundle:           caBundle,
		InsecureSkipTLS:    insecure,
		Logf:               logf,
	}
}

func cmdServe(args []string, out, errOut io.Writer) error {
	fs := newFlags("serve", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	logf := loggerTo(errOut)
	gc, err := newGitea(cfg)
	if err != nil {
		return err
	}
	for _, missing := range cfg.SessionSecretMissing() {
		logf("warn: 未配置 %s，钩子/webhook 相关功能会受限", missing)
	}
	srv := httpapi.New(cfg, st, gc, judgeOptions(cfg, gc, logf), logf)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.AutoFreezeLoop(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logf("listening on %s (public=%s)", cfg.Listen, cfg.PublicURL)

	select {
	case <-ctx.Done():
		logf("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	_ = out
	return nil
}

func cmdAdmin(args []string, out, errOut io.Writer) error {
	fs := newFlags("admin", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	username := fs.String("username", "admin", "管理员用户名")
	password := fs.String("password", "", "管理员密码")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *password == "" {
		return fmt.Errorf("必须提供 -password")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := st.CreateAdmin(*username, string(hash)); err != nil {
		return err
	}
	fmt.Fprintf(out, "admin created: %s\n", *username)
	return nil
}

func parseFlexTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q（示例：2027-02-15T14:00:00+08:00）", value)
}

func cmdExam(args []string, out, errOut io.Writer) error {
	fs := newFlags("exam", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	id := fs.String("id", "", "场次 ID")
	title := fs.String("title", "", "场次标题")
	startsAt := fs.String("starts-at", "", "开始时间")
	endsAt := fs.String("ends-at", "", "结束时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *title == "" {
		return fmt.Errorf("必须提供 -id 与 -title")
	}
	starts, err := parseFlexTime(*startsAt)
	if err != nil {
		return err
	}
	ends, err := parseFlexTime(*endsAt)
	if err != nil {
		return err
	}
	if starts.IsZero() {
		starts = time.Now()
	}
	if ends.IsZero() {
		ends = starts.Add(2 * time.Hour)
	}
	if !ends.After(starts) {
		return fmt.Errorf("结束时间必须晚于开始时间")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.CreateExam(&store.Exam{ID: *id, Title: *title, StartsAt: starts, EndsAt: ends}); err != nil {
		return err
	}
	fmt.Fprintf(out, "exam created: %s (%s -> %s)\n", *id, starts.Format(time.RFC3339), ends.Format(time.RFC3339))
	return nil
}

func cmdRoster(args []string, out, errOut io.Writer) error {
	fs := newFlags("roster", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	csvPath := fs.String("csv", "", "名单 CSV 路径，- 表示标准输入")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" || *csvPath == "" {
		return fmt.Errorf("必须提供 -exam 与 -csv")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.GetExam(*examID); err != nil {
		return err
	}

	var reader io.Reader
	if *csvPath == "-" {
		reader = os.Stdin
	} else {
		f, err := os.Open(*csvPath)
		if err != nil {
			return err
		}
		defer f.Close()
		reader = f
	}
	parsed, err := roster.Parse(reader)
	if err != nil {
		return err
	}
	imported := 0
	for _, row := range parsed.Rows {
		if _, err := st.UpsertCandidate(&store.Candidate{
			ExamID:    *examID,
			StudentNo: row.StudentNo,
			Name:      row.Name,
			Track:     row.Track,
			Variant:   row.Variant,
		}); err != nil {
			fmt.Fprintf(errOut, "第 %d 行导入失败: %v\n", row.Line, err)
			continue
		}
		imported++
	}
	for _, e := range parsed.Errors {
		fmt.Fprintf(errOut, "第 %d 行跳过: %s\n", e.Line, e.Reason)
	}
	fmt.Fprintf(out, "imported=%d skipped=%d\n", imported, len(parsed.Errors))
	if imported == 0 {
		return fmt.Errorf("没有导入任何考生")
	}
	return nil
}

func cmdProblem(args []string, out, errOut io.Writer) error {
	fs := newFlags("problem", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	dir := fs.String("dir", "", "题目目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" || *dir == "" {
		return fmt.Errorf("必须提供 -exam 与 -dir")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.GetExam(*examID); err != nil {
		return err
	}
	count, err := problem.Import(st, cfg.DataDirPath(), *examID, *dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "imported problems=%d\n", count)
	return nil
}

func cmdProvision(args []string, out, errOut io.Writer) error {
	fs := newFlags("provision", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	slipsPath := fs.String("slips", "", "凭条输出 CSV")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" {
		return fmt.Errorf("必须提供 -exam")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	gc, err := newGitea(cfg)
	if err != nil {
		return err
	}
	if gc == nil {
		return fmt.Errorf("未配置 gitea.url")
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	p := provision.New(cfg, st, gc, loggerTo(errOut))
	summary, err := p.Run(context.Background(), *examID)
	if err != nil {
		return err
	}
	for _, res := range summary.Results {
		if res.Err != nil {
			fmt.Fprintf(errOut, "%s failed: %v\n", res.StudentNo, res.Err)
		}
	}
	created, seeded := 0, 0
	for _, res := range summary.Results {
		if res.UserCreated {
			created++
		}
		if res.RepoSeeded {
			seeded++
		}
	}
	fmt.Fprintf(out, "total=%d created_users=%d seeded_repos=%d failed=%d\n",
		len(summary.Results), created, seeded, summary.Failed())

	if *slipsPath != "" {
		f, err := os.OpenFile(*slipsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		err = roster.WriteSlips(f, summary.Slips())
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(out, "slips written: %s (%d)\n", *slipsPath, len(summary.Slips()))
	}
	if summary.Failed() > 0 {
		return fmt.Errorf("%d 名考生开通失败", summary.Failed())
	}
	return nil
}

func cmdToken(args []string, out, errOut io.Writer) error {
	fs := newFlags("token", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	student := fs.String("student", "", "学号")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" || *student == "" {
		return fmt.Errorf("必须提供 -exam 与 -student")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	gc, err := newGitea(cfg)
	if err != nil {
		return err
	}
	if gc == nil {
		return fmt.Errorf("未配置 gitea.url")
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	token, err := provision.New(cfg, st, gc, loggerTo(errOut)).ResetToken(context.Background(), *examID, *student)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "student=%s token=%s\n", *student, token)
	return nil
}

func cmdJudge(args []string, out, errOut io.Writer) error {
	fs := newFlags("judge", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	outPath := fs.String("out", "-", "结果 CSV 路径，- 表示标准输出")
	student := fs.String("student", "", "只判某学号")
	runtime := fs.String("runtime", "", "覆盖 judge.runtime（docker/local）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" {
		return fmt.Errorf("必须提供 -exam")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *runtime != "" {
		cfg.Judge.Runtime = *runtime
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.GetExam(*examID); err != nil {
		return err
	}

	logf := loggerTo(errOut)
	gc, err := newGitea(cfg)
	if err != nil {
		return err
	}
	runner := judge.New(judgeOptions(cfg, gc, logf), st)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	results, err := runner.RunExam(ctx, *examID, *student)
	if err != nil {
		return err
	}
	problems, err := st.ListProblems(*examID)
	if err != nil {
		return err
	}

	var w io.Writer = out
	var f *os.File
	if *outPath != "-" {
		f, err = os.Create(*outPath)
		if err != nil {
			return err
		}
		w = f
	}
	if err := judge.WriteCSV(w, results, problems); err != nil {
		if f != nil {
			f.Close()
		}
		return err
	}
	if f != nil {
		if err := f.Close(); err != nil {
			return err
		}
	}

	accepted := 0
	for _, r := range results {
		if judge.Accepted(r) {
			accepted++
		}
	}
	fmt.Fprintf(errOut, "judged=%d accepted_candidates=%d\n", len(results), accepted)
	return nil
}

func cmdReview(args []string, out, errOut io.Writer) error {
	fs := newFlags("review", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	student := fs.String("student", "", "只复核某学号")
	outPath := fs.String("out", "-", "时间线 CSV 路径，- 表示标准输出")
	includeHeartbeat := fs.Bool("include-heartbeat", false, "包含心跳空提交")
	bulkLines := fs.Int("bulk-lines", 200, "单次增删行数达到多少标记 bulk_change")
	maxGap := fs.String("max-gap", "10m", "提交间隔超过多久标记 long_gap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" {
		return fmt.Errorf("必须提供 -exam")
	}
	gap, err := time.ParseDuration(*maxGap)
	if err != nil {
		return fmt.Errorf("max-gap 格式错误: %w", err)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	opts := review.Options{
		WorkDir:          cfg.JudgeWorkDir(),
		IncludeHeartbeat: *includeHeartbeat,
		BulkLines:        *bulkLines,
		MaxGap:           gap,
		Logf:             loggerTo(errOut),
	}
	gc, err := newGitea(cfg)
	if err != nil {
		return err
	}
	if gc != nil {
		user, pass := gc.BasicAuth()
		opts.Auth = &githttp.BasicAuth{Username: user, Password: pass}
		opts.CABundle = gc.CABundle()
		opts.InsecureSkipTLS = gc.InsecureSkipTLS()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	rows, err := review.Run(ctx, st, *examID, *student, opts)
	if err != nil {
		return err
	}

	var w io.Writer = out
	var f *os.File
	if *outPath != "-" {
		f, err = os.Create(*outPath)
		if err != nil {
			return err
		}
		w = f
	}
	if err := review.WriteCSV(w, rows); err != nil {
		if f != nil {
			f.Close()
		}
		return err
	}
	if f != nil {
		if err := f.Close(); err != nil {
			return err
		}
	}

	bulk, gaps := 0, 0
	for _, r := range rows {
		if strings.Contains(r.Flags, "bulk_change") {
			bulk++
		}
		if strings.Contains(r.Flags, "long_gap") {
			gaps++
		}
	}
	fmt.Fprintf(errOut, "review rows=%d bulk_change=%d long_gap=%d\n", len(rows), bulk, gaps)
	return nil
}

func cmdExport(args []string, out, errOut io.Writer) error {
	fs := newFlags("export", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	examID := fs.String("exam", "", "场次 ID")
	outPath := fs.String("out", "-", "报表 CSV 路径，- 表示标准输出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *examID == "" {
		return fmt.Errorf("必须提供 -exam")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	data, err := report.Build(st, *examID)
	if err != nil {
		return err
	}
	if *outPath == "-" {
		_, err = out.Write(data)
		return err
	}
	return os.WriteFile(*outPath, data, 0o644)
}

func cmdBackup(args []string, out, errOut io.Writer) error {
	fs := newFlags("backup", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	outPath := fs.String("out", "", "备份文件路径（.tar.gz）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPath == "" {
		return fmt.Errorf("必须提供 -out")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := backup.Create(st, cfg.DataDirPath(), *outPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "backup written: %s\n", *outPath)
	return nil
}

func cmdRestore(args []string, out, errOut io.Writer) error {
	fs := newFlags("restore", errOut)
	configPath := fs.String("config", "config.yaml", "配置文件")
	inPath := fs.String("in", "", "备份文件路径（.tar.gz）")
	force := fs.Bool("force", false, "覆盖已有数据库与数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" {
		return fmt.Errorf("必须提供 -in")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *force {
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(cfg.DBPath() + suffix)
		}
	}
	if err := backup.Restore(*inPath, cfg.DBPath(), cfg.DataDirPath(), *force); err != nil {
		return err
	}
	abs, _ := filepath.Abs(*inPath)
	fmt.Fprintf(out, "restored %s -> %s\n", abs, cfg.DBPath())
	return nil
}
