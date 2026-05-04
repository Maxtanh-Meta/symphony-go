package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/logosc/symphony-go/internal/github"
	"github.com/logosc/symphony-go/internal/types"
)

// TestReconcileCrashMidImplementation seeds a job pre-marked
// `implementing` with a stale label. After Reconcile, the issue should be
// blocked.
func TestReconcileCrashMidImplementation(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	o := h.newOrch(t, "x", false)

	// Seed an issue with the implementing label and a corresponding local job.
	iss := types.Issue{
		Number: 100, Title: "crashed", Description: "", State: "open",
		Labels: []string{h.cfg.Labels.Implementing},
	}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber:   100,
		Repo:          h.cfg.Repo.FullName,
		Status:        types.StatusImplementing,
		WorkspaceRoot: "/tmp/wt-100",
		RepoPath:      "/tmp/wt-100/repo",
		Branch:        "symphony/issue-100-crashed",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(100), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(100))
	}
	updated, _ := h.state.Load(100)
	if updated.Status != types.StatusBlocked {
		t.Fatalf("expected local blocked, got %q", updated.Status)
	}
}

// TestReconcileOrphanPlanning: issue carries planning label, no local job.
func TestReconcileOrphanPlanning(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 101, Title: "orphan", State: "open",
		Labels: []string{h.cfg.Labels.Planning}}
	h.gh.SeedIssue(iss, false)
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(101), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(101))
	}
}

// TestReconcileReadyLeavesAlone: row 1 — no local, ready label, leave alone.
func TestReconcileReadyLeavesAlone(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 102, Title: "fresh", State: "open",
		Labels: []string{h.cfg.Labels.Ready}}
	h.gh.SeedIssue(iss, false)
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(102), h.cfg.Labels.Ready) {
		t.Fatalf("expected ready unchanged, got %v", h.labelsFor(102))
	}
}

// TestReconcileRetryPlanning: row 7 — local + github both `planning`
// (interrupted mid-plan). Reconcile relabels back to `ready` and drops
// the local state so the dispatch loop retries fresh on the next tick.
// Reconciliation never starts an agent itself.
func TestReconcileRetryPlanning(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 104, Title: "in-flight planning", State: "open",
		Labels: []string{h.cfg.Labels.Planning}}
	h.gh.SeedIssue(iss, false)
	// Seed a prior plan comment so we can verify it gets edited in place.
	const originalPlan = "## Plan\n- step 1\n- step 2"
	planComment := h.gh.SeedComment(104, types.IssueComment{Body: originalPlan})
	job := &types.Job{
		IssueNumber:   104,
		Repo:          h.cfg.Repo.FullName,
		Status:        types.StatusPlanning,
		Branch:        "symphony/issue-104-in-flight-planning",
		PlanCommentID: planComment.ID,
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(104), h.cfg.Labels.Ready) {
		t.Fatalf("expected ready, got %v", h.labelsFor(104))
	}
	if findLabel(h.labelsFor(104), h.cfg.Labels.Planning) {
		t.Fatalf("expected planning label removed, got %v", h.labelsFor(104))
	}
	if _, err := h.state.Load(104); err == nil {
		t.Fatalf("expected local state deleted, got nil error")
	}
	// The prior plan comment should have been edited in place.
	got, ok := h.gh.GetComment(planComment.ID)
	if !ok {
		t.Fatalf("expected plan comment %d to still exist", planComment.ID)
	}
	if got.Body == originalPlan {
		t.Errorf("expected plan comment body edited, still original: %q", got.Body)
	}
	if !strings.Contains(got.Body, "superseded") {
		t.Errorf("expected edited plan comment to mention 'superseded', got: %q", got.Body)
	}
}

// TestReconcilePRReadyTerminal: row 5 / 15 — leave alone.
func TestReconcilePRReadyTerminal(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 103, Title: "done", State: "open",
		Labels: []string{h.cfg.Labels.PRReady}}
	h.gh.SeedIssue(iss, false)
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(103), h.cfg.Labels.PRReady) {
		t.Fatalf("expected pr-ready unchanged, got %v", h.labelsFor(103))
	}
}

// TestReconcileRow2_OrphanPlanningNoLocal: github planning label, no local
// state. Reconcile must replace label with `blocked` and post a comment.
func TestReconcileRow2_OrphanPlanningNoLocal(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 200, Title: "orphan plan", State: "open",
		Labels: []string{h.cfg.Labels.Planning}}
	h.gh.SeedIssue(iss, false)
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(200), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(200))
	}
	if findLabel(h.labelsFor(200), h.cfg.Labels.Planning) {
		t.Fatalf("planning label not removed, got %v", h.labelsFor(200))
	}
}

// TestReconcileRow3_OrphanAwaitingApprovalNoLocal: github
// awaiting-approval label, no local state. Reconcile must replace with
// blocked.
func TestReconcileRow3_OrphanAwaitingApprovalNoLocal(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 203, Title: "orphan", State: "open",
		Labels: []string{h.cfg.Labels.AwaitingApproval}}
	h.gh.SeedIssue(iss, false)
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(203), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(203))
	}
}

