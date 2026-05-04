#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/onboard-smoke.sh [options]

Create a tiny smoke issue for an onboarded repo and optionally run
symphony-go once against it.

Options:
  --repo OWNER/REPO        GitHub repo
  --config PATH            Config path (default: ~/.symphony-go/config.yml)
  --env-file PATH          Env file path (default: ~/.symphony-go/.env)
  --local-path PATH        Local checkout path; used only for default title/body
  --title TITLE            Issue title
  --body BODY              Issue body
  --label LABEL            Extra label; may be repeated (default: type:code)
  --run-once               Run symphony-go once after creating the issue
  --no-create              Do not create issue; only run/status
  --status                 Print symphony-go status after run
  -h, --help               Show this help

Environment:
  SYMPHONY_GO_BIN          Binary path (default: ~/go/bin/symphony-go,
                           falling back to symphony-go in PATH)
USAGE
}

die() {
  echo "error: $*" >&2
  exit 1
}

ask() {
  local prompt="$1"
  local default="${2:-}"
  local answer
  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " answer
    printf '%s\n' "${answer:-$default}"
  else
    read -r -p "$prompt: " answer
    [[ -n "$answer" ]] || die "$prompt is required"
    printf '%s\n' "$answer"
  fi
}

repo=""
config_path="${HOME}/.symphony-go/config.yml"
env_file="${HOME}/.symphony-go/.env"
local_path="${PWD}"
title=""
body=""
labels=("type:code")
run_once="false"
create_issue="true"
print_status="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo="${2:-}"; shift 2 ;;
    --config) config_path="${2:-}"; shift 2 ;;
    --env-file) env_file="${2:-}"; shift 2 ;;
    --local-path) local_path="${2:-}"; shift 2 ;;
    --title) title="${2:-}"; shift 2 ;;
    --body) body="${2:-}"; shift 2 ;;
    --label) labels+=("${2:-}"); shift 2 ;;
    --run-once) run_once="true"; shift ;;
    --no-create) create_issue="false"; shift ;;
    --status) print_status="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

if [[ -z "$repo" ]]; then
  repo="$(ask "GitHub repo (OWNER/REPO)")"
fi
if [[ "$repo" != */* ]]; then
  die "--repo must look like OWNER/REPO"
fi

if [[ -z "$title" ]]; then
  title="T-smoke: symphony-go onboarding"
fi
if [[ -z "$body" ]]; then
  body="Create docs/symphony-go-onboarding-smoke.md containing one short sentence that says this repo has passed the symphony-go onboarding smoke. Do not touch any other file."
fi

bin="${SYMPHONY_GO_BIN:-}"
if [[ -z "$bin" ]]; then
  if [[ -x "$HOME/go/bin/symphony-go" ]]; then
    bin="$HOME/go/bin/symphony-go"
  else
    bin="$(command -v symphony-go || true)"
  fi
fi
[[ -n "$bin" ]] || die "symphony-go binary not found"

if [[ "$create_issue" == "true" ]]; then
  command -v gh >/dev/null 2>&1 || die "gh is required to create the smoke issue"
  args=(issue create --repo "$repo" --title "$title" --body "$body" --label "symphony:ready")
  for label in "${labels[@]}"; do
    [[ -n "$label" ]] && args+=(--label "$label")
  done
  echo "Creating smoke issue in $repo..."
  gh "${args[@]}"
fi

if [[ "$run_once" == "true" ]]; then
  [[ -f "$env_file" ]] || die "missing env file $env_file"
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a
  echo "Running symphony-go once..."
  "$bin" run --config "$config_path" --once
fi

if [[ "$print_status" == "true" ]]; then
  [[ -f "$env_file" ]] && {
    set -a
    # shellcheck disable=SC1090
    source "$env_file"
    set +a
  }
  "$bin" status --config "$config_path"
fi

cat <<EOF

Smoke expectations:
  - In handoff/auto success: issue reaches symphony:pr-ready and a draft PR opens.
  - In gated mode: issue reaches symphony:awaiting-approval.
  - If approval.require_token is true, approve by commenting the exact token
    shown under the plan comment's "## Approval" footer.
  - If approval.require_token is false, approve by commenting the configured
    approval.command, usually /symphony approve.

Useful commands:
  set -a; source '$env_file'; set +a
  $bin status --config '$config_path'
  $bin run --config '$config_path'
EOF
