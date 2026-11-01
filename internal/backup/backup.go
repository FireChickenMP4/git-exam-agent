// Package backup 实现数据库与数据目录的备份/恢复。
package backup

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exam.local/tools/internal/store"
)

const (
	dbEntry   = "examd.db"
	dataEntry = "data"
)

// Create 备份数据库快照与数据目录（排除 backups/judge 临时目录）。
func Create(st *store.Store, dataDir, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("创建备份目录: %w", err)
	}
	tmp, err := os.MkdirTemp("", "examd-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	snapshot := filepath.Join(tmp, dbEntry)
	if err := vacuum(st.DB(), snapshot); err != nil {
		return err
	}

	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("创建备份文件: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, snapshot, dbEntry); err != nil {
		return err
	}
	if dataDir != "" {
		if _, err := os.Stat(dataDir); err == nil {
			err := filepath.Walk(dataDir, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				rel, err := filepath.Rel(dataDir, p)
				if err != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				if rel == "." {
					return nil
				}
				base := filepath.Base(rel)
				if info.IsDir() {
					if base == "backups" || base == "judge" {
						return filepath.SkipDir
					}
					return nil
				}
				return addFile(tw, p, filepath.ToSlash(filepath.Join(dataEntry, rel)))
			})
			if err != nil {
				return err
			}
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Restore 从备份恢复；force 为 true 时覆盖已有数据库。
func Restore(in, dbPath, dataDir string, force bool) error {
	if _, err := os.Stat(dbPath); err == nil && !force {
		return fmt.Errorf("数据库已存在: %s（追加 -force 覆盖）", dbPath)
	}
	tmp, err := os.MkdirTemp("", "examd-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := extract(in, tmp); err != nil {
		return err
	}
	srcDB := filepath.Join(tmp, dbEntry)
	if _, err := os.Stat(srcDB); err != nil {
		return fmt.Errorf("备份中没有 %s", dbEntry)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	if err := copyFile(srcDB, dbPath); err != nil {
		return fmt.Errorf("恢复数据库: %w", err)
	}

	srcData := filepath.Join(tmp, dataEntry)
	if _, err := os.Stat(srcData); err == nil {
		if err := copyTree(srcData, dataDir); err != nil {
			return fmt.Errorf("恢复数据目录: %w", err)
		}
	}
	return nil
}

func vacuum(db *sql.DB, dest string) error {
	_ = os.Remove(dest)
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("备份数据库: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(name)
	hdr.ModTime = time.Now()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func extract(in, dir string) error {
	f, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("打开备份: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压备份: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取备份: %w", err)
		}
		name := filepath.ToSlash(hdr.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "../") {
			return fmt.Errorf("备份包含非法路径: %s", name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			out, err := os.Create(dst)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(p, target)
	})
}