// TestReconcileRow8_PlanningLocalLabelDrift: local=planning but github
// label is something else (e.g. awaiting-approval). Mark local blocked,
// replace github label with blocked.
func TestReconcileRow8_PlanningLocalLabelDrift(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 208, Title: "drift plan", State: "open",
		Labels: []string{h.cfg.Labels.AwaitingApproval}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 208, Repo: h.cfg.Repo.FullName,
		Status: types.StatusPlanning, Branch: "symphony/issue-208",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(208), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(208))
	}
	got, _ := h.state.Load(208)
	if got.Status != types.StatusBlocked {
		t.Fatalf("expected local blocked, got %q", got.Status)
	}
}

// TestReconcileRow9_AwaitingApprovalResume: local + github both
// awaiting-approval. Reconcile leaves things alone (poller will resume).
func TestReconcileRow9_AwaitingApprovalResume(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 209, Title: "wait", State: "open",
		Labels: []string{h.cfg.Labels.AwaitingApproval}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 209, Repo: h.cfg.Repo.FullName,
		Status: types.StatusAwaitingApproval, Branch: "symphony/issue-209",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(209), h.cfg.Labels.AwaitingApproval) {
		t.Fatalf("expected awaiting-approval preserved, got %v", h.labelsFor(209))
	}
	got, _ := h.state.Load(209)
	if got.Status != types.StatusAwaitingApproval {
		t.Fatalf("expected local unchanged, got %q", got.Status)
	}
}

// TestReconcileRow10_AwaitingImplementingMidTransition: local=awaiting,
// github=implementing, with ApprovalCommentID set indicating an approve
// was already received. Reconcile must leave things alone (resume path).
func TestReconcileRow10_AwaitingImplementingMidTransition(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 210, Title: "mid", State: "open",
		Labels: []string{h.cfg.Labels.Implementing}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 210, Repo: h.cfg.Repo.FullName,
		Status: types.StatusAwaitingApproval, Branch: "symphony/issue-210",
		ApprovalCommentID: 4242,
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(210), h.cfg.Labels.Implementing) {
		t.Fatalf("expected implementing preserved, got %v", h.labelsFor(210))
	}
	got, _ := h.state.Load(210)
	if got.Status != types.StatusAwaitingApproval {
		t.Fatalf("expected local awaiting_approval preserved, got %q", got.Status)
	}
}

// TestReconcileRow10_AwaitingImplementingNoApproval covers the negative
// branch of row 10: local=awaiting, github=implementing, but neither
// ApprovalCommentID nor ReviewerDecision is set. Treat as drift and
// block.
func TestReconcileRow10_AwaitingImplementingNoApproval(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 211, Title: "mid no-approval", State: "open",
		Labels: []string{h.cfg.Labels.Implementing}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 211, Repo: h.cfg.Repo.FullName,
		Status: types.StatusAwaitingApproval, Branch: "symphony/issue-211",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(211), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(211))
	}
	got, _ := h.state.Load(211)
	if got.Status != types.StatusBlocked {
		t.Fatalf("expected local blocked, got %q", got.Status)
	}
}

// TestReconcileRow11_AwaitingApprovalLabelDrift: local=awaiting, github
// is something else entirely (e.g. ready). Block.
func TestReconcileRow11_AwaitingApprovalLabelDrift(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 212, Title: "drift wait", State: "open",
		Labels: []string{h.cfg.Labels.Ready}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 212, Repo: h.cfg.Repo.FullName,
		Status: types.StatusAwaitingApproval, Branch: "symphony/issue-212",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(212), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(212))
	}
	got, _ := h.state.Load(212)
	if got.Status != types.StatusBlocked {
		t.Fatalf("expected local blocked, got %q", got.Status)
	}
}

// TestReconcileRow13_PRReadyAfterImplCrash: local=implementing,
// github=pr-ready; a PR matching the branch already exists. Reconcile
// must adopt that PR (save pr_number) and mark the job pr_ready.
func TestReconcileRow13_PRReadyAfterImplCrash(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	branch := "symphony/issue-213-feature"
	iss := types.Issue{Number: 213, Title: "feature", State: "open",
		Labels: []string{h.cfg.Labels.PRReady}}
	h.gh.SeedIssue(iss, false)
	// Drive the PR creation through deps.GitHub (per brief) so we don't
	// touch internal/github directly in this package.
	pr, err := o.deps.GitHub.CreateDraftPR(context.Background(), github.CreatePRRequest{
		Title: "[agent] feature",
		Body:  "x",
		Head:  branch,
		Base:  h.cfg.Repo.BaseBranch,
		Draft: true,
	})
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	job := &types.Job{
		IssueNumber: 213, Repo: h.cfg.Repo.FullName,
		Status: types.StatusImplementing, Branch: branch,
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := h.state.Load(213)
	if got.Status != types.StatusPRReady {
		t.Fatalf("expected pr_ready, got %q", got.Status)
	}
	if got.PRNumber != pr.Number {
		t.Fatalf("expected PRNumber=%d, got %d", pr.Number, got.PRNumber)
	}
}

// TestReconcileRow13_PRReadyZeroMatches: local=implementing,
// github=pr-ready, but no PR matches the recorded branch. Block.
func TestReconcileRow13_PRReadyZeroMatches(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 214, Title: "no pr", State: "open",
		Labels: []string{h.cfg.Labels.PRReady}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 214, Repo: h.cfg.Repo.FullName,
		Status: types.StatusImplementing, Branch: "symphony/issue-214-no-pr",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(214), h.cfg.Labels.Blocked) {
		t.Fatalf("expected blocked, got %v", h.labelsFor(214))
	}
}

