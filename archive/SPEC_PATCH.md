# SPEC.md patch — A1 (config out of repo) + A4 (reconcile table)

This patch supersedes the corresponding sections of the original SPEC. Apply
in order; sections not mentioned are unchanged.

---

## Patch 1 — Section 2, principle 3

Was:

> 3. WORKFLOW.md defines repo-specific agent policy.

Becomes:

> 3. Configuration lives in `~/.minisymphony/config.yml`, outside any repo
>    workspace. The agent subprocess never reads it and cannot edit it during
>    a run. `WORKFLOW.md` lives in the repo and contains only the prompt
>    template.

Rationale: in the original design, `WORKFLOW.md` carries both config (env
allowlist, validation commands, approval users) and prompt, and lives in the
repo workspace where the agent has Edit/Write access. The agent could
self-promote by editing its own permission set during the first task. Splitting
the two files removes the attack.

---

## Patch 2 — Replaces Section 5 in full

`WORKFLOW.md` is plain markdown with substitution bindings, no YAML front
matter:

```
{{ issue.title }}
{{ issue.description }}
{{ issue.url }}
{{ issue.number }}
{{ issue.labels }}
```

Mode-specific suffixes (planning / implementation) are appended by the
orchestrator, not the user. The user does not write the suffixes.

Configuration moves to `~/.minisymphony/config.yml` with the same field set as
the old front matter, minus the prompt body, plus one new field:

```yaml
repo:
  full_name: "OWNER/REPO"
  base_branch: "main"
  local_path: "/abs/path/to/repo"
  workflow_file: "WORKFLOW.md"   # relative to local_path; default "WORKFLOW.md"

# ... github / labels / approval / agent / claude / codex / env /
#     validation / pull_request / audit blocks unchanged from original §5 ...
```

CLI:

```
minisymphony run    --config ~/.minisymphony/config.yml
minisymphony run    --once --config ~/.minisymphony/config.yml
minisymphony doctor --config ~/.minisymphony/config.yml
```

If `--config` is omitted, search in order:

1. `$MINISYMPHONY_CONFIG`
2. `$XDG_CONFIG_HOME/minisymphony/config.yml`
3. `~/.minisymphony/config.yml`

Hard-fail if none exist.

---

## Patch 3 — New Section 5a (Config integrity guard)

The orchestrator MUST:

1. Resolve `config.yml` to its absolute path at startup, `stat` it, compute
   SHA-256, and store both.
2. On every poll tick, re-stat and re-hash. If the hash changed and any job
   is in `planning`, `awaiting_approval`, or `implementing`: refuse to apply
   the new config until those jobs reach a terminal state. Log a warning on
   every tick until then.
3. Refuse to start the orchestrator if the resolved `config.yml` path is
   under any `repo.local_path`. Catches the case where someone moves the
   config back into a repo. Hard fail at startup, also checked by `doctor`.
4. Before commit, scan the staged diff for any change to the path resolved
   from `repo.workflow_file`. If present, do not block the commit, but post
   an issue comment: `[minisymphony] agent modified WORKFLOW.md; review
   carefully before merge`. The PR body must contain the same notice in a
   `## Warnings` section.
5. Pass neither the config path nor any config field as an env var or CLI arg
   to the agent subprocess. Only orchestrator-process code reads `config.yml`.

---

## Patch 4 — Replaces Section 8, last paragraph (reconcile prose)

State reconciliation runs at startup, after acquiring the lock and before any
dispatch. Inputs:

- every open issue in the repo carrying at least one `symphony:*` label
  (one paginated issues-search call)
- every file under `.minisymphony/state/jobs/`

For each (issue, local-state) pair, apply exactly one row of the table below.
The match is on the tuple `(local.status, github.symphony_label, issue.state)`.
The table is exhaustive — if no row matches, that is a bug, not "ambiguous";
fail loud.

