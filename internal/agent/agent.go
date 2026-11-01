package agent

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

const (
	tickInterval    = 200 * time.Millisecond
	statusInterval  = 10 * time.Second
	netPollInterval = 5 * time.Second
	eventsFileName  = ".exam-agent.events.jsonl"
	maxSkippedLog   = 8
)

// Agent 是 exam-agent 的主循环状态，只在单 goroutine 内推进。
type Agent struct {
	cfg      *Config
	out      io.Writer
	repo     *git.Repository
	wt       *git.Worktree
	auth     transport.AuthMethod
	caBundle []byte

	events *EventLog
	rand   *rand.Rand
	bulk   *bulkTracker

	mu         sync.Mutex
	dirty      bool
	lastChange time.Time

	pushPending   bool
	nextPush      time.Time
	nextHeartbeat time.Time
	nextNetPoll   time.Time
	nextStatus    time.Time

	lastCommit  time.Time
	lastPush    time.Time
	commitSHA   string
	fails       int
	netSig      string
	lastStatus  string
	lastSkipped string
}

func newAgent(cfg *Config, out io.Writer) (*Agent, error) {
	dir, err := cfg.WorkspaceDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作区 %s: %w", dir, err)
	}

	a := &Agent{
		cfg:  cfg,
		out:  out,
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	if a.repo, a.wt, err = openRepo(dir); err != nil {
		return nil, err
	}
	if err := ensureRemote(a.repo, cfg.Repo); err != nil {
		return nil, err
	}
	a.auth = authFor(cfg)
	if cfg.CAFile != "" {
		raw, err := os.ReadFile(cfg.CAFilePath())
		if err != nil {
			return nil, fmt.Errorf("读取 ca_file: %w", err)
		}
		a.caBundle = raw
	}
	return a, nil
}

// Run 启动监听与主循环，直到 ctx 结束。
func Run(ctx context.Context, cfg *Config, out io.Writer) error {
	a, err := newAgent(cfg, out)
	if err != nil {
		return err
	}

	events, err := OpenEventLog(filepath.Join(cfg.ConfigDir(), eventsFileName))
	if err != nil {
		return err
	}
	defer events.Close()
	a.events = events
	a.enableRemote()

	dir, err := cfg.WorkspaceDir()
	if err != nil {
		return err
	}
	a.bulk = newBulkTracker(dir, cfg.BulkEditBytes, 500*time.Millisecond,
		func(path string, delta, size, prev int64) {
			a.events.Write("bulk_edit", map[string]any{
				"path":      path,
				"bytes":     delta,
				"size":      size,
				"prev_size": prev,
			})
			a.logf("bulk edit %s +%d bytes", path, delta)
		})
	w, err := newWatcher(dir, a.noteChange)
	if err != nil {
		return err
	}
	defer w.Close()

	now := time.Now()
	a.netSig = networkSignature()
	a.nextHeartbeat = now.Add(a.randBetween(cfg.Heartbeat()))
	a.nextNetPoll = now
	a.nextStatus = now

	a.logf("start repo=%s workspace=%s", cfg.Repo, dir)
	events.Write("start", map[string]any{"repo": cfg.Repo, "workspace": dir})

	if res, err := a.commitAll(initialCommitMessage, now, false); err != nil {
		a.logf("initial commit failed: %v", err)
	} else if res.SHA != "" {
		a.lastCommit, a.commitSHA = now, res.SHA
		a.schedulePush(now)
		a.logf("initial commit %s files=%d", shortSHA(res.SHA), len(res.Staged))
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.shutdown()
			return nil
		case <-ticker.C:
			a.step()
		}
	}
}

// RunOnce 执行一次提交并推送，用于 agent 无法常驻时的检查点模式。
func RunOnce(ctx context.Context, cfg *Config, out io.Writer) error {
	a, err := newAgent(cfg, out)
	if err != nil {
		return err
	}
	events, err := OpenEventLog(filepath.Join(cfg.ConfigDir(), eventsFileName))
	if err != nil {
		return err
	}
	defer events.Close()
	a.events = events
	a.enableRemote()

	now := time.Now()
	res, err := a.commitAll(checkpointCommitMessage, now, false)
	if err != nil {
		return err
	}
	if res.SHA != "" {
		a.logf("checkpoint commit %s files=%d", shortSHA(res.SHA), len(res.Staged))
	}
	if err := a.pushRemote(); err != nil {
		events.Write("push_fail", map[string]any{"error": err.Error(), "phase": "checkpoint"})
		return fmt.Errorf("推送失败: %w", err)
	}
	events.Write("checkpoint", map[string]any{"commit": res.SHA})
	a.logf("checkpoint pushed %s", shortSHA(res.SHA))
	return nil
}

func (a *Agent) enableRemote() {
	if a.cfg.ExamdURL == "" {
		return
	}
	a.events.EnableRemote(a.cfg.ExamdURL, a.cfg.Username, a.cfg.Token)
}

func (a *Agent) step() {
	now := time.Now()

	if a.debounceElapsed(now) {
		a.mu.Lock()
		a.dirty = false
		a.mu.Unlock()

		res, err := a.commitAll(autoCommitMessage, now, false)
		switch {
		case err != nil:
			a.logf("commit failed: %v", err)
		case res.SHA != "":
			a.lastCommit, a.commitSHA = now, res.SHA
			a.logf("committed %s files=%d", shortSHA(res.SHA), len(res.Staged))
			a.schedulePush(now)
		}
		a.reportSkipped(res.Skipped)
	}

	if a.pushPending && !now.Before(a.nextPush) {
		a.tryPush(now)
	}

	if !now.Before(a.nextHeartbeat) {
		a.heartbeat(now)
		a.nextHeartbeat = now.Add(a.randBetween(a.cfg.Heartbeat()))
	}

	if !now.Before(a.nextNetPoll) {
		a.pollNetwork()
		a.nextNetPoll = now.Add(netPollInterval)
	}

	if !now.Before(a.nextStatus) {
		a.printStatus(now)
		a.nextStatus = now.Add(statusInterval)
	}
}

