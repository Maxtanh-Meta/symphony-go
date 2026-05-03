// Command symphony-go is a local-first orchestrator that drives Codex or
// Claude Code on GitHub issues. See SPEC.md for the design.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/logosc/symphony-go/internal/approval"
	"github.com/logosc/symphony-go/internal/audit"
	"github.com/logosc/symphony-go/internal/config"
	"github.com/logosc/symphony-go/internal/github"
	"github.com/logosc/symphony-go/internal/orchestrator"
	"github.com/logosc/symphony-go/internal/runner"
	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/types"
	"github.com/logosc/symphony-go/internal/workspace"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "run":
		os.Exit(runCommand(args))
	case "doctor":
		os.Exit(doctorCommand(args))
	case "status":
		os.Exit(statusCommand(args))
	case "clean":
		os.Exit(cleanCommand(args))
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `symphony-go — drives Codex/Claude on GitHub issues

usage:
  symphony-go run    [--once] --config <path>
  symphony-go doctor          --config <path>
  symphony-go status          --config <path>
  symphony-go clean           [--config <path>] [--dry-run] [--force]

If --config is omitted, the following are searched in order:
  $SYMPHONY_GO_CONFIG
  $XDG_CONFIG_HOME/symphony-go/config.yml
  ~/.symphony-go/config.yml`)
}

func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	once := fs.Bool("once", false, "run a single dispatch cycle and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		slog.Error("config not found", "err", err)
		return 2
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		slog.Error("config load", "path", resolved, "err", err)
		return 2
	}

	// Integrity guard: enforce config-not-under-repo and seed the SHA-256
	// baseline. The orchestrator may re-check on each tick.
	if _, err := config.NewIntegrityGuard(resolved, cfg.Repo.LocalPath); err != nil {
		slog.Error("config integrity guard", "err", err)
		return 2
	}

	// Install the per-issue audit log writer (SPEC §13). The audit handler
	// fans out to the existing stderr JSON handler and additionally appends
	// a redacted JSON line to <repo>/.symphony-go/audit/{issue}.jsonl
	// whenever a record carries an "issue" or "issue_number" int attr.
	auditDir := filepath.Join(cfg.Repo.LocalPath, ".symphony-go", "audit")
	stderrDelegate := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	auditHandler := audit.New(auditDir, cfg.Audit.RedactPatterns, stderrDelegate)
	defer func() { _ = auditHandler.Close() }()
	slog.SetDefault(slog.New(auditHandler))

	token := os.Getenv(cfg.GitHub.TokenEnv)
	if token == "" {
		slog.Error("github token env var is empty", "env", cfg.GitHub.TokenEnv)
		return 2
	}

	wfPath := filepath.Join(cfg.Repo.LocalPath, cfg.Repo.WorkflowFile)
	wf, err := config.LoadWorkflow(wfPath)
	if err != nil {
		slog.Error("workflow load", "path", wfPath, "err", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	storeRoot := filepath.Join(cfg.Repo.LocalPath, ".symphony-go", "state")
	store, err := state.NewStore(storeRoot)
	if err != nil {
		slog.Error("state init", "root", storeRoot, "err", err)
		return 2
	}
	release, err := store.AcquireLock()
	if err != nil {
		slog.Error("lock contention; another symphony-go appears to be running", "err", err)
		return 2
	}
	defer func() { _ = release() }()

	gh, err := github.NewClient(ctx, token, cfg.Repo.FullName)
	if err != nil {
		slog.Error("github client", "err", err)
		return 2
	}

	wsMgr := workspace.NewManager(cfg.Repo.LocalPath)

	agentRunner, err := buildRunner(cfg.Agent.Provider, cfg.Agent, cfg)
	if err != nil {
		slog.Error("agent runner", "err", err)
		return 2
	}

	var reviewer *approval.Reviewer
	if string(cfg.Approval.Mode) == string(types.ApprovalAuto) && anyRuleNeedsReviewer(cfg.Auto.Rules) {
		revAgentCfg := config.AgentConfig{
			Provider:       cfg.Auto.Reviewer.Provider,
			Model:          cfg.Auto.Reviewer.Model,
			TimeoutSeconds: cfg.Auto.Reviewer.TimeoutSeconds,
		}
		revRunner, err := buildRunner(cfg.Auto.Reviewer.Provider, revAgentCfg, cfg)
		if err != nil {
			slog.Error("reviewer runner", "err", err)
			return 2
		}
		reviewer = approval.NewReviewer(revRunner, cfg.Auto.Reviewer)
	}

	orch, err := orchestrator.New(orchestrator.Deps{
		Config:         cfg,
		GitHub:         gh,
		State:          store,
		WorkspaceMgr:   wsMgr,
		AgentRunner:    agentRunner,
		Reviewer:       reviewer,
		PromptTemplate: wf,
		GitHubToken:    token,
	})
	if err != nil {
		slog.Error("orchestrator new", "err", err)
		return 2
	}

	slog.Info("symphony-go starting", "config", resolved, "once", *once, "repo", cfg.Repo.FullName)
	if err := orch.Run(ctx, *once); err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("shutdown")
			return 0
		}
		slog.Error("run", "err", err)
		return 1
	}
	return 0
}

func doctorCommand(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		slog.Error("config not found", "err", err)
		return 2
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		return 1
	}
	if _, err := config.NewIntegrityGuard(resolved, cfg.Repo.LocalPath); err != nil {
		fmt.Fprintf(os.Stderr, "config integrity: %v\n", err)
		return 1
	}
	if err := orchestrator.Doctor(context.Background(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "ok")
	return 0
}

func buildRunner(provider string, agentCfg config.AgentConfig, cfg *config.Config) (runner.AgentRunner, error) {
	switch provider {
	case "claude":
		return runner.NewClaudeRunner(agentCfg, cfg.Claude, cfg.Env, cfg.Audit), nil
	case "codex":
		return runner.NewCodexRunner(agentCfg, cfg.Codex, cfg.Env, cfg.Audit), nil
	default:
		return nil, fmt.Errorf("unknown agent provider %q (want claude|codex)", provider)
	}
}

func anyRuleNeedsReviewer(rules []config.AutoRule) bool {
	for _, r := range rules {
		if r.ReviewerRequired {
			return true
		}
	}
	return false
}

func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := os.Getenv("SYMPHONY_GO_CONFIG"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v + "/symphony-go/config.yml", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no --config and no fallback: %w", err)
	}
	return home + "/.symphony-go/config.yml", nil
}
