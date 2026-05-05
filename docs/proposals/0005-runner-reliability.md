# Proposal 0005 - Runner reliability hardening

| Field | Value |
|---|---|
| Status | Draft |
| Author | symphony-go maintainer |
| Target milestone | Post-Dreamwright soak follow-up |
| Affects | `internal/runner/*`, `internal/config`, `internal/orchestrator/job.go`, `cmd/symphony-go/doctor.go`, `docs/M6-real-runner-smoke.md`, `SPEC.md` section 9 |
| Backward compatible | Yes |
| Closes gap | Long-running agent brittleness caused by provider protocol drift, missing server-request handling, weak diagnostics, and incomplete real-runner smoke coverage |

## 1. Summary

The Dreamwright prompt-eval soak found two concrete Codex runner failure
modes:

- `codex exec --json` changed the `item.completed` event shape so agent
  text moved under a nested `item` object. The runner treated the turn as
  successful but lost the text needed by the orchestrator.
- `codex app-server` sent JSON-RPC approval requests during a turn. The
  runner did not answer those requests, so app-server mode could hang
  until timeout even though the agent was waiting for a protocol response.

Both bugs were fixed, but the larger lesson is that runner reliability
must be treated as a protocol-compatibility surface, not only as process
execution glue. This proposal makes that explicit: app-server becomes the
preferred Codex transport, Claude stream-json gets the same fixture-driven
compatibility coverage, server-initiated requests are handled by policy, and
failures produce diagnostics that explain which layer broke.

## 2. Goals

- Detect provider event-shape drift before a real orchestration run loses
  plan text, review decisions, or implementation summaries.
- Prevent app-server turns from hanging on unhandled JSON-RPC requests.
- Make stall timeouts distinguish "no protocol frames", "waiting on an
  approval request", "turn failed", and "subprocess dead".
- Provide a reproducible real-runner smoke command for Claude and Codex.
- Keep existing `exec` mode working while making app-server the recommended
  Codex path for long planning and implementation jobs.
- Make Claude planning safer by separating "can write Symphony side-channel
  files" from "can edit source files" where the Claude CLI allows it.

## 3. Non-goals

- Replacing the existing `AgentRunner` interface.
- Building a complete generated client for every provider protocol.
- Changing the approval semantics of the orchestrator's issue/PR gates.
- Trusting agent-side approvals beyond the sandbox already selected for the
  phase.

## 4. Design

### 4.1 Promote app-server to the preferred Codex mode

`codex.mode: app-server` should be the documented default for long-running
Codex jobs. It has better liveness properties than `exec` because the
server streams JSON-RPC notifications throughout a turn, which keeps the
event-inactivity watchdog touched during legitimate work.

Config remains backward compatible:

```yaml
codex:
  mode: app-server
  planning_args: ["--sandbox", "read-only"]
  implementation_args: ["--sandbox", "workspace-write"]
  review_args: ["--sandbox", "read-only"]
```

`exec` remains available for installations where app-server is unavailable
or where users need the simpler one-shot subprocess behavior.

### 4.2 Harden Claude stream-json parsing

The Claude runner uses:

```sh
claude -p --output-format stream-json --verbose --max-turns <n> ...
```

Its current extraction rule is practical: prefer the final
`{"type":"result","result":"..."}` event, fall back to assistant text, then
fall back to raw stdout. Keep that behavior, but move the event decoding
into a fixture-tested parser with counters.

Claude-specific fixtures should cover:

- `assistant` messages with `message.content[]` text blocks.
- Empty `result` events that require assistant-text fallback.
- `result` events carrying `is_error`, `subtype`, duration, or token
  metadata.
- `error` events emitted before or after assistant text.
- Malformed JSONL lines mixed with valid events.
- Tool-result-heavy lines near the 16 MiB scanner cap.
- Runs that exit zero but never emit a `result` event.
- Runs that hit `--max-turns` and report that limit in result metadata or
  stderr.

The important invariant is that Symphony should not silently accept an empty
plan, review, or implementation summary when Claude emitted useful assistant
text in a non-final event.

### 4.3 Add protocol compatibility fixtures

Create a small fixture suite under `internal/runner/testdata/` with known
provider frames:

- `codex_exec_item_completed_flat.jsonl`
- `codex_exec_item_completed_nested.jsonl`
- `codex_exec_turn_failed.jsonl`
- `codex_appserver_happy_path.jsonl`
- `codex_appserver_nested_item.jsonl`
- `codex_appserver_command_approval.jsonl`
- `codex_appserver_file_approval_readonly.jsonl`
- `codex_appserver_file_approval_workspace_write.jsonl`
- `claude_stream_json_agent_message.jsonl`
- `claude_stream_json_empty_result_with_assistant_text.jsonl`
- `claude_stream_json_result_error.jsonl`
- `claude_stream_json_max_turns.jsonl`
- `claude_stream_json_malformed_then_result.jsonl`

