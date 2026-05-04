package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/logosc/symphony-go/internal/runner"
	"github.com/logosc/symphony-go/internal/types"
)

// barrierRunner wires a counting/release barrier into the FakeRunner's
// OnRun. Each call to OnRun increments concurrent and inCount, blocks on
// release, then decrements concurrent. peakConcurrent records the
// max-observed concurrent value.
type barrier struct {
	mu              sync.Mutex
	concurrent      int
	peakConcurrent  int
	totalEntries    int64
	release         chan struct{}
	plan            string
	implFiles       []string
	planningEntered chan struct{}
}

func newBarrier(plan string, implFiles []string) *barrier {
	return &barrier{
		release:         make(chan struct{}),
		plan:            plan,
		implFiles:       implFiles,
		planningEntered: make(chan struct{}, 32),
	}
}

func (b *barrier) install(rnr *runner.FakeRunner) {
	rnr.OnRun = func(ctx context.Context, req types.RunRequest) (types.RunResult, error) {
		b.mu.Lock()
		b.concurrent++
		if b.concurrent > b.peakConcurrent {
			b.peakConcurrent = b.concurrent
		}
		b.mu.Unlock()
		atomic.AddInt64(&b.totalEntries, 1)

		// Notify the test that one more goroutine has entered the planning
		// runner. Non-blocking so this works for arbitrary fan-in.
		if req.Phase == types.PhasePlanning {
			select {
			case b.planningEntered <- struct{}{}:
			default:
			}
		}

		// Block until the test releases (or ctx cancellation).
		select {
		case <-b.release:
		case <-ctx.Done():
			b.mu.Lock()
			b.concurrent--
			b.mu.Unlock()
			return types.RunResult{}, ctx.Err()
		}

		b.mu.Lock()
		b.concurrent--
		b.mu.Unlock()

		switch req.Phase {
		case types.PhasePlanning:
			return types.RunResult{Success: true, Text: b.plan}, nil
		case types.PhaseImplementation:
			// Use implWriter-like behavior: write the configured files.
			return implWriteRunResult(req.RepoPath, b.implFiles)
		}
		return types.RunResult{Success: true, Text: "ok"}, nil
	}
}

// implWriteRunResult writes the listed files at repoPath and returns a
// successful RunResult. Mirrors orchestrator_test.go's implWriter but
// inlined here.
func implWriteRunResult(repoPath string, files []string) (types.RunResult, error) {
	for _, rel := range files {
		p := filepath.Join(repoPath, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return types.RunResult{}, err
		}
		if err := os.WriteFile(p, []byte("body for "+rel+"\n"), 0o644); err != nil {
			return types.RunResult{}, err
		}
	}
	return types.RunResult{Success: true, Text: "done"}, nil
}

