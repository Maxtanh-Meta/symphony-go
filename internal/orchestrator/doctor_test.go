package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/logosc/symphony-go/internal/config"
)

// TestDoctorMissingToken: a config with no token in env should fail.
func TestDoctorMissingToken(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.GitHub.TokenEnv = "DEFINITELY_NOT_SET_XYZ"
	t.Setenv("DEFINITELY_NOT_SET_XYZ", "")
	err := Doctor(context.Background(), h.cfg)
	if err == nil || !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_XYZ") {
		t.Fatalf("expected token-empty error, got %v", err)
	}
}

// TestDoctorAutoModeMissingCatchAll: warns when neither catch-all nor
// fallback is set.
func TestDoctorAutoModeMissingCatchAll(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "auto"
	h.cfg.Auto.Rules = []config.AutoRule{
		{IssueLabels: []string{"docs"}, MaxPlanFilesClaimed: 5},
	}
	h.cfg.Auto.FallbackOnNoRuleMatch = ""
	t.Setenv(h.cfg.GitHub.TokenEnv, "")
	err := Doctor(context.Background(), h.cfg)
	if err == nil || !strings.Contains(err.Error(), "fallback_on_no_rule_match") {
		t.Fatalf("expected catch-all/fallback error, got %v", err)
	}
}

// TestDoctorBaseBranchMissing: the test repo has no fictional branch.
func TestDoctorBaseBranchMissing(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Repo.BaseBranch = "branch-that-does-not-exist"
	t.Setenv(h.cfg.GitHub.TokenEnv, "")
	err := Doctor(context.Background(), h.cfg)
	if err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("expected base-branch error, got %v", err)
	}
}
