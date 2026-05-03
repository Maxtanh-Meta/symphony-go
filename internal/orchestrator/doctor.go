package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	gh "github.com/logosc/symphony-go/internal/github"

	"github.com/logosc/symphony-go/internal/config"
	"github.com/logosc/symphony-go/internal/types"
)

// Doctor runs the SPEC §12 checks against cfg and the live environment.
// Returns a multi-error (errors.Join) on any failure, nil on success.
//
// Doctor is intentionally permissive: it does not require the
// orchestrator to already be wired up. It expects cfg to be already
// validated by config.Validate (Load does this for you).
func Doctor(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("doctor: nil config")
	}
	var errs []error

	// 1, 2 happen at config load + integrity-guard construction time.
	// Re-run the path-inside-repo check here so doctor catches it without
	// requiring the caller to have built a guard first.
	absCfg, _ := filepath.Abs(os.Getenv("SYMPHONY_GO_CONFIG"))
	absRepo, _ := filepath.Abs(cfg.Repo.LocalPath)
	if absCfg != "" && absRepo != "" {
		if pathInside(absCfg, absRepo) {
			errs = append(errs, fmt.Errorf("doctor: SYMPHONY_GO_CONFIG %q is under repo.local_path %q", absCfg, absRepo))
		}
	}

	// 3: workflow file exists & renders.
	wfPath := filepath.Join(cfg.Repo.LocalPath, cfg.Repo.WorkflowFile)
	if body, err := config.LoadWorkflow(wfPath); err != nil {
		errs = append(errs, fmt.Errorf("doctor: load workflow: %w", err))
	} else if _, err := config.RenderPrompt(body, types.Issue{Number: 0}, 0); err != nil {
		errs = append(errs, fmt.Errorf("doctor: render workflow: %w", err))
	}

	// 4: GITHUB_TOKEN env var set.
	token := os.Getenv(cfg.GitHub.TokenEnv)
	if token == "" {
		errs = append(errs, fmt.Errorf("doctor: env %s is empty", cfg.GitHub.TokenEnv))
	}

	// 5, 6, 7: live GitHub probes (only when we have a token).
	if token != "" {
		client, err := gh.NewClient(ctx, token, cfg.Repo.FullName)
		if err != nil {
			errs = append(errs, fmt.Errorf("doctor: github client: %w", err))
		} else {
			// Cheap probe: list ready issues. Confirms repo + label existence.
			if _, err := client.ListReadyIssues(ctx, cfg.Labels.Ready); err != nil {
				errs = append(errs, fmt.Errorf("doctor: list ready issues: %w", err))
			}
		}
	}

	// 8: git, agent in PATH.
	if _, err := exec.LookPath("git"); err != nil {
		errs = append(errs, fmt.Errorf("doctor: git not in PATH: %w", err))
	}
	if cfg.Agent.Provider != "" {
		if _, err := exec.LookPath(cfg.Agent.Provider); err != nil {
			errs = append(errs, fmt.Errorf("doctor: agent %q not in PATH: %w", cfg.Agent.Provider, err))
		}
	}

	// 9, 10: local repo + base branch.
	gitDir := filepath.Join(cfg.Repo.LocalPath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		errs = append(errs, fmt.Errorf("doctor: %s is not a git repo: %w", cfg.Repo.LocalPath, err))
	} else {
		// origin remote.
		out, err := exec.CommandContext(ctx, "git", "-C", cfg.Repo.LocalPath, "remote", "get-url", "origin").CombinedOutput()
		if err != nil {
			errs = append(errs, fmt.Errorf("doctor: origin remote: %w (%s)", err, out))
		}
		// base branch local + remote.
		if err := exec.CommandContext(ctx, "git", "-C", cfg.Repo.LocalPath,
			"show-ref", "--verify", "--quiet", "refs/heads/"+cfg.Repo.BaseBranch).Run(); err != nil {
			errs = append(errs, fmt.Errorf("doctor: base branch %s missing locally", cfg.Repo.BaseBranch))
		}
	}

	// 11: workspace root writable.
	wsRoot := filepath.Join(cfg.Repo.LocalPath, ".symphony-go", "wt")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		errs = append(errs, fmt.Errorf("doctor: workspace root not writable: %w", err))
	}

	// 12: auto-mode preconditions.
	if cfg.Approval.Mode == "auto" {
		hasCatchAll := false
		needReviewer := false
		for _, r := range cfg.Auto.Rules {
			if len(r.IssueLabels) == 0 {
				hasCatchAll = true
			}
			if r.ReviewerRequired {
				needReviewer = true
			}
		}
		if !hasCatchAll && cfg.Auto.FallbackOnNoRuleMatch == "" {
			errs = append(errs, errors.New("doctor: auto.rules has no catch-all and fallback_on_no_rule_match is empty"))
		}
		if needReviewer && cfg.Auto.Reviewer.Provider != "" {
			if _, err := exec.LookPath(cfg.Auto.Reviewer.Provider); err != nil {
				errs = append(errs, fmt.Errorf("doctor: reviewer %q not in PATH: %w", cfg.Auto.Reviewer.Provider, err))
			}
		}
		if cfg.Auto.Reviewer.Provider != "" && cfg.Auto.Reviewer.Provider == cfg.Agent.Provider {
			slog.Warn("doctor: auto.reviewer.provider equals agent.provider; consider using a different provider for defense in depth")
		}
	}

	return errors.Join(errs...)
}

// pathInside is local to avoid an import cycle with config; it mirrors
// config.pathInside.
func pathInside(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(os.PathSeparator)
	p := parent
	if len(p) > 0 && p[len(p)-1:] != sep {
		p += sep
	}
	return len(child) >= len(p) && child[:len(p)] == p
}
