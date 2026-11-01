package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"exam.local/tools/internal/paths"
)

// bulkTracker 在保存级别观察文件增长：同一次保存让白名单文件增长超过阈值时上报一次。
// 它只记录字节数，不读取内容、不接触剪贴板，不能证明“粘贴”，只能作为大块改动的信号。
type bulkTracker struct {
	root      string
	threshold int64
	debounce  time.Duration
	emit      func(path string, delta, size, prev int64)

	mu     sync.Mutex
	last   map[string]int64
	timers map[string]*time.Timer
}

func newBulkTracker(root string, threshold int64, debounce time.Duration, emit func(path string, delta, size, prev int64)) *bulkTracker {
	if threshold <= 0 {
		return nil
	}
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	return &bulkTracker{
		root:      root,
		threshold: threshold,
		debounce:  debounce,
		emit:      emit,
		last:      make(map[string]int64),
		timers:    make(map[string]*time.Timer),
	}
}

// Note 记录一次文件系统事件；保存造成的连续写入会按 debounce 合并。
func (b *bulkTracker) Note(path string, op fsnotify.Op) {
	if b == nil {
		return
	}
	rel, ok := b.rel(path)
	if !ok {
		return
	}
	if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		b.forget(path)
		return
	}

	b.mu.Lock()
	if _, seen := b.last[path]; !seen {
		// 首次见到文件只记录基线，不判断。
		info, err := os.Stat(path)
		if err == nil {
			b.last[path] = info.Size()
		}
		b.mu.Unlock()
		_ = rel
		return
	}
	if timer, ok := b.timers[path]; ok {
		timer.Stop()
	}
	b.timers[path] = time.AfterFunc(b.debounce, func() { b.check(path) })
	b.mu.Unlock()
}

func (b *bulkTracker) check(path string) {
	b.mu.Lock()
	prev, ok := b.last[path]
	delete(b.timers, path)
	b.mu.Unlock()
	if !ok {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		b.forget(path)
		return
	}
	size := info.Size()
	b.mu.Lock()
	b.last[path] = size
	b.mu.Unlock()

	delta := size - prev
	if delta < b.threshold {
		return
	}
	rel, ok := b.rel(path)
	if !ok {
		return
	}
	b.emit(rel, delta, size, prev)
}

func (b *bulkTracker) forget(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.last, path)
	if timer, ok := b.timers[path]; ok {
		timer.Stop()
		delete(b.timers, path)
	}
}

func (b *bulkTracker) rel(path string) (string, bool) {
	rel, err := filepath.Rel(b.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if !paths.Allowed(rel) {
		return "", false
	}
	return rel, true
}
