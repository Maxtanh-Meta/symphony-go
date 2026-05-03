package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/logosc/symphony-go/internal/config"
	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/types"
)

// statusCommand implements `symphony-go status`. It loads the config to
// locate the state directory, reads jobs via state.Store.List, and renders
// a table to stdout. It is read-only and intentionally does NOT acquire
// the cross-process flock so it can be invoked alongside an active `run`.
func statusCommand(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
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

	storeRoot := filepath.Join(cfg.Repo.LocalPath, ".symphony-go", "state")
	// Read-only command: deliberately no AcquireLock so `status` works
	// concurrently with an active `symphony-go run` instance.
	store, err := state.NewStore(storeRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state init: %v\n", err)
		return 1
	}
	jobs, err := store.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "state list: %v\n", err)
		return 1
	}
	if err := renderJobs(os.Stdout, jobs); err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		return 1
	}
	return 0
}

// renderJobs writes a tab-aligned table of jobs to w. Columns:
// ISSUE, STATUS, BRANCH, PR, APPROVAL, UPDATED. Jobs are sorted by
// IssueNumber ascending. Branch names longer than 40 runes are truncated
// with a trailing ellipsis. An empty PRNumber renders as "-". UpdatedAt
// is rendered as RFC3339 in the local timezone. If jobs is empty,
// renderJobs writes "no jobs\n".
func renderJobs(w io.Writer, jobs []*types.Job) error {
	if len(jobs) == 0 {
		_, err := fmt.Fprintln(w, "no jobs")
		return err
	}

	sorted := make([]*types.Job, len(jobs))
	copy(sorted, jobs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].IssueNumber < sorted[j].IssueNumber
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ISSUE\tSTATUS\tBRANCH\tPR\tAPPROVAL\tUPDATED"); err != nil {
		return err
	}
	for _, j := range sorted {
		pr := "-"
		if j.PRNumber > 0 {
			pr = "#" + strconv.Itoa(j.PRNumber)
		}
		approval := string(j.ApprovalPath)
		if approval == "" {
			approval = "-"
		}
		status := string(j.Status)
		if status == "" {
			status = "-"
		}
		branch := truncate(j.Branch, 40)
		if branch == "" {
			branch = "-"
		}
		updated := "-"
		if !j.UpdatedAt.IsZero() {
			updated = j.UpdatedAt.Local().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			j.IssueNumber, status, branch, pr, approval, updated); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// truncate shortens s to at most max runes, appending a single ellipsis
// rune when truncation occurs. If max <= 1 and truncation is required,
// truncate returns just the ellipsis.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