Unit tests should parse these fixtures without spawning shells. The fake
bash app-server tests are still useful for stdin/stdout ordering, but pure
parser fixtures make event-shape drift cheap to cover and review.

Add tests for these invariants:

- Agent text is extracted from flat and nested event shapes.
- Terminal status is recognized across dot and slash method names.
- Unknown notification methods do not fail the turn.
- Unknown server requests receive a JSON-RPC error response and are counted.
- Read-only phases deny file-change approvals.
- Workspace-write and danger-full-access phases allow file-change
  approvals, but only inside the selected sandbox.
- Claude result text, assistant fallback text, error events, and max-turn
  exhaustion are classified consistently.

### 4.4 Centralize event classification

Today Codex exec mode and app-server mode each decode their own event
shape, and Claude decodes stream-json inline inside `Run`. Consolidate that
into provider-specific parsers:

```go
type AgentEvent struct {
    Kind       string // agent_message | terminal | tool_event | unknown
    Text       string
    Terminal   string // completed | failed | interrupted
    RawMethod  string
    RawType    string
}

type ProtocolStats struct {
    FramesTotal              int
    MalformedFrames          int
    UnknownNotifications     int
    AssistantMessages        int
    ResultEvents             int
    ErrorEvents              int
    MaxTurnsExhausted        bool
    ServerRequestsAnswered   int
    ServerRequestsDenied     int
    ServerRequestsUnsupported int
}
```

`codex.go` and `codex_appserver.go` can still own transport details, but
text extraction and terminal detection should use the same compatibility
logic where possible. When a new provider version adds a wrapper, the fix
lands in one parser and one fixture.

For Claude, the parser should expose:

- `FinalText`: final result text, assistant fallback, or raw fallback.
- `HadErrorEvent`: true when any `type:error` event appears.
- `HadResultEvent`: true when a result event appears.
- `MaxTurnsExhausted`: true when Claude reports a max-turn stop condition.
- `TokenUsage`: optional input/output token counts when present.

That gives the orchestrator enough signal to distinguish a model answer
from a runner/protocol failure.

### 4.5 Treat server-initiated requests as policy decisions

The app-server runner should continue to answer known approval requests:

| Method | Read-only | Workspace-write | Danger-full-access |
|---|---:|---:|---:|
| `item/commandExecution/requestApproval` | accept | accept | accept |
| `execCommandApproval` | approved | approved | approved |
| `item/fileChange/requestApproval` | decline | accept | accept |
| `applyPatchApproval` | denied | approved | approved |

Rationale:

- Command execution still runs inside the thread sandbox selected by
  `thread/start`.
- File changes are phase-sensitive. Planning and review should not write
  files even if Codex asks.
- Unsupported requests should receive JSON-RPC `-32601` and be surfaced in
  diagnostics, not silently ignored.

Add a config escape hatch only if real users need it later. The current
policy is conservative and matches the orchestration model.

### 4.6 Tighten Claude permission strategy

Claude planning currently uses `--permission-mode acceptEdits` because
proposal 0004 introduced a structured side-channel file that the planning
agent must write. That works, but it weakens the planning boundary: the
workflow prompt forbids source edits, while the CLI permission mode allows
edits.

Preferred direction:

1. Keep planning source edits forbidden by policy and reviewer gates.
2. Place side-channel paths under the isolated per-job home directory, never
   under the repo checkout.
3. Investigate whether Claude Code can grant write permission only to the
   side-channel path while keeping repository edits in plan mode.
4. If path-scoped writes are unavailable, add a post-planning guard that
   fails the planning phase if the repo diff is non-empty after planning.

The post-planning guard is cheap and provider-agnostic. Even if Claude must
remain in `acceptEdits` for side-channel writes, Symphony can enforce that
planning produced no source diff before any approval decision is evaluated.

### 4.7 Improve watchdog diagnostics

Keep the existing event-inactivity watchdog, but attach the reason to the
result:

- `stall:no_frames` - subprocess produced no frames within the timeout.
- `stall:mid_turn` - frames arrived earlier, then stopped before terminal.
- `stall:pending_server_request` - runner received a server request and
  failed or timed out while responding.
- `turn:failed` - provider emitted a failed terminal status.
- `transport:closed` - stdout closed before terminal status.
- `turn:max_turns` - Claude reached `--max-turns` before producing a usable
  final result.
- `auth:missing` - runner stderr indicates the provider CLI is not logged in
  or its expected home-state files are absent.