| #  | local                | github label             | issue  | action |
|----|----------------------|--------------------------|--------|--------|
| 1  | missing              | `ready`                  | open   | normal: enter dispatch queue |
| 2  | missing              | `planning`               | open   | replace label with `blocked`, comment "orphan planning label, no local state" |
| 3  | missing              | `awaiting-approval`      | open   | replace with `blocked`, comment "orphan awaiting-approval label, no local state" |
| 4  | missing              | `implementing`           | open   | replace with `blocked`, comment "orphan implementing label; no local state, workspace not preserved" |
| 5  | missing              | `pr-ready`               | open   | leave alone (terminal, owned by humans) |
| 6  | missing              | `failed` or `blocked`    | open   | leave alone |
| 7  | `planning`           | `planning`               | open   | re-run planning from scratch. If `plan_comment_id` is set, edit that comment in place rather than posting a new one |
| 8  | `planning`           | anything else            | open   | mark blocked locally, replace github label with `blocked`, comment "label drift: local=planning, github=`<label>`" |
| 9  | `awaiting_approval`  | `awaiting-approval`      | open   | resume: poll comments for approval |
| 10 | `awaiting_approval`  | `implementing`           | open   | crash mid-transition. If `approval_comment_id` is set in local state: resume implementation. Else: mark blocked, comment "transition observed but no approval recorded" |
| 11 | `awaiting_approval`  | anything else            | open   | label drift, blocked |
| 12 | `implementing`       | `implementing`           | open   | DO NOT auto-resume. Mark blocked. Replace label with `blocked`. Comment "interrupted mid-implementation; workspace preserved at `<path>`; remove this label and add `symphony:ready` to retry from scratch" |
| 13 | `implementing`       | `pr-ready`               | open   | crash after PR creation but before state save. Search PRs via `GET /repos/{owner}/{repo}/pulls?head={owner}:{branch}&state=open`. If exactly one match, save its `number` to local state and mark complete. If zero or multiple matches, mark blocked. |
| 14 | `implementing`       | `failed`                 | open   | crash during failure handling. Leave `failed`, do not retry |
| 15 | `pr_ready`           | `pr-ready`               | open   | terminal, leave |
| 16 | `pr_ready`           | anything else            | open   | someone manually relabeled. Leave local as is, do not touch github |
| 17 | any                  | (no `symphony:*` label)  | open   | mark local `blocked`, do not relabel github (humans removed labels intentionally) |
| 18 | any non-terminal     | any                      | closed | mark local complete, kill any running job, leave workspace and labels |
| 19 | terminal             | any                      | closed | mark local complete, leave |

Rules that apply to every row:

- Reconciliation never starts an agent. New planning runs come from the
  dispatch loop, which runs only after reconcile completes.
- Reconciliation never deletes a workspace. Workspace cleanup is a separate
  command (out of scope for MVP).
- Comments posted by reconcile are prefixed `[minisymphony reconcile]` and
  capped at 1000 chars.
- If a row's action calls a GitHub API and that call fails: log it, leave the
  job blocked locally, continue to the next row. Reconciliation must process
  every row even if some GitHub calls fail. Do not exit the process.
- After processing all rows, log a one-line summary: `reconcile: N rows
  processed, K transitioned, M errors`.

---

## Patch 5 — Replaces Section 24 ("Doctor command") items 1–3

Was:

> 1. WORKFLOW.md exists and parses.
> 2. GitHub token env exists.
> 3. GitHub repo is accessible.

Becomes:

> 1. `<resolved config path>` exists, parses, and validates.
> 2. `<resolved config path>` is NOT under any `repo.local_path`. Hard fail
>    if it is.
> 3. `<repo.local_path>/<repo.workflow_file>` exists and renders against an
>    empty issue without panicking.
> 4. GitHub token env var exists and is non-empty.
> 5. `GET /repos/{full_name}` returns 200.
> 6. The token has write access — heuristic:
>    `GET /repos/{full_name}/collaborators/{authenticated_user}/permission`
>    returns `admin`, `maintain`, or `write`.

Remaining doctor checks (worktree, agent binary, validation commands, etc.)
are unchanged.
