// Package report 生成场次报表 CSV。
package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"time"

	"exam.local/tools/internal/store"
)

// Build 输出每个考生一行的报表。
func Build(st *store.Store, examID string) ([]byte, error) {
	candidates, err := st.ListCandidates(examID)
	if err != nil {
		return nil, err
	}
	problems, err := st.ListProblems(examID)
	if err != nil {
		return nil, err
	}
	live, err := st.LiveRows(examID)
	if err != nil {
		return nil, err
	}
	results, err := st.ListJudgeResults(examID)
	if err != nil {
		return nil, err
	}

	liveByCandidate := map[int64]store.LiveRow{}
	for _, row := range live {
		liveByCandidate[row.CandidateID] = row
	}
	problemKey := map[int64]string{}
	for _, p := range problems {
		problemKey[p.ID] = p.Key
	}
	type judged struct {
		score int
		label string
	}
	judgedByCandidate := map[int64]map[string]judged{}
	totalByCandidate := map[int64]int{}
	for _, r := range results {
		if judgedByCandidate[r.CandidateID] == nil {
			judgedByCandidate[r.CandidateID] = map[string]judged{}
		}
		judgedByCandidate[r.CandidateID][problemKey[r.ProblemID]] = judged{
			score: r.Score,
			label: fmt.Sprintf("%d/%d", r.Passed, r.Total),
		}
		totalByCandidate[r.CandidateID] += r.Score
	}

	keys := make([]string, 0, len(problems))
	for _, p := range problems {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	header := []string{"student_no", "name", "track", "status", "push_count", "last_push_at", "final_commit", "archived", "judge_score"}
	header = append(header, keys...)
	if err := cw.Write(header); err != nil {
		return nil, err
	}
	for _, c := range candidates {
		row := liveByCandidate[c.ID]
		line := []string{
			c.StudentNo,
			c.Name,
			c.Track,
			c.Status,
			strconv.Itoa(row.PushCount),
			formatTime(row.LastPushAt),
			row.FinalCommit,
			strconv.FormatBool(row.Archived),
			strconv.Itoa(totalByCandidate[c.ID]),
		}
		per := judgedByCandidate[c.ID]
		for _, k := range keys {
			label := ""
			if j, ok := per[k]; ok {
				label = j.label
			}
			line = append(line, label)
		}
		if err := cw.Write(line); err != nil {
			return nil, err
		}
	}
	cw.Flush()
	return buf.Bytes(), cw.Error()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
