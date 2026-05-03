package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/types"
)

func mkJob(n int, status types.JobStatus, root, repoPath, branch string) *types.Job {
	return &types.Job{
		IssueNumber:   n,
		Status:        status,
		WorkspaceRoot: root,
		RepoPath:      repoPath,
		Branch:        branch,
	}
}

func TestPlanClean_TerminalAndNonTerminal(t *testing.T) {
	jobs := []*types.Job{
		mkJob(1, types.StatusPRReady, "/ws/1", "/ws/1/repo", "symphony/issue-1-x"),
		mkJob(2, types.StatusFailed, "/ws/2", "/ws/2/repo", "symphony/issue-2-y"),
		mkJob(3, types.StatusBlocked, "/ws/3", "/ws/3/repo", "symphony/issue-3-z"),
		mkJob(4, types.StatusImplementing, "/ws/4", "/ws/4/repo", "symphony/issue-4-a"),
		mkJob(5, types.StatusPlanning, "/ws/5", "/ws/5/repo", "symphony/issue-5-b"),
	}
	plan := planClean(jobs, false)

	seen := map[int]bool{}
	for _, a := range plan {
		seen[a.IssueNumber] = true
	}
	for _, n := range []int{1, 2, 3} {
		if !seen[n] {
			t.Errorf("expected issue %d in plan", n)
		}
	}
	for _, n := range []int{4, 5} {
		if seen[n] {
			t.Errorf("non-terminal issue %d should not be in plan", n)
		}
	}
}

func TestPlanClean_OrderingPerJob(t *testing.T) {
	jobs := []*types.Job{
		mkJob(7, types.StatusPRReady, "/ws/7", "/ws/7/repo", "br-7"),
	}
	plan := planClean(jobs, false)
	if len(plan) != 3 {
		t.Fatalf("expected 3 actions (worktree, branch, state), got %d", len(plan))
	}
	wantKinds := []cleanActionKind{actionWorktreeRemove, actionBranchDelete, actionStateDelete}
	for i, k := range wantKinds {
		if plan[i].Kind != k {
			t.Errorf("plan[%d].Kind = %q, want %q", i, plan[i].Kind, k)
		}
	}
}

func TestPlanClean_ForceAddsOriginDelete(t *testing.T) {
	jobs := []*types.Job{
		mkJob(8, types.StatusPRReady, "/ws/8", "/ws/8/repo", "br-8"),
	}
	plan := planClean(jobs, true)

	var hasOrigin bool
	for _, a := range plan {
		if a.Kind == actionOriginDelete {
			hasOrigin = true
			if a.Branch != "br-8" {
				t.Errorf("origin delete branch=%q want br-8", a.Branch)
			}
		}
	}
	if !hasOrigin {
		t.Error("expected actionOriginDelete with --force")
	}

	// Without force: no origin delete.
	plan2 := planClean(jobs, false)
	for _, a := range plan2 {
		if a.Kind == actionOriginDelete {
			t.Error("origin delete present without force")
		}
	}
}

func TestPlanClean_NoBranchSkipsBranchActions(t *testing.T) {
	jobs := []*types.Job{
		mkJob(9, types.StatusFailed, "/ws/9", "/ws/9/repo", ""),
	}
	plan := planClean(jobs, true)
	for _, a := range plan {
		if a.Kind == actionBranchDelete || a.Kind == actionOriginDelete {
			t.Errorf("unexpected %s when Branch is empty", a.Kind)
		}
	}
}

func TestPlanClean_NoWorkspaceSkipsWorktree(t *testing.T) {
	jobs := []*types.Job{
		mkJob(10, types.StatusFailed, "", "", "br-10"),
	}
	plan := planClean(jobs, false)
	for _, a := range plan {
		if a.Kind == actionWorktreeRemove {
			t.Error("unexpected worktree action when WorkspaceRoot empty")
		}
	}
}

func TestRenderActions_DryRunPrefix(t *testing.T) {
	plan := []cleanAction{
		{Kind: actionWorktreeRemove, IssueNumber: 1, RepoPath: "/r", Root: "/root"},
		{Kind: actionBranchDelete, IssueNumber: 1, Branch: "br"},
		{Kind: actionOriginDelete, IssueNumber: 1, Branch: "br"},
		{Kind: actionStateDelete, IssueNumber: 1},
	}
	var buf bytes.Buffer
	renderActions(&buf, plan)
	out := buf.String()
	for _, want := range []string{
		"[dry-run] issue 1: git worktree remove --force /r",
		"[dry-run] issue 1: git branch -D br",
		"[dry-run] issue 1: git push origin --delete br",
		"[dry-run] issue 1: state.Delete",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing line: %q\n--got--\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "[dry-run]") {
			t.Errorf("line missing [dry-run] prefix: %q", line)
		}
	}
}

