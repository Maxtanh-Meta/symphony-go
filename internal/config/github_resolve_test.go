package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveToken_InlinePreferred(t *testing.T) {
	t.Setenv("RT_TOK", "from-env")
	g := GitHubConfig{Token: "inline-tok", TokenEnv: "RT_TOK"}
	// Validate would reject this combo, but the resolver itself prefers
	// inline so a buggy caller doesn't silently pick env.
	got, err := g.ResolveToken()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "inline-tok" {
		t.Errorf("got %q, want inline-tok", got)
	}
}

func TestResolveToken_FallbackToEnv(t *testing.T) {
	t.Setenv("RT_TOK_FALLBACK", "from-env")
	g := GitHubConfig{TokenEnv: "RT_TOK_FALLBACK"}
	got, err := g.ResolveToken()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

func TestResolveToken_EmptyEnvErrors(t *testing.T) {
	t.Setenv("RT_TOK_EMPTY", "")
	g := GitHubConfig{TokenEnv: "RT_TOK_EMPTY"}
	if _, err := g.ResolveToken(); err == nil {
		t.Fatal("expected error for empty env")
	}
}

func TestResolveAppID_InlineWins(t *testing.T) {
	t.Setenv("RT_APP_ID", "999")
	g := GitHubConfig{AppID: 12345, AppIDEnv: "RT_APP_ID"}
	got, err := g.ResolveAppID()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != 12345 {
		t.Errorf("got %d, want 12345", got)
	}
}

func TestResolveAppID_EnvParsed(t *testing.T) {
	t.Setenv("RT_APP_ID_ONLY", "67890")
	g := GitHubConfig{AppIDEnv: "RT_APP_ID_ONLY"}
	got, err := g.ResolveAppID()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != 67890 {
		t.Errorf("got %d, want 67890", got)
	}
}

func TestResolveAppID_NonNumericEnvErrors(t *testing.T) {
	t.Setenv("RT_APP_ID_BAD", "not-a-number")
	g := GitHubConfig{AppIDEnv: "RT_APP_ID_BAD"}
	if _, err := g.ResolveAppID(); err == nil {
		t.Fatal("expected error for non-numeric env")
	}
}

func TestResolveAppID_NeitherSetErrors(t *testing.T) {
	g := GitHubConfig{}
	if _, err := g.ResolveAppID(); err == nil {
		t.Fatal("expected error when neither app_id nor app_id_env set")
	}
}

func TestResolvePrivateKey_InlinePathPreferredOverEnv(t *testing.T) {
	dir := t.TempDir()
	pemFile := filepath.Join(dir, "inline.pem")
	if err := os.WriteFile(pemFile, []byte("INLINE-PEM-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := GitHubConfig{PrivateKeyPath: pemFile}
	body, src, err := g.ResolvePrivateKey()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(body) != "INLINE-PEM-BYTES" {
		t.Errorf("body = %q", body)
	}
	if src != pemFile {
		t.Errorf("src = %q, want %q", src, pemFile)
	}
}

func TestResolvePrivateKey_InlinePEM(t *testing.T) {
	g := GitHubConfig{PrivateKeyPEM: "-----BEGIN RSA-----\nx\n-----END RSA-----"}
	body, src, err := g.ResolvePrivateKey()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(string(body), "-----BEGIN RSA-----") {
		t.Errorf("body = %q", body)
	}
	if src != "<inline>" {
		t.Errorf("src = %q, want <inline>", src)
	}
}

func TestResolvePrivateKey_PEMEnv(t *testing.T) {
	t.Setenv("RT_PEM_ENV", "FROM-ENV")
	g := GitHubConfig{PrivateKeyPEMEnv: "RT_PEM_ENV"}
	body, src, err := g.ResolvePrivateKey()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(body) != "FROM-ENV" {
		t.Errorf("body = %q", body)
	}
	if !strings.Contains(src, "RT_PEM_ENV") {
		t.Errorf("src = %q, want to mention RT_PEM_ENV", src)
	}
}

func TestResolvePrivateKey_PathEnv(t *testing.T) {
	dir := t.TempDir()
	pemFile := filepath.Join(dir, "envpath.pem")
	if err := os.WriteFile(pemFile, []byte("PATH-ENV-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RT_PEM_PATH", pemFile)
	g := GitHubConfig{PrivateKeyPathEnv: "RT_PEM_PATH"}
	body, src, err := g.ResolvePrivateKey()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(body) != "PATH-ENV-BYTES" {
		t.Errorf("body = %q", body)
	}
	if src != pemFile {
		t.Errorf("src = %q, want %q", src, pemFile)
	}
}
