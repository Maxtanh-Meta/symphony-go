package orchestrator

// tryClaim atomically marks issueNum as in-flight. Returns false when
// the issue is already being processed (caller should skip).
func (o *Orchestrator) tryClaim(issueNum int) bool {
	o.runningMu.Lock()
	defer o.runningMu.Unlock()
	if _, ok := o.running[issueNum]; ok {
		return false
	}
	o.running[issueNum] = struct{}{}
	return true
}

// release removes issueNum from the in-flight set. Idempotent; calling
// release on a never-claimed or already-released issue is a no-op.
func (o *Orchestrator) release(issueNum int) {
	o.runningMu.Lock()
	defer o.runningMu.Unlock()
	delete(o.running, issueNum)
}

// inflightCount returns the current number of in-flight jobs.
func (o *Orchestrator) inflightCount() int {
	o.runningMu.Lock()
	defer o.runningMu.Unlock()
	return len(o.running)
}
