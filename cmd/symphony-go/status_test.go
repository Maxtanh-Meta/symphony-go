package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/logosc/symphony-go/internal/types"
)

func TestRenderJobs_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderJobs(&buf, nil); err != nil {
		t.Fatalf("renderJobs: %v", err)
	}
	if got := buf.String(); got != "no jobs\n" {
		t.Fatalf("empty render = %q, want %q", got, "no jobs\n")
	}
}

func TestRenderJobs_OrderingAndColumns(t *testing.T) {
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	jobs := []*types.Job{
		{
			IssueNumber:  42,
			Status:       types.StatusImplementing,
			Branch:       "feat/short-branch",
			PRNumber:     7,
			ApprovalPath: types.ApprovalPathRules,
			UpdatedAt:    t1,
		},
		{
			IssueNumber: 5,
			Status:      types.StatusPlanning,
			Branch:      "",
			UpdatedAt:   t1,
		},
	}
	var buf bytes.Buffer
	if err := renderJobs(&buf, jobs); err != nil {
		t.Fatalf("renderJobs: %v", err)
	}
	out := buf.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines:\n%s", len(lines), out)
	}

	header := lines[0]
	for _, col := range []string{"ISSUE", "STATUS", "BRANCH", "PR", "APPROVAL", "UPDATED"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing %q: %s", col, header)
		}
	}

	// Sorted ascending by IssueNumber: 5 first, then 42.
	if !strings.HasPrefix(strings.TrimSpace(lines[1]), "5") {
		t.Errorf("row1 should start with issue 5: %q", lines[1])
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[2]), "42") {
		t.Errorf("row2 should start with issue 42: %q", lines[2])
	}

	// PR rendering: "-" for issue 5, "#7" for issue 42.
	if !strings.Contains(lines[1], "-") {
		t.Errorf("issue 5 row missing dash for empty PR: %q", lines[1])
	}
	if !strings.Contains(lines[2], "#7") {
		t.Errorf("issue 42 row missing #7: %q", lines[2])
	}

	// Approval defaults to "-" when unset.
	if !strings.Contains(lines[1], "planning") {
		t.Errorf("issue 5 row missing status: %q", lines[1])
	}
	if !strings.Contains(lines[2], "rules") {
		t.Errorf("issue 42 row missing approval path: %q", lines[2])
	}

	// Updated rendered as RFC3339 (local). Just check the year-month-day prefix.
	wantDate := t1.Local().Format(time.RFC3339)
	if !strings.Contains(out, wantDate) {
		t.Errorf("output missing local RFC3339 timestamp %q:\n%s", wantDate, out)
	}
}

func TestRenderJobs_BranchTruncation(t *testing.T) {
	long := strings.Repeat("a", 60)
	jobs := []*types.Job{
		{
			IssueNumber: 1,
			Status:      types.StatusImplementing,
			Branch:      long,
			UpdatedAt:   time.Now(),
		},
	}
	var buf bytes.Buffer
	if err := renderJobs(&buf, jobs); err != nil {
		t.Fatalf("renderJobs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, long) {
		t.Errorf("expected long branch to be truncated, but full string present:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis in truncated branch:\n%s", out)
	}
	// Truncated form: 39 a's + ellipsis.
	want := strings.Repeat("a", 39) + "…"
	if !strings.Contains(out, want) {
		t.Errorf("expected truncated branch %q in output:\n%s", want, out)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, ""},
		{"abc", 10, "abc"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, "…"},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.max); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
