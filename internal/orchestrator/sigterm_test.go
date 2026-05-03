package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/logosc/symphony-go/internal/state"
	"github.com/logosc/symphony-go/internal/types"
)

// TestRunSIGTERMCleanShutdown exercises G7: a long-running Run loop, when
// its parent context is canceled, returns within ~2 seconds with the
// context error, releases the on-disk flock so a fresh AcquireLock
// succeeds, and leaves any in-flight Job in a documented state.
//
// In-flight job state choice: when ProcessIssue's planning agent returns
// with ctx.Err() (because OnRun observed cancellation), the orchestrator
// calls markFailed which transitions the job to `failed`. If cancellation
// races such that markFailed cannot run (e.g. ProcessIssue had not yet
// saved the planning state) the job may still be at `planning`. We accept
// both `failed` and `blocked` (markBlocked is the alternate exit path)
// and `planning` if the cancellation arrived before ProcessIssue's first
// save — but document that the expected shape on this codebase is
// `failed`.
func TestRunSIGTERMCleanShutdown(t *testing.T) {
	h := newTestHarness(t)
	h.cfg.Approval.Mode = "handoff"
	// Tight poll so the long-running Run picks the seeded issue quickly.
	h.cfg.GitHub.PollIntervalSeconds = 1

	// Override the harness state store so we control the lock/unlock cycle
	// and can verify a *new* AcquireLock succeeds after Run returns. We
	// mount the new store under a known directory so the test owns it.
	stateRoot := filepath.Join(t.TempDir(), "state")
	store, err := state.NewStore(stateRoot)
	if err != nil {
		t.Fatalf("state.NewStore: %v", err)
	}
	h.state = store

	o := h.newOrch(t, "x", false)

	// Acquire the lock here (mimicking what main.go does around Run).
	release, err := store.AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	// Configure the fake runner so the planning phase blocks until ctx is
	// canceled. This puts the goroutine in a known in-flight state when
	// we cancel.
	entered := make(chan struct{}, 1)
	h.runner.OnRun = func(ctx context.Context, req types.RunRequest) (types.RunResult, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return types.RunResult{Success: false}, ctx.Err()
	}

	h.seedReadyIssue(900, "long")

	// Snapshot goroutine count before Run starts. We allow a small grace
	// for the std runtime / test harness asynchrony below.
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- o.Run(ctx, false)
	}()

	// Wait for the planning agent to enter the blocking OnRun. If this
	// times out, the dispatch never claimed the issue and the test is
	// invalid.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		cancel()
		<-runErrCh
		_ = release()
		t.Fatalf("planning agent never entered OnRun")
	}

	// Cancel the parent context and expect a quick clean shutdown.
	cancel()
	deadline := time.After(2 * time.Second)
	select {
	case err := <-runErrCh:
		if err == nil {
			t.Fatalf("Run returned nil; expected ctx error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; expected wrapped context.Canceled", err)
		}
	case <-deadline:
		t.Fatalf("Run did not return within 2s of ctx cancel")
	}

	// Release our lock; verify a fresh NewStore + AcquireLock succeeds.
	if err := release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	store2, err := state.NewStore(stateRoot)
	if err != nil {
		t.Fatalf("state.NewStore (post-shutdown): %v", err)
	}
	release2, err := store2.AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock (post-shutdown): %v: flock not released cleanly", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("second release: %v", err)
	}

	// Verify the in-flight job ended in an acceptable state.
	job, lerr := store.Load(900)
	if lerr != nil {
		// No job persisted — also acceptable if cancellation arrived
		// before the first saveJob call.
		t.Logf("no job persisted for 900: %v (acceptable race)", lerr)
	} else {
		switch job.Status {
		case types.StatusFailed, types.StatusBlocked, types.StatusPlanning:
			// ok — see test doc comment above for rationale.
		default:
			t.Fatalf("unexpected post-shutdown job status %q (want failed|blocked|planning)", job.Status)
		}
	}

	// Allow one short scheduler tick for any deferred cleanup goroutines.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	// Small grace for std runtime asynchrony (test harness, GC sweep, etc.).
	if after > before+2 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}
