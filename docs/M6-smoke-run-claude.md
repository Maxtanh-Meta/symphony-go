# M6 — recorded end-to-end smoke run (claude, subscription mode)

This document records the first successful end-to-end run of symphony-go
against a real GitHub repo using Claude Code in subscription mode. It
closes DoD §16 item 4 ("claude runner completes a planning run on a
real issue").

The runbook procedure is in [`docs/M6-real-runner-smoke.md`](./M6-real-runner-smoke.md);
this file is the authoritative record of one execution of that procedure,
plus the bug-fixes that were needed before it would actually complete.

## Environment

| Field        | Value |
|--------------|-------|
| Date         | 2026-05-04 |
| Repo         | `logosc/symphony-go` (the project's own repo, used as a sandbox) |
| Issue        | #1 — `[stress] add HELLO_STRESS.md` |
| PR opened    | [#2](https://github.com/logosc/symphony-go/pull/2) (draft) |
| Auth         | PAT (token from `gh auth token`) |
| Agent        | claude 2.1.118 (subscription, `~/.claude.json` + macOS Keychain) |
| Approval     | `handoff` |
| Validation   | `[]` (deliberately disabled — see "What this run did NOT exercise") |
| Platform     | macOS 25.3.0, Apple Silicon |

## Issue payload

Title: `[stress] add HELLO_STRESS.md`

Body (verbatim):

> Create exactly one file `HELLO_STRESS.md` at the repo root containing the
> literal text `hello from symphony-go stress test` (no trailing newline,
> no other content). Do not modify any other file.

The issue carried label `symphony:ready` so the orchestrator would claim it.

## Config

Minimal config used for the smoke run (the rest is defaults):

```yaml
repo:
  owner: logosc
  name: symphony-go
  default_branch: main
  local_path: <repo>            # checkout used as sandbox

github:
  auth: pat                     # token resolved via `gh auth token`

agents:
  default: claude               # claude 2.1.118, subscription auth

approval:
  mode: handoff                 # skip the gated approval step

validation:
  commands: []                  # disabled for smoke; see notes below
```

Config lives outside the repo (e.g. `~/.config/symphony-go/config.yaml`)
to avoid the doctor's "config inside repo" guard — see Finding 1 below.

## Run transcript (redacted)

Paths shortened to `<repo>` and tokens to `<REDACTED>`. Lines verbatim
from the orchestrator's structured logger, in order:

```
00:31:39 INFO claim                  issue=1 branch=symphony/issue-1-stress-add-hello_stress.md
00:31:40 INFO worktree_created       issue=1 path=<repo>/.symphony-go/wt/issue-1-…/repo
00:31:40 INFO planning_started       issue=1 axis_key=default axis_source=scalar
00:32:04 INFO planning_completed     issue=1 success=true                 (~24s claude planning)
00:32:04 INFO scope_parsed           issue=1 files=1
00:32:04 INFO auto_approved          issue=1 path=handoff
00:32:06 INFO implementation_started issue=1 axis_key=default
00:32:15 INFO implementation_completed issue=1 success=true turns=1       (~9s, single turn)
00:32:15 INFO validation_completed   issue=1 n=0
00:32:15 INFO committed              issue=1
00:32:16 INFO pushed                 issue=1
00:32:17 INFO pr_created             issue=1 number=2 url=https://github.com/logosc/symphony-go/pull/2
```

Total wall time: **~38s** (claim → PR created).
Of that, planning (`claude -p` cold start + tool-using turn) accounted
for ~24s, and the single implementation turn for ~9s.

## Resulting artifact

- Branch: `symphony/issue-1-stress-add-hello_stress.md`
- Diff: **+1 / -0** (one new file)
- File: `HELLO_STRESS.md` containing `hello from symphony-go stress test`
- Final issue label: `symphony:pr-ready`
- PR: [#2](https://github.com/logosc/symphony-go/pull/2), opened as **draft**
  (the orchestrator never marks PRs ready and never merges)

## Findings — bugs surfaced and fixed during the smoke run

These are the real value of running a smoke test: four bugs that no
unit or integration test had caught, each traced to a thin layer
between the orchestrator and the host OS.

### 1. Doctor false-positive when `SYMPHONY_GO_CONFIG` is unset

`internal/orchestrator/doctor.go` called `filepath.Abs("")` to resolve
the configured config-file path. On Unix, `filepath.Abs("")` does **not**
return `""` — it returns the process CWD. When `SYMPHONY_GO_CONFIG` was
unset (the common case for users who pass `--config` or rely on default
search paths), the doctor wrongly compared CWD against `repo.local_path`
and emitted a false-positive "config inside repo" error, refusing to
start.

**Fix:** explicitly look up the env var before invoking `filepath.Abs`,
and skip the comparison entirely when the env var is empty. The
"config inside repo" guard now triggers only when the path was actually
provided.

### 2. Subscription auth seeded directories but not `~/.claude.json`

`internal/orchestrator/auth.go::seedSubscriptionAuth` symlinked the
authenticated user's Claude state into the agent's isolated HOME — but
the helper iterated over candidates with an `info.IsDir()` filter and
only included directory entries (`~/.claude/`, etc.). The 86 KB
`~/.claude.json` file (which holds session bookkeeping and the
subscription token reference) was silently skipped.

Symptom: every `claude -p` invocation under the orchestrator returned
`Not logged in · Please run /login`, even though the user had a perfectly
good subscription session in their real HOME.

**Fix:** drop the `info.IsDir()` filter and extend the path list so it
covers both files and directories. `~/.claude.json` is now symlinked
alongside `~/.claude/`.

### 3. `BuildAgentEnv` stripped POSIX baseline vars

`internal/exec/exec.go::BuildAgentEnv` was deliberately aggressive about
scrubbing the agent's environment to avoid leaking `GITHUB_TOKEN`,
`SSH_AUTH_SOCK`, etc. Unfortunately it also stripped `PATH`, `USER`,
`LOGNAME`, `LANG`, `LC_*`, and `TERM`. Two consequences:

- Child processes (the claude binary and anything it shelled out to)
  lost `PATH` and could not find their own helpers (`node`, `git`,
  `security`, …).
- macOS Keychain refused to authenticate the user without `USER` and
  `LOGNAME` set; Keychain access is keyed by the resolved user identity,
  and an empty identity means an unconditional denial.

**Fix:** added a `baselineEnvNames` allowlist of POSIX/identity vars
that always pass through. They are still subject to `block_patterns`
and to the always-drop list, so the existing protection against
`GITHUB_TOKEN` / `SSH_AUTH_SOCK` / friends is unchanged.

### 4. Keychain unreachable from isolated HOME on macOS

macOS Keychain reads `~/Library/Keychains/login.keychain-db` directly
from the user's home directory. With the orchestrator's isolated HOME
override, that file was hidden and
`security find-generic-password "Claude Code-credentials"` failed with
no matching entry — even after Finding 2 was fixed.

**Fix:** added `Library/Keychains`, `Library/Application Support/Claude`,
and `Library/Caches/claude-cli-nodejs` to the symlink list in
`seedSubscriptionAuth`.

**Security trade-off, documented:** symlinking `Library/Keychains`
gives the agent process access to the **entire user Login Keychain**,
not just the `Claude Code-credentials` item. Operators who care about
that blast radius should use API-key mode (`agents.default.auth: api_key`)
instead of subscription mode; the API-key path does not touch Keychain
at all.

## Reproduce

A reader with the same setup (Claude Code installed and logged-in,
`gh` authenticated, the symphony-go repo cloned) can reproduce this run:

```sh
# 1. Build and install the orchestrator.
go install ./cmd/symphony-go

# 2. Make sure the required labels exist on the sandbox repo.
for L in symphony:ready symphony:in-progress symphony:pr-ready \
         symphony:needs-input symphony:blocked; do
  gh label create "$L" --repo logosc/symphony-go --force
done

# 3. Drop the minimal config shown above into ~/.config/symphony-go/config.yaml
#    (NOT inside the repo — see Finding 1).

# 4. Create the smoke issue.
gh issue create --repo logosc/symphony-go \
  --title '[stress] add HELLO_STRESS.md' \
  --label symphony:ready \
  --body 'Create exactly one file HELLO_STRESS.md at the repo root containing the literal text "hello from symphony-go stress test" (no trailing newline, no other content). Do not modify any other file.'

# 5. Run a single iteration against that issue.
symphony-go run --once --config ~/.config/symphony-go/config.yaml
```

Expected log: the eleven lines reproduced under "Run transcript" above,
within ~40s on a warm machine. Expected PR: one new file, +1/-0,
draft, label `symphony:pr-ready` on the issue.

## What this run did NOT exercise

- **Real codex runner** — separate transcript needed; codex auth follows
  a different code path and has not been smoke-tested end-to-end.
- **`gated` approval mode** — covered by integration tests; no real-runner
  smoke yet.
- **`auto` mode rules + reviewer** — covered by integration tests; no
  real-runner smoke yet.
- **Multi-turn continuation** — this run completed in a single
  implementation turn. Resume / continuation behavior is covered by
  integration tests but not by a real-runner smoke.
- **App-installation auth** (`github.auth: app`) — covered by
  [`docs/github-app-setup.md`](./github-app-setup.md) but not smoke-run yet.
- **Validation commands** — deliberately disabled (`validation.commands: []`)
  for this run because of an unrelated test-pollution issue in the
  orchestrator package that would have made `go test ./...` fail in the
  worktree. Restoring this is tracked separately.
