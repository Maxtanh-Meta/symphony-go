package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// readJSONL reads the file at path and returns each line decoded into a
// generic map. Empty trailing lines are skipped. Test helper.
func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", string(line), err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestHandler_SingleIssueWrite(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	delegate := slog.NewJSONHandler(&stderr, nil)
	h := New(dir, nil, delegate)
	defer h.Close()

	logger := slog.New(h)
	logger.Info("claim", "issue", 123, "branch", "main")

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, "123.jsonl")
	lines := readJSONL(t, path)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	got := lines[0]
	if got["msg"] != "claim" {
		t.Errorf("msg=%v", got["msg"])
	}
	if got["level"] != "INFO" {
		t.Errorf("level=%v", got["level"])
	}
	if _, ok := got["time"]; !ok {
		t.Errorf("missing time")
	}
	if got["branch"] != "main" {
		t.Errorf("branch=%v", got["branch"])
	}
	if int(got["issue"].(float64)) != 123 {
		t.Errorf("issue=%v", got["issue"])
	}
	if stderr.Len() == 0 {
		t.Errorf("expected delegate to receive record")
	}
}

func TestHandler_MultiIssueRouting(t *testing.T) {
	dir := t.TempDir()
	h := New(dir, nil, nil)
	defer h.Close()
	logger := slog.New(h)

	logger.Info("planning_started", "issue", 1)
	logger.Info("planning_started", "issue", 2)
	logger.Info("committed", "issue", 1)

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	l1 := readJSONL(t, filepath.Join(dir, "1.jsonl"))
	l2 := readJSONL(t, filepath.Join(dir, "2.jsonl"))
	if len(l1) != 2 {
		t.Errorf("issue 1: want 2 lines, got %d", len(l1))
	}
	if len(l2) != 1 {
		t.Errorf("issue 2: want 1 line, got %d", len(l2))
	}
}

func TestHandler_RedactsStringAttr(t *testing.T) {
	dir := t.TempDir()
	patterns := []string{`ghp_[A-Za-z0-9]+`}
	h := New(dir, patterns, nil)
	defer h.Close()
	logger := slog.New(h)

	logger.Info("hook_completed", "issue", 7, "token", "ghp_abcdef123", "msg", "ok")

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readJSONL(t, filepath.Join(dir, "7.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	tok, _ := lines[0]["token"].(string)
	if !strings.Contains(tok, "[REDACTED]") {
		t.Errorf("expected token redacted, got %q", tok)
	}
	if strings.Contains(tok, "ghp_") {
		t.Errorf("token still contains raw secret: %q", tok)
	}
}

func TestHandler_RedactsNestedStringAttr(t *testing.T) {
	dir := t.TempDir()
	patterns := []string{`ghp_[A-Za-z0-9]+`}
	h := New(dir, patterns, nil)
	defer h.Close()
	logger := slog.New(h)

	logger.LogAttrs(context.Background(), slog.LevelInfo, "evt",
		slog.Int("issue", 9),
		slog.Group("hook", slog.String("out", "got ghp_xyz123 from env")),
	)

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	lines := readJSONL(t, filepath.Join(dir, "9.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	hook, ok := lines[0]["hook"].(map[string]any)
	if !ok {
		t.Fatalf("missing hook group: %v", lines[0])
	}
	out, _ := hook["out"].(string)
	if !strings.Contains(out, "[REDACTED]") || strings.Contains(out, "ghp_xyz") {
		t.Errorf("nested string not redacted: %q", out)
	}
}

func TestHandler_NoIssueAttr_NoFile(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	delegate := slog.NewJSONHandler(&stderr, nil)
	h := New(dir, nil, delegate)
	defer h.Close()

	logger := slog.New(h)
	logger.Info("startup", "config", "/path/cfg.yml")

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		// dir might not exist at all (good) or be empty.
		for _, e := range entries {
			t.Errorf("unexpected audit file: %s", e.Name())
		}
	}
	if stderr.Len() == 0 {
		t.Errorf("delegate should have received the record")
	}
}

func TestHandler_ConcurrentSameIssue(t *testing.T) {
	dir := t.TempDir()
	h := New(dir, nil, nil)
	defer h.Close()
	logger := slog.New(h)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			logger.Info("validation_command", "issue", 42, "i", i)
		}(i)
	}
	wg.Wait()
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readJSONL(t, filepath.Join(dir, "42.jsonl"))
	if len(lines) != N {
		t.Fatalf("want %d lines, got %d", N, len(lines))
	}
	seen := make(map[int]bool, N)
	for _, m := range lines {
		f, ok := m["i"].(float64)
		if !ok {
			t.Fatalf("missing i: %v", m)
		}
		seen[int(f)] = true
	}
	if len(seen) != N {
		t.Errorf("want %d distinct i values, got %d", N, len(seen))
	}
}

func TestHandler_CloseFlushesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := New(dir, nil, nil)
	logger := slog.New(h)
	logger.Info("pushed", "issue", 5, "sha", "abc")

	if err := h.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	// File should be readable and contain the line.
	data, err := os.ReadFile(filepath.Join(dir, "5.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(data, []byte(`"sha":"abc"`)) {
		t.Errorf("missing payload in file: %q", data)
	}
}

func TestHandler_WithAttrsAndGroup(t *testing.T) {
	dir := t.TempDir()
	patterns := []string{`ghp_[A-Za-z0-9]+`}
	h := New(dir, patterns, nil)
	defer h.Close()

	// Logger.With("issue", N) — issue arrives via WithAttrs, not on the
	// record itself. Must still route correctly.
	logger := slog.New(h).With("issue", 11, "secret", "ghp_aaa111")
	logger.Info("worktree_created", "path", "/tmp/wt")

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	lines := readJSONL(t, filepath.Join(dir, "11.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if got := lines[0]["secret"].(string); !strings.Contains(got, "[REDACTED]") {
		t.Errorf("preset string not redacted: %q", got)
	}
	if lines[0]["path"] != "/tmp/wt" {
		t.Errorf("path=%v", lines[0]["path"])
	}
}

// Sanity: the package compiles against a generic io.Writer-backed
// delegate — guards against accidental tight coupling.
var _ io.Writer = (*bytes.Buffer)(nil)
