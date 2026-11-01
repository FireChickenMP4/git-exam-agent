package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestBulkTracker(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type event struct {
		path              string
		delta, size, prev int64
	}
	var events []event
	tracker := newBulkTracker(root, 100, time.Hour, func(p string, d, s, prev int64) {
		events = append(events, event{p, d, s, prev})
	})

	// 首次见到文件只记基线
	tracker.Note(mainPath, fsnotify.Write)
	if len(events) != 0 {
		t.Fatal("基线事件不应上报")
	}

	appendBytes := func(n int) {
		f, err := os.OpenFile(mainPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	appendBytes(500)
	tracker.Note(mainPath, fsnotify.Write)
	tracker.check(mainPath)
	if len(events) != 1 {
		t.Fatalf("大块写入应上报一次，实际 %d", len(events))
	}
	if events[0].delta < 500 || events[0].path != "main.go" {
		t.Fatalf("事件内容错误: %+v", events[0])
	}

	appendBytes(10)
	tracker.Note(mainPath, fsnotify.Write)
	tracker.check(mainPath)
	if len(events) != 1 {
		t.Fatalf("小改动不应上报，实际 %d", len(events))
	}

	// 非白名单文件不参与
	other := filepath.Join(root, "notes.md")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker.Note(other, fsnotify.Write)
	appendBytes(1000)
	tracker.check(other)
	if len(events) != 1 {
		t.Fatalf("非 Go 文件不应上报，实际 %d", len(events))
	}
}

func TestBulkTrackerDisabled(t *testing.T) {
	if tr := newBulkTracker(t.TempDir(), 0, time.Second, func(string, int64, int64, int64) {}); tr != nil {
		t.Fatal("阈值 0 应禁用")
	}
}