// TestMaxConcurrentJobsSerial: default config (max=1), dispatch 3 ready
// issues. Assert at most 1 goroutine in-flight at any time.
func TestMaxConcurrentJobsSerial(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	h.cfg.Orchestrator.MaxConcurrentJobs = 1
	for i := 1; i <= 3; i++ {
		h.seedReadyIssue(i, "fix")
	}
	o := h.newOrch(t, "x", false)

	plan := canonicalPlan([]string{"a.txt"})
	bar := newBarrier(plan, []string{"a.txt"})
	bar.install(h.runner)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Drain the runner by releasing on demand, in the background.
	releaserDone := make(chan struct{})
	go func() {
		defer close(releaserDone)
		for {
			select {
			case bar.release <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Each Run(once=true) tick dispatches up to slots=1 ready issues and
	// waits for them to complete. Three ticks drains all three issues.
	for i := 0; i < 3; i++ {
		if err := o.Run(ctx, true); err != nil {
			t.Fatalf("Run #%d: %v", i+1, err)
		}
	}
	cancel()
	<-releaserDone

	if bar.peakConcurrent > 1 {
		t.Fatalf("peakConcurrent = %d; want 1 in serial mode", bar.peakConcurrent)
	}
	if got := atomic.LoadInt64(&bar.totalEntries); got < 6 {
		t.Fatalf("totalEntries = %d; want >= 6 (3 issues * 2 phases) labels1=%v labels2=%v labels3=%v",
			got, h.labelsFor(1), h.labelsFor(2), h.labelsFor(3))
	}
}

// TestMaxConcurrentJobsParallel: with max=2, dispatch 3 ready issues and
// assert exactly 2 concurrent entrants at peak.
func TestMaxConcurrentJobsParallel(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	h.cfg.Orchestrator.MaxConcurrentJobs = 2
	o := h.newOrch(t, "x", false)

	plan := canonicalPlan([]string{"a.txt"})
	bar := newBarrier(plan, []string{"a.txt"})
	bar.install(h.runner)

	for i := 1; i <= 3; i++ {
		h.seedReadyIssue(i, "fix")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- o.Run(ctx, true)
	}()

	// Wait for the first two planning entrants to arrive.
	for i := 0; i < 2; i++ {
		select {
		case <-bar.planningEntered:
		case <-time.After(20 * time.Second):
			t.Fatalf("only %d planning entrants observed (want 2)", i)
		}
	}

	// At this point we expect exactly 2 concurrent runners. The third
	// issue must NOT have entered planning yet.
	bar.mu.Lock()
	got := bar.concurrent
	bar.mu.Unlock()
	if got != 2 {
		t.Fatalf("concurrent runners = %d; want 2", got)
	}

	// Drain in the background; the in-flight pair needs planning + impl
	// each (4 total). The third issue is never dispatched in once-mode
	// because both slots are taken when tick() returns.
	go func() {
		for {
			select {
			case bar.release <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("Run did not return")
	}

	if bar.peakConcurrent != 2 {
		t.Fatalf("peakConcurrent = %d; want exactly 2", bar.peakConcurrent)
	}
	// Exactly two issues should have been processed (4 phase entries).
	if got := atomic.LoadInt64(&bar.totalEntries); got != 4 {
		t.Fatalf("totalEntries = %d; want 4 (2 issues * 2 phases)", got)
	}
}

// TestSameIssueClaimedOnce: dispatch the same issue twice in rapid
// succession; only one ProcessIssue runs.
func TestSameIssueClaimedOnce(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	h.cfg.Orchestrator.MaxConcurrentJobs = 4
	o := h.newOrch(t, "x", false)

	plan := canonicalPlan([]string{"a.txt"})
	bar := newBarrier(plan, []string{"a.txt"})
	bar.install(h.runner)

	iss := h.seedReadyIssue(42, "fix")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Two concurrent ProcessIssue calls: second must early-return because
	// the first has already claimed the issue.
	var wg sync.WaitGroup
	var firstErr, secondErr error
	wg.Add(2)
	started := make(chan struct{})
	go func() {
		defer wg.Done()
		<-started
		firstErr = o.ProcessIssue(ctx, iss)
	}()
	go func() {
		defer wg.Done()
		<-started
		// Tiny sleep so the first goroutine wins the claim.
		time.Sleep(50 * time.Millisecond)
		secondErr = o.ProcessIssue(ctx, iss)
	}()
	close(started)

	// Release planning + implementation runner blocks in the background.
	releaserDone := make(chan struct{})
	go func() {
		defer close(releaserDone)
		for {
			select {
			case bar.release <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
	cancel()
	<-releaserDone
	if firstErr != nil {
		t.Fatalf("first ProcessIssue: %v", firstErr)
	}
	if secondErr != nil {
		t.Fatalf("second ProcessIssue: %v", secondErr)
	}
	// The runner records all calls; we expect 2 (one planning + one
	// implementation), not 4.
	calls := h.runner.Calls()
	if len(calls) != 2 {
		t.Fatalf("runner calls = %d; want 2 (claim should have deduped)", len(calls))
	}
}

// TestRunCancelDrains: with max=2 and 2 in-flight blocking goroutines,
// canceling ctx makes Run return within ~5s and goroutine count is back
// to baseline.
func TestRunCancelDrains(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	h.cfg.Orchestrator.MaxConcurrentJobs = 2
	// Tight poll interval so the dispatch loop fires quickly.
	h.cfg.GitHub.PollIntervalSeconds = 1
	o := h.newOrch(t, "x", false)

	plan := canonicalPlan([]string{"a.txt"})
	bar := newBarrier(plan, []string{"a.txt"})
	bar.install(h.runner)

	for i := 1; i <= 2; i++ {
		h.seedReadyIssue(i, "fix")
	}

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- o.Run(ctx, false)
	}()

	// Wait for both jobs to enter planning. Generous timeout because CI
	// runs are noticeably slower than dev laptops (the dispatch loop must
	// fire its 1s tick, then both goroutines must spawn, set up worktrees,
	// and reach the fake runner — each step can stretch on a 2-core CI).
	for i := 0; i < 2; i++ {
		select {
		case <-bar.planningEntered:
		case <-time.After(60 * time.Second):
			cancel()
			<-runDone
			t.Fatalf("only %d planning entrants observed (want 2)", i)
		}
	}

	// Cancel and unblock the runner so the goroutines can return.
	cancel()
	// Release all blocked runner calls (their ctx is already cancelled,
	// so they'll exit via the ctx.Done() branch — but be defensive).
	go func() {
		for i := 0; i < 8; i++ {
			select {
			case bar.release <- struct{}{}:
			case <-time.After(2 * time.Second):
				return
			}
		}
	}()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("Run returned nil; expected ctx.Err()")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Run did not return within 10s after cancel (drain budget is %s)", drainTimeout)
	}

	// Goroutine count should be back near baseline. Allow grace for
	// runtime/test framework goroutines.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+4 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}