// TestReconcileRow14_ImplementingFailedLeave: local=implementing,
// github=failed. Crash during failure handling. Per SPEC §7 row 14,
// leave failed; do not retry; do not transition local Job.
func TestReconcileRow14_ImplementingFailedLeave(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 215, Title: "fail crash", State: "open",
		Labels: []string{h.cfg.Labels.Failed}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 215, Repo: h.cfg.Repo.FullName,
		Status: types.StatusImplementing, Branch: "symphony/issue-215",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(215), h.cfg.Labels.Failed) {
		t.Fatalf("expected failed preserved, got %v", h.labelsFor(215))
	}
	got, _ := h.state.Load(215)
	if got.Status != types.StatusImplementing {
		t.Fatalf("expected local implementing preserved, got %q", got.Status)
	}
}

// TestReconcileRow16_PRReadyManualRelabel: local=pr_ready, github label
// is something else (e.g. someone manually relabeled to ready). Leave
// local as is, do not touch github.
func TestReconcileRow16_PRReadyManualRelabel(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 216, Title: "relabeled", State: "open",
		Labels: []string{h.cfg.Labels.Ready}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 216, Repo: h.cfg.Repo.FullName,
		Status: types.StatusPRReady, Branch: "symphony/issue-216",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !findLabel(h.labelsFor(216), h.cfg.Labels.Ready) {
		t.Fatalf("expected ready unchanged on github, got %v", h.labelsFor(216))
	}
	got, _ := h.state.Load(216)
	if got.Status != types.StatusPRReady {
		t.Fatalf("expected local pr_ready preserved, got %q", got.Status)
	}
}

// TestReconcileRow17_NoSymphonyLabel: open issue with no symphony:* label
// at all and a local job. Mark local blocked; do not relabel github.
func TestReconcileRow17_NoSymphonyLabel(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 217, Title: "stripped", State: "open",
		Labels: []string{"bug"}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 217, Repo: h.cfg.Repo.FullName,
		Status: types.StatusPlanning, Branch: "symphony/issue-217",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := h.state.Load(217)
	if got.Status != types.StatusBlocked {
		t.Fatalf("expected local blocked, got %q", got.Status)
	}
	// github labels untouched.
	labels := h.labelsFor(217)
	if findLabel(labels, h.cfg.Labels.Blocked) {
		t.Fatalf("expected github untouched (no blocked added), got %v", labels)
	}
}

// TestReconcileRow18_ClosedNonTerminal: issue closed while local was
// non-terminal. Reconcile leaves workspace and labels; local file
// remains.
func TestReconcileRow18_ClosedNonTerminal(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 218, Title: "closed mid", State: "closed",
		Labels: []string{h.cfg.Labels.Planning}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 218, Repo: h.cfg.Repo.FullName,
		Status: types.StatusPlanning, Branch: "symphony/issue-218",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Labels untouched on github.
	if !findLabel(h.labelsFor(218), h.cfg.Labels.Planning) {
		t.Fatalf("expected planning label preserved on closed issue, got %v",
			h.labelsFor(218))
	}
	// Local job file still loadable.
	if _, err := h.state.Load(218); err != nil {
		t.Fatalf("expected local job to remain, got err %v", err)
	}
}

// TestReconcileRow19_ClosedTerminal: issue closed, local terminal.
// Reconcile must not error and leaves things alone.
func TestReconcileRow19_ClosedTerminal(t *testing.T) {
	h := newTestHarness(t)
	o := h.newOrch(t, "x", false)
	iss := types.Issue{Number: 219, Title: "closed done", State: "closed",
		Labels: []string{h.cfg.Labels.PRReady}}
	h.gh.SeedIssue(iss, false)
	job := &types.Job{
		IssueNumber: 219, Repo: h.cfg.Repo.FullName,
		Status: types.StatusPRReady, Branch: "symphony/issue-219",
	}
	if err := h.state.Save(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := o.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := h.state.Load(219)
	if got.Status != types.StatusPRReady {
		t.Fatalf("expected pr_ready preserved, got %q", got.Status)
	}
}
