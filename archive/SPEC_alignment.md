# SPEC_minimal.md vs Symphony Service Specification Draft v1

Audit of how `SPEC_minimal.md` aligns with the language-agnostic Symphony
Service Specification you pasted. Honest answer up front: **it does not fully
follow that spec**. It follows your prior `SPEC.md`, which itself already
deviated from Symphony in load-bearing ways. This document maps every
deviation so you can decide which to revisit.

---

## TL;DR

Your prior `SPEC.md` already diverged from Symphony in three load-bearing
ways:

1. **GitHub Issues, not Linear** (you flagged this explicitly).
2. **Orchestrator owns tracker writes** (PR creation). Symphony says writes
   are typically performed by the agent.
3. **Mandatory human approval gate between plan and implementation.**
   Symphony has no such gate; runs end at workflow-defined handoff states.

`SPEC_minimal.md` inherits all three, plus adds:

4. **Config split out of the repo** (security fix A1). Symphony explicitly
   says `WORKFLOW.md` should be self-contained.
5. **Single-shot agent runs per phase, no multi-turn continuation.**
   Symphony has continuation as a first-class concept.
6. **`codex exec` as the default runner**, app-server only as an option.
   Symphony's reference command is `codex app-server`.

If "follow the Symphony spec" is the goal, items 4–6 should be revisited.
Items 1–3 are your design choices and can stay.

---

## Concept-by-concept alignment

### Preserved

| Symphony concept | Where in SPEC_minimal.md | Notes |
|---|---|---|
| Long-running daemon with poll loop | §1, §3 | `run` and `run --once` |
| Per-issue workspace isolation | §8 | git worktree under `.minisymphony/wt/` |
| Workspace key sanitized identifier | §8 | matches Symphony §4.2 / §9.2 |
| WORKFLOW.md as repo-owned prompt | §2 | content is preserved; structure split (see deviation 4) |
| Bounded global concurrency | §11 (implicit) | default 1 in MVP |
| Reconciliation before dispatch | §7 | full table in `SPEC_PATCH.md` |
| Restart recovery without DB | §6 | JSON state files + flock |
| Hard cwd validation for agent | §9 | `cmd.Dir = req.RepoPath` |
| Workspace path stays inside workspace root | §8 | implicit via construction |
| JSON-RPC-like agent protocol | §9 | both Claude and Codex covered |
| Token / event streaming capture | §9 | via `RunResult.Events` |
| Stop sticky label cancels active run | §5 | matches Symphony "active state changes make ineligible" |

### Cut, intentionally — inherited from your prior SPEC.md

| Symphony concept | Status | Why kept the cut |
|---|---|---|
| Linear tracker | dropped, GitHub Issues used | your explicit choice; Symphony §3.3 allows other adapters |
| Tracker writes by agent | orchestrator owns PR creation | your prior SPEC.md §10 security posture |
| Workflow-defined handoff state (e.g. `Human Review`) | mandatory `/symphony approve` comment + draft PR | your prior SPEC.md §15.2 |
| `linear_graphql` client-side tool | N/A for GitHub | could add an analogous `github_graphql` later |
| Per-state concurrency limits, blocker-aware dispatch | dropped | GitHub Issues lacks native blockers; defer |

### Cut, by my simplifications — should reconsider

| Symphony concept | What I cut | Cost | Recommend |
|---|---|---|---|
| Workspace hooks (`after_create`, `before_run`, `after_run`, `before_remove`) | dropped | high — repos need bootstrap (npm install, generate schema, etc.) | **add back** (low cost, high value) |
| Multi-turn continuation on same thread | dropped, single-shot per phase | medium — agents finish 80% of long tasks via continuation | document as deferred; add when codex app-server lands |
| `codex app-server` as default runner | demoted to opt-in | medium — loses streaming events, stall detection by inactivity | **support both, default to `app-server` when available** |
| Stall detection by event inactivity | wall-clock timeout only | low — wall-clock is enough for MVP | keep as-is; revisit with app-server |
| Dynamic config reload | frozen during runs | low — security trade-off for A1 | keep frozen; document as deliberate |
| Workflow validation as preflight per tick | only at startup | low | keep startup-only for MVP |

### Cut, inherent to security-first split (A1)

| Symphony concept | What I changed | Rationale |
|---|---|---|
| `WORKFLOW.md` self-contained (config + prompt) | split: config in `~/.minisymphony/config.yml`, prompt-only in repo | A1 — agent edits in workspace can self-promote |
| Hot-reload of any config field | frozen while jobs are running | same A1 reasoning |
| `tracker.api_key: $VAR` indirection in WORKFLOW.md | resolved in config.yml outside repo | same |

These three are deliberate. Trade: lose Symphony's "self-contained" property,
gain agent containment. If your trust model lets you accept Symphony's
default (trusted-repo, trusted-agent), revert this and you're back to spec.

---

## Direction taken

`SPEC_minimal.md` now adopts a **three-mode approval design** that lets you
trade human friction for autonomy explicitly, without faking safety:

- **`gated`** — current original behavior, mandatory `/symphony approve`.
- **`auto`** — D + C combined: a rules engine (path/label/scope caps) +
  a reviewer agent (different LLM, read-only) + post-impl diff
  verification. Three independent gates; the diff verification is
  prompt-injection-immune because it runs on actual files after the agent
  is done.
- **`handoff`** — Symphony's no-gate default; orchestrator-owned PR push
  remains the final safety boundary.

This satisfies the "less human friction" goal while explicitly rejecting
naive same-agent self-approve (which provides zero safety — the agent
that produced the plan trivially approves it).

Workspace hooks and `codex app-server` were also added back as opt-in
verified features. Multi-turn continuation, per-state concurrency, and
event-inactivity stall remain deferred (lighting up naturally when you
enable `codex.mode: app-server`).

What this spec still does NOT match Symphony on:

- Linear → GitHub (your design choice, not a flaw)
- Self-contained `WORKFLOW.md` → split for A1 (security trade)
- Agent-owned tracker writes → orchestrator-owned PR push (your design)

Those three are deliberate and documented; everything else from Symphony
is either preserved, opt-in, or a clearly labeled deferral.

---

## Specific blog/spec items to ensure are preserved

From the Symphony Service Spec sections that matter most for the blog's
narrative:

- **§3.1 component split into 8 layers** — preserved by my §4 layout, with
  the `agent runner` / `orchestrator` / `tracker client` separation intact.
- **§3.2 abstraction layers** — preserved.
- **§5 WORKFLOW.md as prompt template with strict variable checking** —
  preserved.
- **§7.1 issue orchestration states (claimed/running/retrying/released)** —
  partially preserved as labels; `claimed` not modeled (single-shot).
- **§8.5 active run reconciliation** — preserved by the 19-row reconcile
  table.
- **§9.5 safety invariants** — preserved (cwd check, sanitized key,
  workspace-root prefix).
- **§10.4 emitted runtime events** — partially preserved as audit events;
  finer-grained events (`session_started`, `turn_input_required`) not
  modeled because we don't run app-server.
- **§13 logging conventions, snapshot interface** — partially preserved
  via `slog`; no snapshot endpoint in MVP.

What is **not** preserved that the blog likely highlights:

- Agent doing its own ticket lifecycle (a key Symphony talking point).
- Multi-turn agent sessions per worker run.
- Workspace hooks as the integration seam for repo-specific bootstrap.
- The "scheduler/runner stays minimal, policy stays in repo" framing.

The first one is irreconcilable with your security model. The others are
fixable; the next round of edits to `SPEC_minimal.md` adds two of three.
