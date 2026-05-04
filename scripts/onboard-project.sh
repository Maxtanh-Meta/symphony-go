#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/onboard-project.sh [options]

Guided setup for a new symphony-go target repo. The script can:
  - create the standard symphony labels
  - copy WORKFLOW.md into the target repo
  - write an external config file
  - write an env template for GitHub auth
  - optionally run symphony-go doctor

Options:
  --repo OWNER/REPO              GitHub repo to onboard
  --local-path PATH              Local checkout path for the repo
  --config PATH                  Config path (default: ~/.symphony-go/config.yml)
  --env-file PATH                Env file path (default: ~/.symphony-go/.env)
  --auth pat|app                 GitHub auth mode (default: app)
  --approval handoff|gated|auto  Initial approval mode (default: handoff)
  --require-token true|false     Use per-plan numeric approval token (default: false)
  --agent claude|codex           Agent provider (default: claude)
  --model MODEL                  Agent model (default: sonnet)
  --validation COMMAND           Validation command (default: go test ./...)
  --skip-labels                  Do not create labels with gh
  --skip-workflow                Do not copy WORKFLOW.md
  --run-doctor                   Run symphony-go doctor at the end
  -h, --help                     Show this help

Environment:
  SYMPHONY_GO_BIN                Binary path (default: ~/go/bin/symphony-go,
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

bool_arg() {
  case "$1" in
    true|false) printf '%s\n' "$1" ;;
    *) die "expected true or false, got $1" ;;
  esac
}

repo=""
local_path=""
config_path="${HOME}/.symphony-go/config.yml"
env_file="${HOME}/.symphony-go/.env"
auth="app"
approval="handoff"
require_token="false"
agent="claude"
model="sonnet"
validation="go test ./..."
create_labels="true"
copy_workflow="true"
run_doctor="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo="${2:-}"; shift 2 ;;
    --local-path) local_path="${2:-}"; shift 2 ;;
    --config) config_path="${2:-}"; shift 2 ;;
    --env-file) env_file="${2:-}"; shift 2 ;;
    --auth) auth="${2:-}"; shift 2 ;;
    --approval) approval="${2:-}"; shift 2 ;;
    --require-token) require_token="$(bool_arg "${2:-}")"; shift 2 ;;
    --agent) agent="${2:-}"; shift 2 ;;
    --model) model="${2:-}"; shift 2 ;;
    --validation) validation="${2:-}"; shift 2 ;;
    --skip-labels) create_labels="false"; shift ;;
    --skip-workflow) copy_workflow="false"; shift ;;
    --run-doctor) run_doctor="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$auth" in pat|app) ;; *) die "--auth must be pat or app" ;; esac
case "$approval" in handoff|gated|auto) ;; *) die "--approval must be handoff, gated, or auto" ;; esac
case "$agent" in claude|codex) ;; *) die "--agent must be claude or codex" ;; esac

if [[ -z "$repo" ]]; then
  repo="$(ask "GitHub repo (OWNER/REPO)")"
