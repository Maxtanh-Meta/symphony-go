package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/logosc/symphony-go/internal/config"
	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/types"
)

// cleanActionKind enumerates the distinct kinds of cleanup steps that
// planClean may emit for a single terminal job.
type cleanActionKind string

const (
	// actionWorktreeRemove runs `git worktree remove --force <RepoPath>` and
	// then `os.RemoveAll(WorkspaceRoot)`.
	actionWorktreeRemove cleanActionKind = "worktree_remove"
	// actionBranchDelete runs `git branch -D <Branch>` locally.
	actionBranchDelete cleanActionKind = "branch_delete"
	// actionOriginDelete runs `git push origin --delete <Branch>`. Emitted
	// only when --force is set.
	actionOriginDelete cleanActionKind = "origin_delete"
	// actionStateDelete deletes the on-disk job state file. Always emitted
	// last for a job.
	actionStateDelete cleanActionKind = "state_delete"
)

// cleanAction is one planned step against one Job. Pure data; no I/O.
type cleanAction struct {
	Kind        cleanActionKind
	IssueNumber int
	Branch      string
	RepoPath    string // git worktree leaf path (Job.RepoPath)
	Root        string // workspace root (Job.WorkspaceRoot)
}

// isTerminal reports whether status is one of the terminal job statuses
// that `clean` is allowed to operate on.
func isTerminal(s types.JobStatus) bool {
	switch s {
	case types.StatusPRReady, types.StatusFailed, types.StatusBlocked:
		return true
	}
	return false
}

// planClean derives the ordered list of cleanup actions for the supplied
// jobs. Non-terminal jobs are skipped. When force is true, an
// actionOriginDelete is emitted for any job with a non-empty Branch.
func planClean(jobs []*types.Job, force bool) []cleanAction {
	var out []cleanAction
	for _, j := range jobs {
		if j == nil || !isTerminal(j.Status) {
			continue
		}
		if j.WorkspaceRoot != "" {
			out = append(out, cleanAction{
				Kind:        actionWorktreeRemove,
				IssueNumber: j.IssueNumber,
				RepoPath:    j.RepoPath,
				Root:        j.WorkspaceRoot,
			})
		}
		if j.Branch != "" {
			out = append(out, cleanAction{
				Kind:        actionBranchDelete,
				IssueNumber: j.IssueNumber,
				Branch:      j.Branch,
			})
			if force {
				out = append(out, cleanAction{
					Kind:        actionOriginDelete,
					IssueNumber: j.IssueNumber,
					Branch:      j.Branch,
				})
			}
		}
		out = append(out, cleanAction{
			Kind:        actionStateDelete,
			IssueNumber: j.IssueNumber,
		})
	}
	return out
}

// renderActions writes a one-line dry-run summary for each action to w.
// Used by `--dry-run` mode and tests.
func renderActions(w io.Writer, actions []cleanAction) {
	for _, a := range actions {
		switch a.Kind {
		case actionWorktreeRemove:
			fmt.Fprintf(w, "[dry-run] issue %d: git worktree remove --force %s && rm -rf %s\n",
				a.IssueNumber, a.RepoPath, a.Root)
		case actionBranchDelete:
			fmt.Fprintf(w, "[dry-run] issue %d: git branch -D %s\n", a.IssueNumber, a.Branch)
		case actionOriginDelete:
			fmt.Fprintf(w, "[dry-run] issue %d: git push origin --delete %s\n", a.IssueNumber, a.Branch)
		case actionStateDelete:
			fmt.Fprintf(w, "[dry-run] issue %d: state.Delete\n", a.IssueNumber)
		}
	}
}

