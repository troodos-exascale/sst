package sst_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// syncBuffer is a thread-safe wrapper around bytes.Buffer
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Bytes()
}

func TestLogx_Basics(t *testing.T) {
	buf := &bytes.Buffer{}
	log := sst.NewWithWriter(buf)
	ctx := context.Background()

	log.Info(ctx, "hello world", "foo", "bar")

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse json log: %v", err)
	}

	if entry["message"] != "hello world" {
		t.Errorf("expected message 'hello world', got %v", entry["message"])
	}
	if entry["level"] != "info" {
		t.Errorf("expected level 'info', got %v", entry["level"])
	}
	if entry["foo"] != "bar" {
		t.Errorf("expected field foo=bar, got %v", entry["foo"])
	}
}

func TestLogx_Subsystem(t *testing.T) {
	buf := &bytes.Buffer{}
	root := sst.NewWithWriter(buf)
	child := root.Subsystem("worker")
	ctx := context.Background()

	child.Warn(ctx, "work failed")

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse json log: %v", err)
	}

	if entry["subsystem"] != "worker" {
		t.Errorf("expected subsystem 'worker', got %v", entry["subsystem"])
	}
	if entry["message"] != "work failed" {
		t.Errorf("expected message 'work failed', got %v", entry["message"])
	}
}

func TestLogx_With_Immutability(t *testing.T) {
	buf := &bytes.Buffer{}
	parent := sst.NewWithWriter(buf)
	child := parent.With("req_id", "123")
	ctx := context.Background()

	// Log from parent - should NOT have req_id
	parent.Info(ctx, "parent log")
	// Log from child - SHOULD have req_id
	child.Info(ctx, "child log")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}

	// Check parent line
	var pEntry map[string]interface{}
	json.Unmarshal([]byte(lines[0]), &pEntry)
	if _, ok := pEntry["req_id"]; ok {
		t.Error("parent log should not have 'req_id'")
	}

	// Check child line
	var cEntry map[string]interface{}
	json.Unmarshal([]byte(lines[1]), &cEntry)
	if cEntry["req_id"] != "123" {
		t.Error("child log missing 'req_id'")
	}
}

func TestLogx_Concurrency(t *testing.T) {
	// FIX: Use thread-safe buffer
	buf := &syncBuffer{}
	root := sst.NewWithWriter(buf)
	ctx := context.Background()

	var wg sync.WaitGroup
	count := 50

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(id int) {
			defer wg.Done()
			// Create a child logger (reads parent fields)
			child := root.With("worker_id", id)
			// Log something (writes to shared buffer)
			child.Info(ctx, "working")
		}(i)
	}

	wg.Wait()

	// Verify we got 50 lines (if no panic/race occurred)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != count {
		t.Errorf("expected %d log lines, got %d", count, len(lines))
	}
}
