package roster

import (
	"bytes"
	"strings"
	"testing"
)

// V-01：70 行名单，含 2 行错误。
func TestParseMixedRoster(t *testing.T) {
	var b strings.Builder
	b.WriteString("student_no,name,track,variant\n")
	for i := 1; i <= 68; i++ {
		b.WriteString("2023" + pad(i) + ",考生" + pad(i) + ",A,A\n")
	}
	b.WriteString("2023998,,B,B\n")     // 姓名为空
	b.WriteString("2023999,重复, C, C\n") // 合法
	b.WriteString("2023999,重复学号,D,D\n") // 学号重复

	res, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Rows) != 69 {
		t.Fatalf("合法行 = %d, want 69", len(res.Rows))
	}
	if len(res.Errors) != 2 {
		t.Fatalf("错误行 = %d, want 2: %+v", len(res.Errors), res.Errors)
	}
	if res.Rows[0].Track != "A" || res.Rows[0].Variant != "A" {
		t.Fatalf("默认值解析错误: %+v", res.Rows[0])
	}
	if res.Rows[68].Track != "C" || res.Rows[68].Variant != "C" {
		t.Fatalf("带空格字段未 trim: %+v", res.Rows[68])
	}
}

func TestParseDefaults(t *testing.T) {
	res, err := Parse(strings.NewReader("student_no,name\n20231234,张三\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	row := res.Rows[0]
	if row.Track != "default" || row.Variant != "default" {
		t.Fatalf("缺省 track/variant 错误: %+v", row)
	}
}

func TestParseBOMHeader(t *testing.T) {
	res, err := Parse(strings.NewReader("\ufeffstudent_no,name\n20231234,张三\n"))
	if err != nil {
		t.Fatalf("BOM 表头解析失败: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].StudentNo != "20231234" {
		t.Fatalf("BOM 名单解析错误: %+v", res)
	}
}

func TestParseBadHeader(t *testing.T) {
	if _, err := Parse(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Fatal("缺表头应报错")
	}
}

func TestWriteSlips(t *testing.T) {
	var buf bytes.Buffer
	err := WriteSlips(&buf, []Slip{{StudentNo: "20231234", Name: "张三", RepoURL: "http://gitea/x.git", Token: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"student_no,name,repo_url,token", "20231234", "http://gitea/x.git", "abc"} {
		if !strings.Contains(out, want) {
			t.Fatalf("凭条缺少 %q:\n%s", want, out)
		}
	}
}

func pad(i int) string {
	s := "000" + itoa(i)
	return s[len(s)-3:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
