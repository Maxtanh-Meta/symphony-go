# GitHub App authentication setup

symphony-go supports two GitHub auth modes selected by `github.auth` in
`~/.symphony-go/config.yml`:

- **`pat`** (default) — long-lived personal access token. Simple, but
  the token survives forever and is scoped to the user.
- **`app`** — GitHub App installation. Tokens are short-lived (~1 h),
  rotate automatically, and are scoped to the repos where the App is
  installed. The orchestrator's GitHub-side identity is the bot
  (`<app-name>[bot]`), giving cleaner audit lines.

This walkthrough sets up a private GitHub App, installs it on one repo,
and configures symphony-go to authenticate as that installation.

## 1. Register the App

1. https://github.com/settings/apps → **New GitHub App**.
2. **Name:** anything; the bot login is derived from this. e.g.
   `symphony-go-myorg`.
3. **Homepage URL:** anything (e.g. your repo URL).
4. **Webhook:** uncheck "Active". symphony-go polls; webhooks are not
   needed.
5. **Repository permissions** (the minimum set):

   | Permission | Access | Why |
   |---|---|---|
   | Contents | Read & write | push branches |
   | Issues | Read & write | label transitions, comments, reactions |
   | Pull requests | Read & write | create draft PRs |
   | Metadata | Read-only | required for any App |

   **Account permissions:** none.
6. **Where can this GitHub App be installed?** "Only on this account"
   for personal use; "Any account" only if you intend to share.
7. Click **Create GitHub App**.

## 2. Generate a private key

On the App's settings page, scroll to **Private keys** → **Generate a
private key**. The browser downloads `<app-name>.<date>.private-key.pem`.

```sh
mkdir -p ~/.symphony-go
mv ~/Downloads/<app-name>.*.private-key.pem ~/.symphony-go/github-app.pem
chmod 600 ~/.symphony-go/github-app.pem
```

The `chmod 600` matters — `symphony-go doctor` warns if the file is
group- or world-readable.

## 3. Note the App ID and install on your repo

Top of the App's settings page:

- **App ID:** a 6–7 digit number. Copy it.

Then **Install App** in the left nav → pick the account, → **Only
select repositories** → choose the sandbox repo you want symphony-go
to drive. After install, the URL ends in `…/installations/<N>`. Copy
that `<N>` — that's your installation ID.

## 4. Edit `~/.symphony-go/config.yml`

Two equivalent ways to wire credentials. Pick whichever fits your
environment.

### Option A — inline (recommended for single-machine ops)

```yaml
github:
  auth: "app"
  app_id: 3587670                         # from §3 (publicly visible — not a secret)
  installation_id: 129186370              # from §3 (publicly visible — not a secret)
  private_key_path: "/Users/you/.symphony-go/github-app.pem"
  poll_interval_seconds: 30
```

No env vars needed. Make sure `~/.symphony-go/config.yml` itself is
`chmod 600` if it sits next to the `.pem`. The actual secret is the
`.pem` file; the path and ID numbers are not.

### Option B — env-var indirection (recommended for shared infra / CI)

```yaml
github:
  auth: "app"
  app_id_env:            "SYMPHONY_GO_APP_ID"
  installation_id_env:   "SYMPHONY_GO_APP_INSTALLATION_ID"
  private_key_path_env:  "SYMPHONY_GO_APP_PRIVATE_KEY_PATH"
  poll_interval_seconds: 30
```

The fields are env-var **names**, not values — symphony-go reads the
named env at startup. Useful when the config file is checked in or
shared, or when secrets come from a secrets-manager.

```sh
# In your shell profile, or a service EnvironmentFile:
export SYMPHONY_GO_APP_ID="3587670"
export SYMPHONY_GO_APP_INSTALLATION_ID="129186370"
export SYMPHONY_GO_APP_PRIVATE_KEY_PATH="$HOME/.symphony-go/github-app.pem"
```

### Option C — inline PEM (for filesystem-less environments)

If you have no usable filesystem (Cloudflare Workers, some container
images), embed the PEM directly:

```yaml
github:
  auth: "app"
  app_id: 3587670
  installation_id: 129186370
  private_key_pem: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
```

Or via env: `private_key_pem_env: "SYMPHONY_GO_APP_PRIVATE_KEY_PEM"`.

### Mutual exclusion

Within each pair, set exactly one — `symphony-go doctor` rejects
configs that set both (e.g. `app_id` and `app_id_env`). Across the four
PEM sources (`private_key_path` / `private_key_path_env` /
`private_key_pem` / `private_key_pem_env`), exactly one must be set.

You can mix-and-match across pairs: e.g. inline `app_id` with env-named
`private_key_path_env` is fine — useful when the App ID is fixed but
the PEM path differs by environment.

## 5. Verify

```sh
symphony-go doctor --config ~/.symphony-go/config.yml
```

Expected log output near the top:

```
INFO doctor: github auth resolved summary="github auth: app (app_id=3587670, installation_id=129186370)"
ok
```

If you see `ok` and no `ERROR`/`WARN` lines, you're set. Common
failures:

| Doctor message | Likely cause |
|---|---|
| `github.app_id_env "X" is empty` | Env var not exported in this shell. |
| `github.app_id_env "X" must parse as int64` | Value contains spaces or non-digits. |
| `read /path/to/.pem: no such file or directory` | `chmod` it back to readable; check the path env. |
| `github app pem file is group/world-readable; recommend chmod 600` | Just a warning; tighten with `chmod 600`. |
| `list ready issues (is App installed on owner/repo?): 404` | The App is created but not installed on this specific repo. |
| `ghinstallation token request failed: 401 Bad credentials` | App ID and PEM don't match (different App), or the PEM was regenerated. |

## 6. Run

```sh
symphony-go run --once --config ~/.symphony-go/config.yml
```

The orchestrator now authenticates as the App. Issues, comments,
labels, and PR creation appear under the bot login (`<app-name>[bot]`).
The `approval.ignored_users` default already includes
`<app-name>[bot]` patterns to prevent the bot from being treated as a
human approver if a prompt-injected issue body echoes
`/symphony approve`.

## Token rotation

`ghinstallation` (the library used internally) mints fresh installation
tokens on demand and refreshes them before they expire. There is no
symphony-go-side caching. A long-running `symphony-go run` survives the
1-hour token TTL automatically.

For `git push`, the orchestrator mints a fresh installation token and
sends it via the per-command HTTPS `extraheader` — the token never
touches disk, the agent subprocess never sees it, and there is no
persistent credential helper.

## Switching back to PAT

Inline (single-machine):

```yaml
github:
  auth: "pat"
  token: "ghp_yourPAThere"
  poll_interval_seconds: 30
```

Or env-var indirection (CI / shared infra):

```yaml
github:
  auth: "pat"           # or omit; "" defaults to "pat"
  token_env: "GITHUB_TOKEN"
  poll_interval_seconds: 30
```

`token` and `token_env` are mutually exclusive. Existing PAT users do
nothing — the schema is fully backward-compatible.