// cleanCommand implements `symphony-go clean`. It removes worktrees,
// branches, and on-disk job state for every job in terminal status
// (pr_ready, failed, blocked).
//
// In dry-run mode (the default) it prints what would happen and touches
// nothing. With --dry-run=false it actually performs the operations and,
// if --force is also supplied, additionally deletes the branch on origin.
func cleanCommand(args []string) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	dryRun := fs.Bool("dry-run", true, "print actions without executing (default true)")
	force := fs.Bool("force", false, "also delete remote branch on origin (only with --dry-run=false)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config not found: %v\n", err)
		return 2
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		return 2
	}

	storeRoot := filepath.Join(cfg.Repo.LocalPath, ".symphony-go", "state")
	store, err := state.NewStore(storeRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state init: %v\n", err)
		return 2
	}
	release, err := store.AcquireLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lock contention: %v\n", err)
		return 2
	}
	defer func() { _ = release() }()

	jobs, err := store.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "state list: %v\n", err)
		return 1
	}

	actions := planClean(jobs, *force && !*dryRun)

	if *dryRun {
		renderActions(os.Stdout, actions)
		nW, nB := 0, 0
		for _, a := range actions {
			switch a.Kind {
			case actionWorktreeRemove:
				nW++
			case actionBranchDelete:
				nB++
			}
		}
		fmt.Fprintf(os.Stdout, "%d worktrees removed, %d branches deleted, 0 skipped\n", nW, nB)
		return 0
	}

	return executeClean(os.Stdout, actions, cfg.Repo.LocalPath, store)
}

// executeClean runs the planned actions for real against the given repo
// and store. It returns 0 on success even when individual best-effort git
// operations fail; failures are reported as skip-with-reason lines and
// counted in the summary.
func executeClean(w io.Writer, actions []cleanAction, localRepoPath string, store *state.Store) int {
	worktreesRemoved := 0
	branchesDeleted := 0
	skipped := 0

	// Track which issues had a failed prerequisite so we suppress the
	// state.Delete for them.
	stateBlocked := map[int]bool{}

	for _, a := range actions {
		switch a.Kind {
		case actionWorktreeRemove:
			// Best-effort: ignore exit error from `worktree remove` — the
			// directory may already be gone or never have existed.
			_ = runGit(localRepoPath, "worktree", "remove", "--force", a.RepoPath)
			if _, err := os.Stat(a.Root); err == nil {
				if err := os.RemoveAll(a.Root); err != nil {
					fmt.Fprintf(w, "issue %d: skip worktree remove: %v\n", a.IssueNumber, err)
					skipped++
					stateBlocked[a.IssueNumber] = true
					continue
				}
			}
			fmt.Fprintf(w, "issue %d: removed worktree %s\n", a.IssueNumber, a.Root)
			worktreesRemoved++

		case actionBranchDelete:
			out, err := runGitOut(localRepoPath, "branch", "-D", a.Branch)
			if err != nil {
				reason := firstLine(out)
				if reason == "" {
					reason = err.Error()
				}
				fmt.Fprintf(w, "issue %d: skip branch delete %s: %s\n", a.IssueNumber, a.Branch, reason)
				skipped++
				continue
			}
			fmt.Fprintf(w, "issue %d: deleted branch %s\n", a.IssueNumber, a.Branch)
			branchesDeleted++

		case actionOriginDelete:
			out, err := runGitOut(localRepoPath, "push", "origin", "--delete", a.Branch)
			if err != nil {
				reason := firstLine(out)
				if reason == "" {
					reason = err.Error()
				}
				fmt.Fprintf(w, "issue %d: skip origin delete %s: %s\n", a.IssueNumber, a.Branch, reason)
				skipped++
				continue
			}
			fmt.Fprintf(w, "issue %d: deleted origin/%s\n", a.IssueNumber, a.Branch)

		case actionStateDelete:
			if stateBlocked[a.IssueNumber] {
				continue
			}
			if err := store.Delete(a.IssueNumber); err != nil {
				fmt.Fprintf(w, "issue %d: skip state delete: %v\n", a.IssueNumber, err)
				skipped++
				continue
			}
		}
	}

	fmt.Fprintf(w, "%d worktrees removed, %d branches deleted, %d skipped\n",
		worktreesRemoved, branchesDeleted, skipped)
	return 0
}

// runGit executes git -C <repo> <args...> and discards stdout/stderr.
func runGit(repo string, args ...string) error {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// runGitOut executes git -C <repo> <args...> and returns combined output.
func runGitOut(repo string, args ...string) (string, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// firstLine returns the first non-empty line of s, with surrounding
// whitespace trimmed.
func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[:i]
			for len(line) > 0 && (line[len(line)-1] == '\r' || line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
				line = line[:len(line)-1]
			}
			if line != "" {
				return line
			}
			s = s[i+1:]
			i = -1
		}
	}
	// trim trailing whitespace on the lone/last line
	for len(s) > 0 && (s[len(s)-1] == '\r' || s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