func TestExecuteClean_NonexistentWorktreeStillCleansState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := exec.Command("git", "init", "-q", repoDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// initial commit so branch ops work
	for _, args := range [][]string{
		{"-C", repoDir, "config", "user.email", "t@t"},
		{"-C", repoDir, "config", "user.name", "t"},
		{"-C", repoDir, "config", "commit.gpgsign", "false"},
		{"-C", repoDir, "commit", "--allow-empty", "-m", "init", "-q"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	stateRoot := filepath.Join(dir, ".symphony-go", "state")
	store, err := state.NewStore(stateRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	job := mkJob(42, types.StatusPRReady,
		filepath.Join(dir, "ws", "issue-42-x"),
		filepath.Join(dir, "ws", "issue-42-x", "repo"),
		"") // no branch -> only worktree + state actions
	if err := store.Save(job); err != nil {
		t.Fatalf("Save: %v", err)
	}

	plan := planClean([]*types.Job{job}, false)

	var buf bytes.Buffer
	rc := executeClean(&buf, plan, repoDir, store)
	if rc != 0 {
		t.Fatalf("executeClean rc=%d, out=%s", rc, buf.String())
	}
	if _, err := store.Load(42); err == nil {
		t.Error("state was not deleted")
	}
	if !strings.Contains(buf.String(), "0 branches deleted") {
		t.Errorf("summary missing, got: %s", buf.String())
	}
}

func TestExecuteClean_RealBranchDelete(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repoDir},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			// -b may be unsupported on old git; fall back
			if err := exec.Command("git", "init", "-q", repoDir).Run(); err != nil {
				t.Fatalf("git init: %v", err)
			}
		}
	}
	for _, args := range [][]string{
		{"-C", repoDir, "config", "user.email", "t@t"},
		{"-C", repoDir, "config", "user.name", "t"},
		{"-C", repoDir, "config", "commit.gpgsign", "false"},
		{"-C", repoDir, "commit", "--allow-empty", "-m", "init", "-q"},
		{"-C", repoDir, "branch", "to-delete"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	stateRoot := filepath.Join(dir, ".symphony-go", "state")
	store, err := state.NewStore(stateRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	job := mkJob(7, types.StatusFailed, "", "", "to-delete")
	if err := store.Save(job); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plan := planClean([]*types.Job{job}, false)

	var buf bytes.Buffer
	rc := executeClean(&buf, plan, repoDir, store)
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, buf.String())
	}
	out, err := exec.Command("git", "-C", repoDir, "branch", "--list", "to-delete").Output()
	if err != nil {
		t.Fatalf("branch --list: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch still exists: %q", out)
	}
	if _, err := store.Load(7); err == nil {
		t.Error("state not cleaned after branch delete")
	}
	if !strings.Contains(buf.String(), "1 branches deleted") {
		t.Errorf("summary wrong: %s", buf.String())
	}
}

func TestExecuteClean_BranchCheckedOutSkippedWithReason(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := exec.Command("git", "init", "-q", repoDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, args := range [][]string{
		{"-C", repoDir, "config", "user.email", "t@t"},
		{"-C", repoDir, "config", "user.name", "t"},
		{"-C", repoDir, "config", "commit.gpgsign", "false"},
		{"-C", repoDir, "commit", "--allow-empty", "-m", "init", "-q"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	// Determine the current branch name (could be "main" or "master").
	cur, err := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	curBranch := strings.TrimSpace(string(cur))

	stateRoot := filepath.Join(dir, ".symphony-go", "state")
	store, err := state.NewStore(stateRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	job := mkJob(11, types.StatusBlocked, "", "", curBranch)
	if err := store.Save(job); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plan := planClean([]*types.Job{job}, false)

	var buf bytes.Buffer
	executeClean(&buf, plan, repoDir, store)
	got := buf.String()
	if !strings.Contains(got, "skip branch delete") {
		t.Errorf("expected skip line, got: %s", got)
	}
	if !strings.Contains(got, "1 skipped") {
		t.Errorf("expected '1 skipped' summary, got: %s", got)
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"hello":           "hello",
		"hello\nworld":    "hello",
		"\n\nfoo\nbar":    "foo",
		"trailing  \n":    "trailing",
		"  leading-keep ": "  leading-keep",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q want %q", in, got, want)
		}
	}
}