This can start as structured text in `RunResult.Stderr` or `RunResult.Events`
without changing public APIs. A later change can add typed fields to
`RunResult` if the orchestrator needs to branch on them.

### 4.8 Add per-run diagnostics artifact

For every runner invocation, write a redacted JSON diagnostics object under
the per-job home directory, for example:

```json
{
  "provider": "codex",
  "mode": "app-server",
  "phase": "planning",
  "sandbox": "read-only",
  "started_at": "2026-05-05T00:00:00Z",
  "completed_at": "2026-05-05T00:03:12Z",
  "success": true,
  "terminal_status": "completed",
  "protocol_stats": {
    "frames_total": 184,
    "malformed_frames": 0,
    "unknown_notifications": 3,
    "server_requests_answered": 2,
    "server_requests_denied": 0,
    "server_requests_unsupported": 0
  },
  "claude": {
    "assistant_messages": 0,
    "result_events": 0,
    "error_events": 0,
    "max_turns_exhausted": false
  }
}
```

This gives the orchestrator and human operator a compact postmortem trail
without scanning megabytes of raw provider events.

### 4.9 Add real-runner smoke tests

Add a manual smoke command that runs outside unit tests:

```sh
go run ./cmd/symphony-go runner-smoke \
  --provider codex \
  --mode app-server \
  --repo /tmp/symphony-runner-smoke \
  --sandbox read-only
```

And the Claude equivalent:

```sh
go run ./cmd/symphony-go runner-smoke \
  --provider claude \
  --repo /tmp/symphony-runner-smoke \
  --permission-mode plan
```

The smoke should:

1. Start the runner.
2. Ask for a short plan with a required sentinel phrase.
3. Ask for a harmless shell command such as `pwd` or `rg --files`.
4. Verify the sentinel appears in `RunResult.Text`.
5. Verify the run reaches a completed terminal status.
6. Print protocol stats and the diagnostics artifact path.

Claude-specific smoke checks should also verify:

- `claude` is on `PATH`.
- The isolated home has the required Claude auth state symlinked, including
  both `~/.claude/` and `~/.claude.json` when subscription auth needs it.
- A plan-mode smoke works for read-only planning.
- An accept-edits smoke can write a temporary file in the smoke repo.
- A low `--max-turns` run reports a clear max-turn diagnostic.

This should be documented as a release checklist item and run whenever
upgrading Codex CLI, Claude Code, or changing runner code.

### 4.10 Reconcile orphan: local-terminal vs GitHub-active

Today's Dreamwright session left three issues split-brain: local job
state said `failed` (the orchestrator had observed the agent's failure
and called `markFailed`), but the GitHub-side relabel + comment POST
was dropped by a network blip (a 03:12 RST observed in the log) so the
issue stayed labelled `implementing`/`planning`/`awaiting-approval`.
The next reconcile tick didn't fix it because the existing reconcile
table doesn't carry a row for "local terminal AND GitHub still
active." The job stayed orphaned until a human moved the labels.

Add one row:

| Local status | GitHub label | Action |
|---|---|---|
| `failed`, `pr_ready`, or `blocked` | any active label (`planning`, `implementing`, `awaiting-approval`, `ready`) | Re-attempt the relabel to the matching terminal label and re-post the failure/completion comment. Both calls must be idempotent. On second failure, log error at WARN and leave alone — next reconcile tick retries. |

Idempotency notes:

- `ReplaceStateLabel` is idempotent: removing a label that's already
  absent and adding one that's already present are both no-ops.
- `PostIssueComment` is *not* idempotent — naive retry posts duplicate
  comments. Either (a) skip re-posting when reconcile is the trigger
  (the local state already records the reason), or (b) use a marker
  fingerprint in the comment body and check existing comments before
  posting. Option (a) is simpler and probably right; the local
  `Job.UpdatedAt` already tracks when the failure was observed.

Cost: ~30 LOC + 2 tests (one for each idempotent path).

### 4.11 Add `doctor` protocol checks

`symphony-go doctor` should inspect the configured provider mode and warn
when the setup is likely brittle:

- Codex provider with no `codex.mode` set: recommend `app-server`.
- App-server mode but installed Codex does not support `codex app-server`.
- `agent.stall_timeout_seconds` is unset or too low for long planning jobs.
- Planning args resolve to a write-capable sandbox.
- Implementation args resolve to read-only sandbox.
- Claude provider but `claude` is not on `PATH`.
- Claude provider but `claude.max_turns` is zero or too low for the selected
  workflow.
- Claude planning uses `acceptEdits` and no post-planning diff guard is
  enabled.
- Claude subscription auth appears incomplete in the orchestrator-visible
  home state.

