package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"exam.local/tools/internal/store"
)

func TestCreateAndRestore(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "examd.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateExam(&store.Exam{ID: "e1", Title: "备份测试"}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "problems", "e1", "sum"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "problems", "e1", "sum", "1.in"), []byte("1 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 备份目录本身不应被递归打包
	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := Create(st, dbPath, dataDir, out); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 数据目录里不能包含 live db/wal/shm（快照是单独的 examd.db）
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(hdr.Name, "data/examd.db") {
			t.Fatalf("备份包混入 live db: %s", hdr.Name)
		}
	}
	gz.Close()
	f.Close()

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	restoreDB := filepath.Join(t.TempDir(), "restored.db")
	restoreData := t.TempDir()
	if err := Restore(out, restoreDB, restoreData, false); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	st2, err := store.Open(restoreDB)
	if err != nil {
		t.Fatal(err)
	}
	exam, err := st2.GetExam("e1")
	if err != nil || exam.Title != "备份测试" {
		t.Fatalf("恢复后数据错误: %+v %v", exam, err)
	}
	if err := st2.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restoreData, "problems", "e1", "sum", "1.in")); err != nil {
		t.Fatalf("数据目录未恢复: %v", err)
	}

	if err := Restore(out, restoreDB, restoreData, false); err == nil {
		t.Fatal("已有数据库且未 -force 时应拒绝")
	}
	if err := Restore(out, restoreDB, restoreData, true); err != nil {
		t.Fatalf("force 恢复失败: %v", err)
	}
}
