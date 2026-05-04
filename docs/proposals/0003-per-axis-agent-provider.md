# Proposal 0003 — Per-axis `agent.provider` and `agent.model`

| Field | Value |
|---|---|
| Status | Draft |
| Author | print-my-ideas operator |
| Target milestone | Follow-up to commit `b0b67f9` |
| Affects | `internal/config`, `internal/orchestrator/job.go` (or wherever provider is read at dispatch), `internal/runner/*` (already takes `req.AxisKey`), `SPEC.md` §9, `docs/per-axis-config.md` |
| Backward compatible | Yes |
| Closes gap | G19 |

## 1. Summary

Proposal 0001 §11 (Open questions) flagged: "Should `agent.provider`
be per-axis too? Recommendation: yes, include in G13." Commit
`b0b67f9` implemented the rest of Proposal 0001 but missed this item;
`testdata/config.example.yml:59` and `internal/config.AgentConfig`
still only carry scalar `Provider` and `Model`.

This proposal adds `provider_by_label` and `model_by_label` maps to
`AgentConfig`, using the same `OrderedMap[T]` + `ResolveAxis[T]`
machinery that `b0b67f9` already introduced for the four other
per-axis knobs. Backward compatible. ~50 LOC + tests.

## 2. Motivation

A real operator (print-my-ideas) needs codex for image-generating
axes (`type:mockup`, `type:marketing-creative`) and claude for
code-shaped axes (`type:code`, `type:deploy`, `type:research`).
Inline imagegen is a model-side capability of codex's image-capable
models; claude doesn't have it. Without per-axis provider, the
operator's options are:

- Pick one provider globally → can't do mockups (claude) or research
  with WebFetch/WebSearch tooling becomes constrained (codex).
- Run two symphony-go processes on the same host with disjoint label
  filters → no `IgnoredUsers` collision since each is a distinct
  bot, but two flock-distinct state directories, two daemon configs,
  two log streams. Operationally annoying.
- Defer the codex axes until G19 lands → exactly this proposal.

## 3. Goals

- Operators select runner provider per axis with the same
  declaration-order map shape as the other `_by_label` knobs.
- Operators select model per axis (since per-provider models differ).
- Existing scalar `provider:` / `model:` configs keep working.
- Resolved provider is frozen on the `Job` at claim time
  (consistent with `Job.AxisKey`).
- Reviewer-provider stays inverse-of-runner per the existing
  `auto.reviewer.provider` field — no new reviewer-side knob.

## 4. Non-goals

- Mixed-provider runs within one job (planning vs implementation
  with different providers). The reviewer is already a different
  provider; that's the only intentional split.
- Auto-detecting provider from model name. Operator declares both.
- Per-axis `timeout_seconds` (existing scalar is fine; can add later
  with same pattern if needed).
- Auto-installing or login-checking codex/claude CLIs at startup.

## 5. Design

### 5.1 Config schema additions

```yaml
agent:
  provider: "claude"                              # existing scalar (fallback)
  provider_by_label:                              # new
    "type:mockup":             "codex"
    "type:marketing-creative": "codex"
    default:                   "claude"
  model: "sonnet"                                 # existing scalar (fallback)
  model_by_label:                                 # new
    "type:mockup":             "gpt-5-codex"
    "type:marketing-creative": "gpt-5-codex"
    default:                   "sonnet"
  timeout_seconds: 3600                           # unchanged, still global
```

Resolution: the same `ResolveAxis[T]` introduced in `b0b67f9` keyed on
`Job.AxisKey`. First match in declaration order, `default` fallback,
case-insensitive label compare. Identical semantics to the four
other per-axis knobs.

### 5.2 Validation

Same shape as the existing per-axis knobs:

- Scalar+map collision (both `provider:` and `provider_by_label:` set)
  → reject at config validation with a specific error.
- Map present but missing `default` key → reject.
- `provider_by_label` value not in `{"claude", "codex"}` → reject.
- `model_by_label` values are accepted as opaque strings (model lists
  evolve; reject only at runner-spawn time if invalid).

### 5.3 Code changes

**`internal/config/config.go` — `AgentConfig`:**

```go
type AgentConfig struct {
    Provider         string              `yaml:"provider"`
    ProviderByLabel  *OrderedMap[string] `yaml:"provider_by_label,omitempty"`
    Model            string              `yaml:"model"`
    ModelByLabel     *OrderedMap[string] `yaml:"model_by_label,omitempty"`
    TimeoutSeconds   int                 `yaml:"timeout_seconds"`
}
```

**`internal/config/validate.go`:** add three checks per the rules in
§5.2; reuse the helpers `b0b67f9` already defined for collision +
default-key validation.

**`types.Job`:** already carries `AxisKey`; add `AgentProvider string`
and `AgentModel string` (frozen at claim time alongside `AxisKey`).
Persist in local state.

**`internal/orchestrator/job.go`** (or wherever the provider is
selected at dispatch):

```go
provider := cfg.Agent.Provider
if cfg.Agent.ProviderByLabel != nil {
    provider, _ = ResolveAxis(*cfg.Agent.ProviderByLabel, axisKey)
}
model := cfg.Agent.Model
if cfg.Agent.ModelByLabel != nil {
    model, _ = ResolveAxis(*cfg.Agent.ModelByLabel, axisKey)
}
job.AgentProvider = provider
job.AgentModel = model
```

