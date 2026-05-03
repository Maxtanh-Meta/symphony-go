package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/logosc/symphony-go/internal/types"
)

// installAwaitingJob seeds an issue at the awaiting-approval label and
// persists a corresponding local job. Returns the seeded issue number.
func installAwaitingJob(t *testing.T, h *testHarness, num int) {
	t.Helper()
	iss := types.Issue{
		Number: num, Title: "feature", Description: "", State: "open",
		Labels: []string{h.cfg.Labels.AwaitingApproval},
	}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber:   num,
		Repo:          h.cfg.Repo.FullName,
		Status:        types.StatusAwaitingApproval,
		WorkspaceRoot: t.TempDir(),
		RepoPath:      h.repo,
		Branch:        "symphony/issue-feature",
		PlanText:      canonicalPlan([]string{"a.txt"}),
		// UpdatedAt left zero-valued so ListIssueComments returns all comments.
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestApprovalBotIgnored_HappyPathWriter is the regression baseline: in
// gated mode, a comment from a writer user advances the job out of
// awaiting-approval. (Mirrors TestGatedHappyPath but starts from a
// pre-seeded awaiting state and only validates that PollApprovals fires
// the transition.)
func TestApprovalBotIgnored_HappyPathWriter(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "gated"
	o := h.newOrch(t, "x", false)

	implWriter(h.runner, []string{"a.txt"})

	installAwaitingJob(t, h, 200)
	h.gh.SetCollaboratorPermission("alice", "write")
	h.gh.SeedComment(200, types.IssueComment{
		User: "alice", Body: "/symphony approve", CreatedAt: time.Now().UTC(),
	})

	if err := o.PollApprovals(context.Background()); err != nil {
		t.Fatalf("PollApprovals: %v", err)
	}
	job, err := h.state.Load(200)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if job.Status == types.StatusAwaitingApproval {
		t.Fatalf("expected job to advance from awaiting_approval, got %q", job.Status)
	}
}

// TestApprovalBotIgnored_BotInIgnoredUsers verifies that a `/symphony
// approve` posted by a user listed in approval.ignored_users is skipped
// even if that user has write permission.
func TestApprovalBotIgnored_BotInIgnoredUsers(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "gated"
	h.cfg.Approval.IgnoredUsers = []string{"symphony-go[bot]"}
	o := h.newOrch(t, "x", false)

	installAwaitingJob(t, h, 201)
	// Even with elevated permission, the ignore-list match must short-circuit
	// before the permission lookup.
	h.gh.SetCollaboratorPermission("symphony-go[bot]", "admin")
	h.gh.SeedComment(201, types.IssueComment{
		User: "symphony-go[bot]", Body: "/symphony approve", CreatedAt: time.Now().UTC(),
	})

	if err := o.PollApprovals(context.Background()); err != nil {
		t.Fatalf("PollApprovals: %v", err)
	}
	job, err := h.state.Load(201)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if job.Status != types.StatusAwaitingApproval {
		t.Fatalf("expected job to remain awaiting_approval, got %q", job.Status)
	}
}

// TestApprovalBotIgnored_NonBotReadUser is the existing-logic regression:
// a non-bot commenter with only `read` permission is rejected by the
// permission check (gets a -1 reaction) and the job stays awaiting.
func TestApprovalBotIgnored_NonBotReadUser(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "gated"
	o := h.newOrch(t, "x", false)

	installAwaitingJob(t, h, 202)
	h.gh.SetCollaboratorPermission("eve", "read")
	c := h.gh.SeedComment(202, types.IssueComment{
		User: "eve", Body: "/symphony approve", CreatedAt: time.Now().UTC(),
	})

	if err := o.PollApprovals(context.Background()); err != nil {
		t.Fatalf("PollApprovals: %v", err)
	}
	job, err := h.state.Load(202)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if job.Status != types.StatusAwaitingApproval {
		t.Fatalf("expected awaiting_approval, got %q", job.Status)
	}
	rxs := h.gh.Reactions(c.ID)
	if len(rxs) != 1 || rxs[0] != "-1" {
		t.Fatalf("expected -1 reaction, got %v", rxs)
	}
}

// TestApprovalBotIgnored_SelfUsername verifies Deps.SelfUsername filters
// the bot's own comments even when not listed in IgnoredUsers.
func TestApprovalBotIgnored_SelfUsername(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "gated"
	// Empty IgnoredUsers explicitly so the only filter is SelfUsername.
	h.cfg.Approval.IgnoredUsers = []string{}

	deps := Deps{
		Config:         h.cfg,
		GitHub:         h.gh,
		State:          h.state,
		WorkspaceMgr:   h.mgr,
		AgentRunner:    h.runner,
		PromptTemplate: "x",
		PushFunc:       h.pushToBare,
		WorkspaceRoot:  h.wsRoot,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		SelfUsername:   "symphony-go[bot]",
	}
	o, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	installAwaitingJob(t, h, 203)
	h.gh.SetCollaboratorPermission("symphony-go[bot]", "admin")
	h.gh.SeedComment(203, types.IssueComment{
		User: "symphony-go[bot]", Body: "/symphony approve", CreatedAt: time.Now().UTC(),
	})

	if err := o.PollApprovals(context.Background()); err != nil {
		t.Fatalf("PollApprovals: %v", err)
	}
	job, err := h.state.Load(203)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if job.Status != types.StatusAwaitingApproval {
		t.Fatalf("expected job to remain awaiting_approval, got %q", job.Status)
	}
}
