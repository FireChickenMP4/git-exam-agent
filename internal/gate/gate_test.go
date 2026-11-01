package gate

import (
	"strings"
	"testing"
)

func basePush() Push {
	return Push{
		Ref:    "refs/heads/main",
		OldSHA: "1111111111111111111111111111111111111111",
		NewSHA: "2222222222222222222222222222222222222222",
		Files:  []File{{Path: "main.go", Size: 1024}},
	}
}

func TestCheckAllowsNormalPush(t *testing.T) {
	if err := Check(basePush(), DefaultRules()); err != nil {
		t.Fatalf("正常推送被拒绝: %v", err)
	}
}

func TestCheckRejects(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Push)
		want string
	}{
		{"非 main 分支", func(p *Push) { p.Ref = "refs/heads/dev" }, "只允许推送到 main"},
		{"删除分支", func(p *Push) { p.NewSHA = strings.Repeat("0", 40) }, "不允许删除"},
		{"force-push", func(p *Push) { p.ForcePush = true }, "force-push"},
		{"非 Go 文件", func(p *Push) { p.Files = append(p.Files, File{Path: "notes.md"}) }, "notes.md"},
		{"单文件过大", func(p *Push) { p.Files = []File{{Path: "big.go", Size: 6 << 20}} }, "超过 5 MB"},
		{"单次新增过大", func(p *Push) { p.AddedBytes = 21 << 20 }, "单次新增"},
		{"仓库过大", func(p *Push) { p.RepoBytes = 201 << 20 }, "仓库超过"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := basePush()
			tt.mod(&p)
			err := Check(p, DefaultRules())
			if err == nil {
				t.Fatalf("期望拒绝")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("错误信息 %q 不含 %q", err.Error(), tt.want)
			}
		})
	}
}

func TestZeroSHA(t *testing.T) {
	if !ZeroSHA(strings.Repeat("0", 40)) {
		t.Fatal("全零 SHA 应识别为删除")
	}
	if ZeroSHA("1234") {
		t.Fatal("非零 SHA 误判")
	}
}
