// Package orchestrator wires together M1–M3 packages (config, github,
// state, workspace, exec, runner, approval) into the per-issue state
// machine and outer poll loop described in SPEC §3, §7, §10, §11.
//
// External collaborators are injected via Deps so tests can swap fakes.
// The orchestrator never spawns goroutines beyond its single Run loop and
// processes one issue at a time (max_concurrency=1 in MVP).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/logosc/symphony-go/internal/approval"
	"github.com/logosc/symphony-go/internal/config"
	"github.com/logosc/symphony-go/internal/github"
	"github.com/logosc/symphony-go/internal/runner"
	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/workspace"
)

// PushFunc pushes branch to origin from repoPath, authenticating with
// token. Tests inject a fake; production code uses DefaultPushFunc.
type PushFunc func(ctx context.Context, repoPath, branch, token string) error

// Deps carries every external collaborator the orchestrator needs.
//
// All fields except Reviewer (used only when approval.mode == "auto" with
// reviewer_required rules) are required. Run will fail fast at New() if a
// required field is nil.
type Deps struct {
	// Config is the parsed, validated config.yml.
	Config *config.Config
	// GitHub is the API client (real or InMemoryFake).
	GitHub github.Client
	// State is the on-disk job-state store.
	State *state.Store
	// WorkspaceMgr creates per-issue worktrees.
	WorkspaceMgr *workspace.Manager
	// AgentRunner runs the planner / implementer agent subprocess.
	AgentRunner runner.AgentRunner
	// Reviewer drives the auto-mode reviewer. May be nil; required only
	// when approval.mode == "auto" and at least one rule sets
	// reviewer_required: true.
	Reviewer *approval.Reviewer
	// PromptTemplate is the pre-loaded body of WORKFLOW.md.
	PromptTemplate string
	// NowFunc returns the current time. Defaults to time.Now.
	NowFunc func() time.Time
	// PushFunc pushes a branch to origin. Defaults to DefaultPushFunc.
	PushFunc PushFunc
	// GitHubToken is the resolved token value (looked up from
	// cfg.GitHub.TokenEnv at startup).
	GitHubToken string
	// Logger is used for all audit/structured logs. Defaults to slog.Default().
	Logger *slog.Logger
	// WorkspaceRoot overrides where per-issue worktrees live. Defaults to
	// "<repo.local_path>/.symphony-go/wt".
	WorkspaceRoot string
	// SelfUsername is the GitHub login this orchestrator instance posts
	// comments as (e.g. the App's bot login). When non-empty, comments
	// authored by this user are ignored by approval polling, even if not
	// listed in cfg.Approval.IgnoredUsers. Helps protect against
	// prompt-injected `/symphony approve` comments echoed by the bot
	// itself when running as a GitHub App.
	SelfUsername string
}

// Orchestrator is the M4 entry point. Construct via New.
type Orchestrator struct {
	deps Deps
	// running holds the issue numbers currently being processed in this
	// process (always 0 or 1 in the MVP).
	running map[int]struct{}
}

// New validates deps and returns an Orchestrator ready to Run.
func New(deps Deps) (*Orchestrator, error) {
	if deps.Config == nil {
		return nil, errors.New("orchestrator: Deps.Config is required")
	}
	if deps.GitHub == nil {
		return nil, errors.New("orchestrator: Deps.GitHub is required")
	}
	if deps.State == nil {
		return nil, errors.New("orchestrator: Deps.State is required")
	}
	if deps.WorkspaceMgr == nil {
		return nil, errors.New("orchestrator: Deps.WorkspaceMgr is required")
	}
	if deps.AgentRunner == nil {
		return nil, errors.New("orchestrator: Deps.AgentRunner is required")
	}
	if deps.PromptTemplate == "" {
		return nil, errors.New("orchestrator: Deps.PromptTemplate is required")
	}
	if deps.NowFunc == nil {
		deps.NowFunc = time.Now
	}
	if deps.PushFunc == nil {
		deps.PushFunc = DefaultPushFunc
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.WorkspaceRoot == "" {
		deps.WorkspaceRoot = fmt.Sprintf("%s/.symphony-go/wt", deps.Config.Repo.LocalPath)
	}
	return &Orchestrator{
		deps:    deps,
		running: make(map[int]struct{}),
	}, nil
}
