package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvBackend(t *testing.T) {
	t.Setenv("SPLITSCREEN_SECRET_RUNNER_ALPHA", "abc")
	b := NewEnvBackend()

	s, err := b.Get("runner-alpha")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s.Value != "abc" {
		t.Fatalf("value = %q", s.Value)
	}
	if _, err := b.Get("missing"); err == nil {
		t.Fatal("expected a miss for an unset secret")
	}
}

func dirBackend(t *testing.T) (*DirBackend, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func TestDirBackend(t *testing.T) {
	b, dir := dirBackend(t)
	// A trailing newline from `echo` is the overwhelmingly common way these
	// files get written, and a token with a newline fails authentication in a
	// way that is miserable to debug.
	if err := os.WriteFile(filepath.Join(dir, "jira-token"), []byte("tok123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := b.Get("jira-token")
	if err != nil {
		t.Fatal(err)
	}
	if s.Value != "tok123" {
		t.Fatalf("value = %q, want the trailing newline stripped", s.Value)
	}
}

func TestDirBackendRefusesLooseDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirBackend(dir); err == nil {
		t.Fatal("a world-readable secret directory was accepted")
	}
}

func TestDirBackendRejectsPathTraversal(t *testing.T) {
	b, _ := dirBackend(t)
	for _, name := range []string{"../etc/passwd", "sub/dir", ".."} {
		if _, err := b.Get(name); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestExpiry(t *testing.T) {
	b, dir := dirBackend(t)
	soon := time.Now().Add(48 * time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "api"), []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.expires"),
		[]byte(soon.Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := b.Get("api")
	if err != nil {
		t.Fatal(err)
	}
	if s.ExpiresAt == nil {
		t.Fatal("expiry was not read")
	}

	// Nothing due inside a day.
	if got := Expiring(b, 24*time.Hour, time.Now()); len(got) != 0 {
		t.Fatalf("expiring within 24h = %v", got)
	}
	// Due inside a week.
	got := Expiring(b, 7*24*time.Hour, time.Now())
	if len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("expiring within a week = %v", got)
	}
	if got[0].Value != "" {
		t.Error("a warning carried the secret value")
	}
}

func TestChainPrefersFirstBackend(t *testing.T) {
	b, dir := dirBackend(t)
	if err := os.WriteFile(filepath.Join(dir, "shared"), []byte("from-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPLITSCREEN_SECRET_SHARED", "from-env")
	t.Setenv("SPLITSCREEN_SECRET_ENVONLY", "env")

	chain := Chain{b, NewEnvBackend()}
	s, err := chain.Get("shared")
	if err != nil {
		t.Fatal(err)
	}
	if s.Value != "from-dir" {
		t.Fatalf("value = %q, want the earlier backend to win", s.Value)
	}
	// Fallthrough still works.
	if s, err := chain.Get("envonly"); err != nil || s.Value != "env" {
		t.Fatalf("fallthrough: %v %q", err, s.Value)
	}
	if _, err := chain.Get("nowhere"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}