Doctor should not require API keys or run a real agent turn; it should only
check executable availability, supported subcommands, and config coherence.

## 5. Migration plan

Ordered by dependency — earlier items unblock later ones. Items
without arrows are independent and can land in parallel.

1. **Parser fixtures + `AgentEvent` / `ProtocolStats` types**
   (foundation). Pure data tests, no behavior change. Unblocks 2, 3, 6.
2. **`ProtocolStats` counters wired into runner outputs** → uses (1).
   Counters land in `RunResult.Events` or a side-channel JSON so the
   public API surface stays unchanged.
3. **App-server server-request policy tests** → uses (1).
   Sandbox-vs-method matrix from §4.5 covered fixture-by-fixture.
4. **Claude post-planning diff guard** (independent). Cheap and
   provider-agnostic; lands without touching the runner refactor.
5. **Reconcile orphan row** (independent). Adds one row to the
   reconcile table per §4.10; idempotent retry of the dropped GitHub
   write.
6. **Doctor warnings** → uses (2) for protocol-stats-aware checks.
   Mode, sandbox, permission, auth, max-turn coherence warnings per
   §4.11.
7. **Runner smoke command** → uses (1) and (2). Manual smoke for
   Codex (exec + app-server) and Claude (plan + acceptEdits).
8. **Docs default flip**: update onboarding examples and
   `testdata/config.example.yml` to prefer `codex.mode: app-server`.
   Land last so users find the new defaults after the supporting
   tests/diagnostics are in place.

## 6. Acceptance criteria

- `go test ./internal/runner ./internal/config ./cmd/symphony-go` passes.
- Fixture tests cover both flat and nested Codex `item.completed` shapes.
- App-server tests prove that command approval requests are answered and
  file-change approval requests are sandbox-gated.
- Claude fixture tests cover result extraction, assistant fallback, error
  events, malformed lines, and max-turn exhaustion.
- Claude planning cannot leave a source diff behind before approval gating.
- A simulated unhandled server request does not hang; it returns a JSON-RPC
  error response and the run continues or fails with a clear diagnostic.
- A simulated mid-turn stall reports `stall:mid_turn` or equivalent text.
- `symphony-go doctor` recommends app-server mode for Codex when appropriate
  and warns on brittle Claude auth, max-turn, or planning-permission setups.
- The manual runner smoke completes against the installed Codex CLI and
  Claude CLI and prints protocol stats.

## 7. Risks and tradeoffs

- **Protocol fixture drift**: fixtures can become stale. This is acceptable;
  stale fixtures are still regression coverage. Real-runner smoke catches
  live CLI changes.
- **Over-approving commands**: command approvals are accepted in every phase.
  The safety boundary remains the Codex sandbox. If that assumption changes,
  make command approval sandbox-sensitive too.
- **Diagnostics file churn**: diagnostics should live under per-job home
  directories or ignored run directories, never in source control.
- **API surface creep**: start with diagnostics embedded in existing result
  artifacts. Add typed `RunResult` fields only after the orchestrator needs
  them.
- **Claude planning writes**: side-channel output requires a write path.
  Until Claude supports path-scoped side-channel writes, enforce a
  post-planning no-diff guard.
- **Claude auth variability**: subscription auth and API-key auth leave
  different local state. Doctor and runner-smoke should check both common
  setups without printing credentials.

## 8. Implementation estimate

| Migration step | LOC | Time |
|---|---:|---|
| 1. Parser fixtures + `AgentEvent` / `ProtocolStats` types | 220-350 | 1-1.5d |
| 2. `ProtocolStats` counters wired into runners | (in 1) | (in 1) |
| 3. App-server server-request policy tests | 100-180 | 0.5d |
| 4. Claude post-planning diff guard | 60-120 | 0.5d |
| 5. Reconcile orphan row | 30-60 | 0.5d |
| 6. Doctor protocol/auth/coherence checks | 120-220 | 0.5-1d |
| 7. Runner smoke command + docs | 220-350 | 1-1.5d |
| 8. Default flip in `testdata/config.example.yml` and onboarding | 5-15 | minutes |

**Total: roughly 4-5 focused engineering days.**

**Parallelism.** Items 4 and 5 are independent of the parser refactor
and can land in any order. Items 3, 6, 7 chain through 1+2. A
two-engineer split is natural: A on the foundation (1+2+3+7), B on
the cross-cutting safety items (4+5+6+8).

## 9. Recommended priority

Do this before the next overnight autonomous run that depends on Codex or
Claude. The fixes already landed address the observed Dreamwright failures,
but the proposal closes the class of failures by making provider protocols
tested, observable, and smoke-tested against the real installed CLIs.
