package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// EventLog 把 agent 事件按 JSONL 追加写入本地文件，可选同步到 examd。
type EventLog struct {
	mu sync.Mutex
	f  *os.File

	remoteURL string
	student   string
	token     string
	client    *http.Client
}

type eventRecord struct {
	T       string         `json:"t"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// OpenEventLog 打开（或创建）事件日志。
func OpenEventLog(file string) (*EventLog, error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开事件日志 %s: %w", file, err)
	}
	return &EventLog{f: f}, nil
}

// EnableRemote 配置 examd 事件上报地址；失败只丢事件，不影响本地日志。
func (l *EventLog) EnableRemote(baseURL, student, token string) {
	if l == nil || baseURL == "" {
		return
	}
	l.remoteURL = strings.TrimRight(baseURL, "/") + "/api/agent/events"
	l.student = student
	l.token = token
	l.client = &http.Client{Timeout: 3 * time.Second}
}

// Write 追加一条事件；写失败只忽略，日志不阻塞主循环。
func (l *EventLog) Write(typ string, payload map[string]any) {
	if l == nil || l.f == nil {
		return
	}
	rec := eventRecord{
		T:       time.Now().Format(time.RFC3339),
		Type:    typ,
		Payload: payload,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	_, _ = l.f.Write(append(line, '\n'))
	remoteURL := l.remoteURL
	client := l.client
	student, token := l.student, l.token
	l.mu.Unlock()

	if remoteURL != "" && client != nil {
		go postEvent(client, remoteURL, student, token, rec)
	}
}

func postEvent(client *http.Client, url, student, token string, rec eventRecord) {
	body, err := json.Marshal(map[string]any{
		"student_no": student,
		"token":      token,
		"events":     []eventRecord{rec},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// Close 关闭日志文件。
func (l *EventLog) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
