package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// watcher 递归监听工作区，忽略 .git 与编辑器临时文件。
type watcher struct {
	fsw    *fsnotify.Watcher
	done   chan struct{}
	wg     sync.WaitGroup
	closed bool
}

func newWatcher(root string, onChange func()) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("创建文件监听: %w", err)
	}
	w := &watcher{fsw: fsw, done: make(chan struct{})}
	if err := w.addRecursive(root); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	w.wg.Add(1)
	go w.loop(onChange)
	return w, nil
}

func (w *watcher) loop(onChange func()) {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if ignorePath(ev.Name) {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addRecursive(ev.Name)
				}
			}
			onChange()
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// 监听错误不致命：下一次文件改动仍会触发提交。
		}
	}
}

func (w *watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(p); err != nil {
			return fmt.Errorf("监听目录 %s: %w", p, err)
		}
		return nil
	})
}

// Close 停止监听。
func (w *watcher) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	close(w.done)
	err := w.fsw.Close()
	w.wg.Wait()
	return err
}

// ignorePath 判断路径是否属于不应触发提交的文件。
func ignorePath(p string) bool {
	slashed := filepathToSlash(p)
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".git" {
			return true
		}
	}
	base := filepath.Base(p)
	if strings.HasPrefix(base, ".exam-agent") {
		return true
	}
	switch {
	case strings.HasSuffix(base, "~"),
		strings.HasPrefix(base, ".#"),
		strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swo"),
		strings.HasSuffix(base, ".tmp"),
		strings.HasSuffix(base, ".crdownload"),
		base == "4913":
		return true
	}
	return false
}