The dispatcher already branches on provider to pick the runner
implementation. That branch reads `job.AgentProvider` instead of
`cfg.Agent.Provider`.

**Runner side:** `internal/runner/{claude,codex}.go` already receive
`req.AxisKey` per `b0b67f9`. They additionally need `req.Model`
(currently they read from `cfg.Agent.Model` directly — change to
take it from the request, where the orchestrator pre-resolved it).

### 5.4 Reconcile-on-startup interaction

`AgentProvider` and `AgentModel` are frozen on the `Job` record. On
restart, reconcile reads them from local state. Mid-run config changes
(e.g. operator changes `provider_by_label` while a `type:mockup` job
is in `implementing`) do not divert that job; it finishes on the
provider that claimed it. Same protection as `AxisKey` already gives.

### 5.5 Doctor

Two new checks:

- For each `provider_by_label` value: must be `"claude"` or `"codex"`.
- For each value, optionally probe: is the corresponding CLI
  (`claude` or `codex`) on `$PATH`? Warn (not fail) if missing —
  could be intentional during config bootstrapping. Same gentleness
  as the existing label-not-on-any-issue warning.

### 5.6 PR provenance

The PR body's `## Provenance` section (added in `b0b67f9` for axis
attribution) gains a line like:

```
## Provenance
Axis: type:mockup
Source: workflow_files map (declaration order match)
Runner: codex / gpt-5-codex
Reviewer: claude / sonnet
Approval path: handoff
```

So a human reviewer can confirm at a glance which provider produced
the diff.

## 6. Backward compatibility

- Configs that omit `provider_by_label` and `model_by_label` keep
  using the scalar fields. Existing test fixtures unchanged.
- `Job` records without `AgentProvider`/`AgentModel` (from before this
  proposal lands) are migrated on read by filling from the global
  scalar at the time of read. Same approach as `AxisKey` had.
- No on-disk state migration tool needed.

## 7. Test plan

Unit:
- Config: parse `provider_by_label` with valid values; reject unknown
  values (e.g. `"openai"`); reject scalar+map collision; reject
  missing `default`; allow `model_by_label` with arbitrary string
  values.
- Resolver: existing `axes_test.go` table-driven tests get two more
  rows covering provider + model resolution.

Orchestrator:
- A two-axis config with `type:code` (claude) and `type:mockup`
  (codex). Two issues claimed; assert `Job.AgentProvider` differs;
  assert the right runner spawn was called per provider. Use the
  existing fake runners.
- Reconcile: `Job.AgentProvider` survives a mid-run restart.

Integration (with fakes):
- An end-to-end flow where the same orchestrator process drives one
  claude-axis and one codex-axis issue concurrently (or serially with
  the global single-job lock; either is fine — the test doesn't care).

Doctor:
- Reject invalid provider values.
- Warn (not fail) when a referenced CLI binary isn't on `$PATH`.

## 8. Documentation

- `SPEC.md` §9 (Agent runner) gets a "Per-axis runner selection"
  paragraph.
- `docs/per-axis-config.md` adds an `agent` row to the config-shape
  table; the print-my-ideas-shaped 5-axis example gains
  `provider_by_label` so it now exercises all five per-axis knobs.
- `testdata/config.example.yml` gets `provider_by_label:` +
  `model_by_label:` shown commented under the existing scalar fields,
  with the same "leave commented unless you mean to use" convention.

## 9. Rollout

Single PR. Order:

1. `internal/config` schema + validation + tests.
2. `types.Job` field additions + persistence.
3. Dispatch-site change reading from `Job.AgentProvider`/`Model`.
4. Runner request struct field additions; runners read from request.
5. Doctor checks + tests.
6. Docs (SPEC, per-axis-config, example yml).

## 10. Risks

- **Operator picks a model name that doesn't match the provider**
  (e.g. `provider: codex, model: sonnet`). The runner will fail at
  spawn. Mitigation: doctor doesn't validate this combination
  (provider/model pairing rules drift over time); the actionable error
  comes from the runner's first invocation, surfaced in the
  orchestrator log. Document this clearly; don't try to be cleverer.
- **Cross-provider reviewer recommendation gets stale.** The
  existing `auto.reviewer.provider` is global. With per-axis runners,
  some axes' reviewer might end up the same as their runner (if both
  default to one provider). Acceptable for v1; revisit only if it
  causes false-positive auto-approves.

## 11. Open questions

1. **Should `agent.timeout_seconds` also become per-axis?** Image-gen
   axes might want longer timeouts than code axes. Defer; add only
   if a concrete operator needs it. Same pattern, ~10 LOC.
2. **Should the doctor warn when `provider_by_label` and
   `auto.reviewer.provider` collide for some axis (i.e. reviewer ==
   runner for that axis)?** Probably yes; add as a warning in §5.5.
   Mark as a follow-up if not done in this PR.

## 12. Estimated effort

- Config schema + validation + tests: ~25 LOC + 50 LOC test, 1 hour.
- Job + dispatch + runner threading: ~15 LOC + 30 LOC test, 1 hour.
- Doctor + docs: ~30 min.
- Manual smoke against a real two-provider config: 30 min.

**Total: ~3 hours.** Should land as a single small PR.
