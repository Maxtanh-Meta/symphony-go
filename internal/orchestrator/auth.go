package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
)

// subscriptionAuthPaths is the list of HOME-relative paths (directories
// or files) that claude/codex CLIs typically use to store authenticated
// subscription state. Each one that exists in the user's real HOME is
// symlinked into the agent's isolated HOME so the subprocess can read
// its existing auth without us copying credentials.
//
// Notable items:
//   - `.claude.json` is a *file* (not a directory) holding the session/
//     refresh token bookkeeping; `.claude/` is a sibling directory
//     holding projects, memory, etc. Both are needed for headless
//     `claude -p` to recognize the user as logged in.
//   - On macOS, claude/codex store their OAuth tokens in the user's
//     Login Keychain (queried via Security framework), which lives at
//     `~/Library/Keychains/login.keychain-db`. Without that symlink,
//     `security find-generic-password "Claude Code-credentials"` fails
//     and the agent reports `Not logged in · Please run /login`.
//
// SECURITY NOTE — Keychain trade-off: symlinking `Library/Keychains`
// gives the agent subprocess access to *every* item in the user's Login
// Keychain (browser passwords, ssh-agent identities, third-party app
// secrets), not just the Claude Code credential. This is a real
// reduction of HOME-isolation guarantees in subscription mode. Operators
// concerned about that should use API-key mode (set
// env.allowlist=["ANTHROPIC_API_KEY"]) — that path doesn't need Keychain
// access at all.
//
// Adjust when adding support for a new CLI's auth location.
var subscriptionAuthPaths = []string{
	".claude",
	".claude.json",
	".codex",
	".codex.json",
	".config/claude",
	".config/codex",
	// macOS — claude/codex use Keychain via the Security framework.
	"Library/Keychains",
	"Library/Application Support/Claude",
	"Library/Caches/claude-cli-nodejs",
}

// seedSubscriptionAuth symlinks the user's real-HOME subscription-auth
// directories into agentHome. Best-effort and idempotent: missing source
// paths are silently skipped, and existing symlinks/files at the
// destination are replaced.
//
// This bridges symphony-go's HOME isolation with claude/codex's
// subscription-mode auth, which lives in the user's normal HOME.
// API-key mode (allowlisted env var) does not need this — the env var is
// passed through `BuildAgentEnv` instead.
func seedSubscriptionAuth(agentHome string) error {
	realHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home: %w", err)
	}
	for _, rel := range subscriptionAuthPaths {
		src := filepath.Join(realHome, rel)
		if _, err := os.Stat(src); err != nil {
			// Source doesn't exist (or unreadable) — skip silently.
			continue
		}
		dst := filepath.Join(agentHome, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		if _, err := os.Lstat(dst); err == nil {
			// Best-effort replace; if dst is a non-empty real dir we
			// can't unlink, fall through and skip.
			if err := os.Remove(dst); err != nil {
				continue
			}
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
	}
	return nil
}