func (a *Agent) debounceElapsed(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dirty && now.Sub(a.lastChange) >= a.cfg.Debounce()
}

func (a *Agent) noteChange(path string, op fsnotify.Op) {
	a.mu.Lock()
	a.dirty = true
	a.lastChange = time.Now()
	a.mu.Unlock()
	a.bulk.Note(path, op)
}

func (a *Agent) hasDirty() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dirty
}

// heartbeat 在没有真实改动时提交一个空 commit，证明 agent 还活着（V-07）。
func (a *Agent) heartbeat(now time.Time) {
	minGap, _ := a.cfg.Heartbeat()
	if !a.lastCommit.IsZero() && now.Sub(a.lastCommit) < minGap {
		return
	}
	// 工作区有未提交改动时不要抢在防抖提交前制造 heartbeat，
	// 否则真实改动会被 heartbeat 消息包裹，复核时被当成心跳过滤掉。
	if a.hasPendingChanges() {
		return
	}
	res, err := a.commitEmpty(heartbeatCommitMessage, now)
	if err != nil {
		a.logf("heartbeat failed: %v", err)
		return
	}
	a.lastCommit, a.commitSHA = now, res.SHA
	a.logf("heartbeat %s", shortSHA(res.SHA))
	a.schedulePush(now)
}

func (a *Agent) hasPendingChanges() bool {
	status, err := a.wt.Status()
	if err != nil {
		return false
	}
	for p := range status {
		if allowedPath(p) {
			return true
		}
	}
	return false
}

func (a *Agent) schedulePush(now time.Time) {
	if a.pushPending {
		return
	}
	lo, hi := a.cfg.PushInterval()
	a.pushPending = true
	a.nextPush = now.Add(a.randBetween(lo, hi))
}

func (a *Agent) tryPush(now time.Time) {
	if err := a.pushRemote(); err != nil {
		a.fails++
		delay := backoffDelay(a.fails)
		a.nextPush = now.Add(delay)
		a.logf("push failed attempts=%d retry_in=%s: %v", a.fails, delay, err)
		a.events.Write("push_fail", map[string]any{
			"attempts":    a.fails,
			"error":       err.Error(),
			"retry_in_ms": delay.Milliseconds(),
		})
		return
	}
	a.fails = 0
	a.pushPending = false
	a.lastPush = now
	a.logf("pushed %s", shortSHA(a.commitSHA))
}

func (a *Agent) pollNetwork() {
	sig := networkSignature()
	if sig == a.netSig {
		return
	}
	prev := a.netSig
	a.netSig = sig
	a.logf("network changed %s -> %s", prev, sig)
	a.events.Write("network_change", map[string]any{"from": prev, "to": sig})
}

func (a *Agent) printStatus(now time.Time) {
	line := fmt.Sprintf("status last_commit=%s last_push=%s fails=%d pending=%t net=%s",
		clock(a.lastCommit), clock(a.lastPush), a.fails, a.pushPending, displayNet(a.netSig))
	if line == a.lastStatus {
		return
	}
	a.lastStatus = line
	a.logf("%s", line)
}

// reportSkipped 只在被跳过的文件集合变化时提示一次，避免刷屏。
func (a *Agent) reportSkipped(skipped []string) {
	if len(skipped) == 0 {
		return
	}
	key := strings.Join(skipped, "\x00")
	if key == a.lastSkipped {
		return
	}
	a.lastSkipped = key

	shown := skipped
	if len(shown) > maxSkippedLog {
		shown = shown[:maxSkippedLog]
	}
	more := ""
	if extra := len(skipped) - len(shown); extra > 0 {
		more = fmt.Sprintf(" (+%d more)", extra)
	}
	a.logf("skipped %d non-Go files: %s%s", len(skipped), strings.Join(shown, ", "), more)
}

// shutdown 停机前尽力落盘并补推，避免退出时丢改动。
func (a *Agent) shutdown() {
	now := time.Now()
	if a.hasDirty() {
		if res, err := a.commitAll(autoCommitMessage, now, false); err != nil {
			a.logf("shutdown commit failed: %v", err)
		} else if res.SHA != "" {
			a.lastCommit, a.commitSHA = now, res.SHA
			a.logf("shutdown commit %s files=%d", shortSHA(res.SHA), len(res.Staged))
			a.schedulePush(now)
		}
	}
	// 已经推送失败过就别在退出路径上再等网络超时，下次启动的心跳提交会带上未推内容。
	if a.pushPending && a.fails == 0 {
		if err := a.pushRemote(); err != nil {
			a.logf("shutdown push failed: %v", err)
			a.events.Write("push_fail", map[string]any{
				"attempts": 1,
				"error":    err.Error(),
				"phase":    "shutdown",
			})
		} else {
			a.pushPending = false
			a.logf("shutdown push ok")
		}
	}
	a.events.Write("stop", map[string]any{"unpushed": a.pushPending})
	a.logf("stop")
}

func (a *Agent) randBetween(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(a.rand.Int63n(int64(hi-lo)+1))
}

func (a *Agent) logf(format string, args ...any) {
	if a.out == nil {
		return
	}
	fmt.Fprintf(a.out, "[exam-agent] %s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("15:04:05")
}

func displayNet(s string) string {
	if s == "" {
		return "unknown"
	}
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
