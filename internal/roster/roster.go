// Package roster 负责名单 CSV 的解析与凭条输出。
package roster

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Row 是一行合法名单。
type Row struct {
	Line      int
	StudentNo string
	Name      string
	Track     string
	Variant   string
}

// RowError 是一行不合法的原因。
type RowError struct {
	Line   int
	Reason string
}

// Result 是解析结果：合法行与错误行分开返回，便于 V-01 的混合名单。
type Result struct {
	Rows   []Row
	Errors []RowError
}

// Parse 解析名单 CSV，表头需包含 student_no、name，可选 track、variant。
func Parse(r io.Reader) (*Result, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取表头: %w", err)
	}
	index := map[string]int{}
	for i, h := range header {
		h = strings.TrimPrefix(strings.TrimSpace(h), "\ufeff")
		index[strings.ToLower(h)] = i
	}
	for _, required := range []string{"student_no", "name"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("表头缺少列 %s", required)
		}
	}

	res := &Result{}
	seen := map[string]int{}
	line := 1
	for {
		line++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 行: %w", line, err)
		}
		if len(record) == 0 || strings.TrimSpace(strings.Join(record, "")) == "" {
			continue
		}

		get := func(name string) string {
			i, ok := index[name]
			if !ok || i >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[i])
		}
		studentNo := get("student_no")
		name := get("name")
		track := get("track")
		variant := get("variant")
		if track == "" {
			track = "default"
		}
		if variant == "" {
			variant = "default"
		}

		switch {
		case studentNo == "":
			res.Errors = append(res.Errors, RowError{Line: line, Reason: "学号为空"})
			continue
		case !validStudentNo.MatchString(studentNo):
			res.Errors = append(res.Errors, RowError{Line: line, Reason: "学号只能包含字母、数字、下划线和短横线"})
			continue
		case name == "":
			res.Errors = append(res.Errors, RowError{Line: line, Reason: "姓名为空"})
			continue
		}
		if first, dup := seen[studentNo]; dup {
			res.Errors = append(res.Errors, RowError{Line: line, Reason: fmt.Sprintf("学号与第 %d 行重复", first)})
			continue
		}
		seen[studentNo] = line
		res.Rows = append(res.Rows, Row{Line: line, StudentNo: studentNo, Name: name, Track: track, Variant: variant})
	}
	return res, nil
}

var validStudentNo = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Slip 是发给考生的凭条。
type Slip struct {
	StudentNo string
	Name      string
	RepoURL   string
	Token     string
}

// WriteSlips 输出凭条 CSV。
func WriteSlips(w io.Writer, slips []Slip) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"student_no", "name", "repo_url", "token"}); err != nil {
		return err
	}
	for _, s := range slips {
		if err := cw.Write([]string{s.StudentNo, s.Name, s.RepoURL, s.Token}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
