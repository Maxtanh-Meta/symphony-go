# Security policy

`symphony-go` (`github.com/logosc/symphony-go`) is a security-focused
agent orchestrator. It runs untrusted coding agents against GitHub
issues, so the threat model is central to the project's design. Please
read this document before reporting an issue or operating an instance
in production.

## Supported versions

- `main` branch only. The project is pre-1.0 and has no formal
  version cadence yet. Fixes land on `main`; operators are expected to
  track it.

## Reporting a vulnerability

Use GitHub's **Private Vulnerability Reporting** form:

> https://github.com/logosc/symphony-go/security/advisories/new

Or navigate via the repo's *Security* tab → *Report a vulnerability*.
This is a private channel — only repository maintainers see it. The
filed report becomes a draft Security Advisory, which we use to
coordinate the fix and (when ready) publish a CVE.

- **First response:** within 5 business days.
- **Embargo:** 30 days from first response, extensible by mutual
  agreement if a fix needs more time or coordinated disclosure.

Please do not open a **public** issue for suspected vulnerabilities —
the private form above is the only supported channel for embargoed
reports. Public issues are fine for hardening suggestions, questions
about the threat model, or anything that does not require embargo.

## Scope (in scope)

- **Agent-injection paths.** Any way a hostile GitHub-issue body,
  comment, or linked artifact can make the orchestrator do something
  it shouldn't: push to the wrong branch, post comments under the
  operator's identity, escape the per-issue worktree, or exfiltrate
  credentials.
- **Sandbox escape.** The agent subprocess reading or writing
  anything outside its isolated `$HOME` or its per-issue worktree.
  Note: subscription mode on macOS deliberately exposes
  `~/Library/Keychains` via symlink — see *Known trade-offs* below.
- **Credential leaks.** `GITHUB_TOKEN`, OAuth tokens, or App private
  keys appearing in committed PR bodies, audit logs (after
  redaction), or in subprocess environments beyond the documented
  allowlist.
- **Approval-bypass.** Any way to make `auto` approval mode skip the
  rules engine, the reviewer agent, or the post-implementation diff
  verification — these are the three independent gates described in
  SPEC §10.
- **Reconcile-table corruption.** Any sequence that leaves the
  orchestrator in a state inconsistent with the SPEC §7 19-row
  reconcile table after restart (lost work, double-pushes,
  half-applied tracker writes).
- **Config tampering.** Any way to make the agent edit its own
  permission set, validation commands, or approval rules during a
  run. This is what the SPEC §2 config integrity guard exists to
  prevent.

## Out of scope

- **The local-trusted-user trust model.** `symphony-go` is documented
  as not multi-tenant. We assume the operator running it is trusted
  on their own laptop or on a single-tenant server.
- **Compromise of the upstream coding agents** (Claude Code, Codex,
  etc.). Report those to Anthropic or OpenAI directly.
- **Compromise of the operator's GitHub account, PAT, or App
  credentials** by means outside `symphony-go` (phishing, leaked
  dotfiles, etc.).
- **Resource exhaustion on the operator's own machine** — filling
  disk with worktrees, exhausting LLM tokens, etc. These are handled
  by the operator via quotas and monitoring.

## Hardening checklist for operators

- Prefer **App-installation auth** (`auth: app`) over PATs when
  possible. Short-lived rotated tokens with per-repo install scope
  are strictly safer than long-lived user PATs. See
  `docs/github-app-setup.md`.
- Run `auto` approval mode with `verify_diff_matches_plan: true`,
  and use a reviewer model **different** from the agent model so a
  single jailbreak does not pass both gates.
- Keep `validation.commands` non-empty so post-implementation tests
  catch broken patches before any PR is opened.
- Add `audit.redact_patterns` for any project-specific secrets that
  could leak into agent stderr (internal hostnames, customer IDs,
  vendor tokens, etc.).
- Review `cfg.Approval.IgnoredUsers` if you ever switch from PAT
  (which acts as your own account) to App (which acts as
  `<app-name>[bot]`), so the bot cannot self-approve its own PRs.

## Known security-relevant trade-offs

- **Subscription auth on macOS.**
  `internal/orchestrator/auth.go::seedSubscriptionAuth` symlinks
  `~/Library/Keychains/` into the agent's isolated `$HOME`. This
  gives the agent subprocess read access to the *entire* user Login
  Keychain — browser passwords, ssh-agent identities, third-party
  app secrets — not just the Claude Code credential it actually
  needs. This is documented inline in `auth.go`. Operators concerned
  about this trade-off should use API-key mode with
  `env.allowlist=["ANTHROPIC_API_KEY"]` instead, which keeps the
  agent's `$HOME` fully isolated.
- **`WORKFLOW.md` is in-repo and human-editable.** An attacker who
  can land a PR modifying `WORKFLOW.md` can change agent behavior on
  *future* runs after the merge. The config integrity guard
  (SPEC §2.5) catches mid-run edits made by the agent itself, but
  not pre-run edits introduced through a normal merge. Treat
  `WORKFLOW.md` as security-sensitive and require human review on
  changes to it.
- **The orchestrator never auto-merges.** All PRs are opened as
  draft; a human must mark them ready and merge. This is the final
  safety net and applies regardless of how the in-flight gates
  (rules engine, reviewer agent, diff verification) are configured.

## Acknowledgements

We credit reporters in the changelog when a fix lands, with their
permission. Anonymous reports are welcome.
