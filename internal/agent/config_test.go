package agent

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRandForTest() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestLoadConfigDefaultsAndPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".exam-agent.json")
	content := `{
  "repo": "http://exam.local:3000/20231234/exam-2027-20231234.git",
  "username": "20231234",
  "token": "abc",
  "workspace": "exam-2027-20231234"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DebounceMS != 3000 || cfg.PushIntervalMSMin != 10000 || cfg.HeartbeatMSMax != 25000 {
		t.Fatalf("默认值未生效: %+v", cfg)
	}
	want := filepath.Join(dir, "exam-2027-20231234")
	got, err := cfg.WorkspaceDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("WorkspaceDir = %s, want %s", got, want)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"repo":"x","username":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("缺少 token 应报错")
	}

	if err := os.WriteFile(path, []byte(`{"repo":"x","username":"y","token":"t","push_interval_ms_min":30000,"push_interval_ms_max":10000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("min > max 应报错")
	}

	if err := os.WriteFile(path, []byte(`{"repo":"x","username":"y","token":"t","debounce_ms":10}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("debounce_ms 过小应报错")
	}
}

func TestIgnorePath(t *testing.T) {
	ignored := []string{
		"/tmp/repo/.git/index",
		`C:\repo\.git\HEAD`,
		"/tmp/repo/main.go~",
		"/tmp/repo/.main.go.swp",
		"/tmp/repo/4913",
		"/tmp/repo/.exam-agent.json",
		"/tmp/repo/.exam-agent.events.jsonl",
	}
	for _, p := range ignored {
		if !ignorePath(p) {
			t.Errorf("ignorePath(%q) = false, want true", p)
		}
	}
	kept := []string{"/tmp/repo/main.go", "/tmp/repo/problems/sum/main.go", "/tmp/repo/go.mod"}
	for _, p := range kept {
		if ignorePath(p) {
			t.Errorf("ignorePath(%q) = true, want false", p)
		}
	}
}

func TestBackoffDelay(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second},
		{100, 60 * time.Second},
		{0, 2 * time.Second},
	}
	for _, c := range cases {
		if got := backoffDelay(c.attempts); got != c.want {
			t.Errorf("backoffDelay(%d) = %s, want %s", c.attempts, got, c.want)
		}
	}
}

func TestRandBetween(t *testing.T) {
	a := &Agent{rand: newRandForTest()}
	lo, hi := 10*time.Second, 20*time.Second
	for i := 0; i < 100; i++ {
		d := a.randBetween(lo, hi)
		if d < lo || d > hi {
			t.Fatalf("randBetween 越界: %s", d)
		}
	}
	if got := a.randBetween(5*time.Second, 5*time.Second); got != 5*time.Second {
		t.Fatalf("固定区间返回 %s", got)
	}
}
