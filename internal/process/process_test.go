package process

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTaskAcceptsModelSelectedDuration(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"task":"test","timeout":"5m"}`), &task); err != nil {
		t.Fatalf("decode duration task: %v", err)
	}
	if task.Name != "test" || task.Timeout != 5*time.Minute {
		t.Fatalf("unexpected task: %+v", task)
	}
}

func TestTaskRejectsNegativeDuration(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"task":"test","timeout":"-1s"}`), &task); err == nil {
		t.Fatal("expected negative timeout to be rejected")
	}
}
