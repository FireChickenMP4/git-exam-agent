package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestEventLogRemoteBulkEdit(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	log, err := OpenEventLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.EnableRemote(srv.URL, "20231234", "token-abc")
	log.Write("bulk_edit", map[string]any{"path": "main.go", "bytes": 5000})

	select {
	case body := <-received:
		events, _ := body["events"].([]any)
		if len(events) != 1 {
			t.Fatalf("events = %v", body["events"])
		}
		first, _ := events[0].(map[string]any)
		if first["type"] != "bulk_edit" {
			t.Fatalf("type = %v", first["type"])
		}
		if body["student_no"] != "20231234" {
			t.Fatalf("student_no = %v", body["student_no"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未上报事件")
	}
}
