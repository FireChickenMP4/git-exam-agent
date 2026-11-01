// Package problem 从目录导入题面、变体与测试用例。
//
// 目录结构：
//
//	<root>/<key>/problem.md            题面（markdown，可选）
//	<root>/<key>/meta.json             标题、分值、时限（可选）
//	<root>/<key>/variants/<seed>.md    题面变体（可选）
//	<root>/<key>/cases/NN.in|NN.out    隐藏用例（必须成对）
//	<root>/<key>/samples/NN.in|NN.out  样例（可选）
package problem

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"exam.local/tools/internal/store"
)

type meta struct {
	Title         string `json:"title"`
	Score         int    `json:"score"`
	TimeLimitMS   int    `json:"time_limit_ms"`
	MemoryLimitMB int    `json:"memory_limit_mb"`
}

type caseFile struct {
	name     string
	input    string
	expected string
}

// Import 导入目录下全部题目，返回题目数量。
func Import(st *store.Store, dataDir, examID, root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("读取题目目录: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		key := entry.Name()
		base := filepath.Join(root, key)
		if err := importOne(st, dataDir, examID, key, base); err != nil {
			return count, fmt.Errorf("题目 %s: %w", key, err)
		}
		count++
	}
	if count == 0 {
		return 0, fmt.Errorf("目录 %s 下没有题目", root)
	}
	return count, nil
}

func importOne(st *store.Store, dataDir, examID, key, base string) error {
	m := meta{Title: key, Score: 100, TimeLimitMS: 3000, MemoryLimitMB: 256}
	if raw, err := os.ReadFile(filepath.Join(base, "meta.json")); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("解析 meta.json: %w", err)
		}
		if m.Title == "" {
			m.Title = key
		}
		if m.Score <= 0 {
			m.Score = 100
		}
		if m.TimeLimitMS <= 0 {
			m.TimeLimitMS = 3000
		}
		if m.MemoryLimitMB <= 0 {
			m.MemoryLimitMB = 256
		}
	}

	statement := ""
	if raw, err := os.ReadFile(filepath.Join(base, "problem.md")); err == nil {
		statement = string(raw)
	}

	hidden, err := collectCases(base, "cases")
	if err != nil {
		return err
	}
	if len(hidden) == 0 {
		return fmt.Errorf("cases/ 下没有成对的 .in/.out 用例")
	}
	samples, err := collectCases(base, "samples")
	if err != nil {
		return err
	}

	var cases []store.TestCase
	scorePer := m.Score / len(hidden)
	remainder := m.Score % len(hidden)
	for i, cf := range hidden {
		inRel, outRel, err := copyCase(dataDir, examID, key, "cases", cf)
		if err != nil {
			return err
		}
		score := scorePer
		if i < remainder {
			score++
		}
		cases = append(cases, store.TestCase{
			CaseNo:       i + 1,
			InputPath:    inRel,
			ExpectedPath: outRel,
			Score:        score,
			Sort:         i + 1,
		})
	}
	for i, cf := range samples {
		inRel, outRel, err := copyCase(dataDir, examID, key, "samples", cf)
		if err != nil {
			return err
		}
		cases = append(cases, store.TestCase{
			CaseNo:       i + 1,
			InputPath:    inRel,
			ExpectedPath: outRel,
			IsSample:     true,
			Sort:         1000 + i,
		})
	}

	variants, err := collectVariants(base)
	if err != nil {
		return err
	}

	_, err = st.UpsertProblem(&store.Problem{
		ExamID:        examID,
		Key:           key,
		Title:         m.Title,
		StatementMD:   statement,
		Score:         m.Score,
		TimeLimitMS:   m.TimeLimitMS,
		MemoryLimitMB: m.MemoryLimitMB,
		Variants:      variants,
		Cases:         cases,
	})
	return err
}

func relJoin(parts ...string) string {
	return filepath.ToSlash(filepath.Join(parts...))
}

func copyCase(dataDir, examID, key, kind string, cf caseFile) (string, string, error) {
	dstDir := filepath.Join(dataDir, "problems", examID, key, kind)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", "", err
	}
	for _, src := range []string{cf.input, cf.expected} {
		dst := filepath.Join(dstDir, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			return "", "", err
		}
	}
	return relJoin("problems", examID, key, kind, cf.name+".in"),
		relJoin("problems", examID, key, kind, cf.name+".out"), nil
}

func collectCases(base, kind string) ([]caseFile, error) {
	dir := filepath.Join(base, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".in") {
			continue
		}
		names[strings.TrimSuffix(e.Name(), ".in")] = true
	}
	keys := make([]string, 0, len(names))
	for n := range names {
		keys = append(keys, n)
	}
	sort.Strings(keys)

	var out []caseFile
	for _, n := range keys {
		in := filepath.Join(dir, n+".in")
		exp := filepath.Join(dir, n+".out")
		if _, err := os.Stat(exp); err != nil {
			return nil, fmt.Errorf("%s/%s.in 缺少对应的 .out", kind, n)
		}
		out = append(out, caseFile{name: n, input: in, expected: exp})
	}
	return out, nil
}

func collectVariants(base string) ([]store.Variant, error) {
	dir := filepath.Join(base, "variants")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []store.Variant
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, store.Variant{
			Seed:        strings.TrimSuffix(e.Name(), ".md"),
			StatementMD: string(raw),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seed < out[j].Seed })
	return out, nil
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