fi
if [[ "$repo" != */* ]]; then
  die "--repo must look like OWNER/REPO"
fi

if [[ -z "$local_path" ]]; then
  local_path="$(ask "Local checkout path" "${PWD}")"
fi
mkdir -p "$local_path"
local_path="$(cd "$local_path" && pwd)"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
workflow_src="$repo_root/testdata/WORKFLOW.example.md"

mkdir -p "$(dirname "$config_path")" "$(dirname "$env_file")"

if [[ "$create_labels" == "true" ]]; then
  command -v gh >/dev/null 2>&1 || die "gh is required for label creation; rerun with --skip-labels"
  echo "Creating symphony labels in $repo..."
  for label in ready planning awaiting-approval implementing pr-ready failed blocked stop; do
    gh -R "$repo" label create "symphony:$label" --force >/dev/null
  done
  for label in type:code type:research type:deploy safe-change docs test; do
    gh -R "$repo" label create "$label" --force >/dev/null
  done
fi

if [[ "$copy_workflow" == "true" ]]; then
  [[ -f "$workflow_src" ]] || die "missing $workflow_src"
  if [[ -e "$local_path/WORKFLOW.md" ]]; then
    echo "WORKFLOW.md already exists in $local_path; leaving it unchanged."
  else
    cp "$workflow_src" "$local_path/WORKFLOW.md"
    echo "Copied WORKFLOW.md into $local_path."
  fi
fi

echo "Writing config to $config_path..."
if [[ "$auth" == "app" ]]; then
  cat > "$config_path" <<EOF
repo:
  full_name: "$repo"
  base_branch: "main"
  local_path: "$local_path"
  workflow_file: "WORKFLOW.md"

github:
  auth: "app"
  app_id_env: "GITHUB_APP_ID"
  installation_id_env: "GITHUB_APP_INSTALLATION_ID"
  private_key_path_env: "GITHUB_APP_PRIVATE_KEY_PATH"
  poll_interval_seconds: 30

labels:
  ready: "symphony:ready"
  planning: "symphony:planning"
  awaiting_approval: "symphony:awaiting-approval"
  implementing: "symphony:implementing"
  pr_ready: "symphony:pr-ready"
  failed: "symphony:failed"
  blocked: "symphony:blocked"
  stop: "symphony:stop"

approval:
  mode: "$approval"
  command: "/symphony approve"
  require_token: $require_token
  require_write_permission: true
  ignored_users:
    - "symphony-go[bot]"
    - "github-actions[bot]"

agent:
  provider: "$agent"
  model: "$model"
  timeout_seconds: 3600

validation:
  commands:
    - "$validation"
  command_timeout_seconds: 900
EOF
else
  cat > "$config_path" <<EOF
repo:
  full_name: "$repo"
  base_branch: "main"
  local_path: "$local_path"
  workflow_file: "WORKFLOW.md"

github:
  auth: "pat"
  token_env: "GITHUB_TOKEN"
  poll_interval_seconds: 30

labels:
  ready: "symphony:ready"
  planning: "symphony:planning"
  awaiting_approval: "symphony:awaiting-approval"
  implementing: "symphony:implementing"
  pr_ready: "symphony:pr-ready"
  failed: "symphony:failed"
  blocked: "symphony:blocked"
  stop: "symphony:stop"

approval:
  mode: "$approval"
  command: "/symphony approve"
  require_token: $require_token
  require_write_permission: true
  ignored_users:
    - "symphony-go[bot]"
    - "github-actions[bot]"

agent:
  provider: "$agent"
  model: "$model"
  timeout_seconds: 3600

validation:
  commands:
    - "$validation"
  command_timeout_seconds: 900
EOF
fi
chmod 600 "$config_path"

if [[ -e "$env_file" ]]; then
  echo "Env file $env_file already exists; leaving it unchanged."
else
  echo "Writing env template to $env_file..."
  if [[ "$auth" == "app" ]]; then
    cat > "$env_file" <<'EOF'
# Fill these with the GitHub App values from GitHub settings.
GITHUB_APP_ID=
GITHUB_APP_INSTALLATION_ID=
GITHUB_APP_PRIVATE_KEY_PATH=$HOME/.symphony-go/github-app.pem
EOF
  else
    cat > "$env_file" <<'EOF'
# Fill with a GitHub token that can read/write issues, contents, and PRs.
GITHUB_TOKEN=
EOF
  fi
  chmod 600 "$env_file"
fi

echo
echo "Next steps:"
echo "  1. Edit $env_file and fill credential values."
echo "  2. Commit WORKFLOW.md in $local_path if it was newly copied."
echo "  3. Run:"
echo "     set -a; source '$env_file'; set +a"
echo "     ${SYMPHONY_GO_BIN:-~/go/bin/symphony-go} doctor --config '$config_path'"
echo "  4. Create a smoke issue:"
echo "     scripts/onboard-smoke.sh --repo '$repo' --config '$config_path' --local-path '$local_path'"

if [[ "$run_doctor" == "true" ]]; then
  bin="${SYMPHONY_GO_BIN:-}"
  if [[ -z "$bin" ]]; then
    if [[ -x "$HOME/go/bin/symphony-go" ]]; then
      bin="$HOME/go/bin/symphony-go"
    else
      bin="$(command -v symphony-go || true)"
    fi
  fi
  [[ -n "$bin" ]] || die "symphony-go binary not found"
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a
  "$bin" doctor --config "$config_path"
fi
