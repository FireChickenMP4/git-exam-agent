package gitea

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRejectsInvalidCA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New("http://127.0.0.1:3000", "", "", "", path, false); err == nil {
		t.Fatal("无效 CA 文件应报错")
	}
}

func TestNewWithoutCA(t *testing.T) {
	c, err := New("http://127.0.0.1:3000/", "", "user", "pass", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if c.CABundle() != nil || !c.InsecureSkipTLS() {
		t.Fatalf("TLS 配置错误: ca=%v insecure=%v", c.CABundle(), c.InsecureSkipTLS())
	}
	user, pass := c.BasicAuth()
	if user != "user" || pass != "pass" {
		t.Fatalf("BasicAuth = %q/%q", user, pass)
	}
}
