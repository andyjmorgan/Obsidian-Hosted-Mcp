package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installFakeOb puts a fake ob binary at the front of PATH. Continuous sync
// invocations block until killed, mimicking the real daemon.
func installFakeOb(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ob"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"OBSIDIAN_EMAIL":    "user@example.com",
		"OBSIDIAN_PASSWORD": "pw",
		"OBSIDIAN_VAULTS":   "Notes",
		"MCP_AUTH_TOKEN":    "secret",
		"VAULTS_DIR":        t.TempDir(),
		"PORT":              "0",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestRunServesUntilShutdown(t *testing.T) {
	installFakeOb(t, `case "$1" in sync) exec sleep 60;; *) exit 0;; esac`)
	env := testEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, getenv(env), io.Discard, func(addr string) { addrCh <- addr })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not become ready")
	}

	res, err := http.Get(fmt.Sprintf("http://%s/healthz", addr))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d", res.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/", addr), strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated MCP request status = %d, want 401", res.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("run returned %v on graceful shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not shut down")
	}
}

func TestRunFailsOnBadConfig(t *testing.T) {
	env := testEnv(t)
	delete(env, "OBSIDIAN_EMAIL")
	if err := run(context.Background(), getenv(env), io.Discard, nil); err == nil {
		t.Error("run succeeded without OBSIDIAN_EMAIL")
	}
}

func TestRunFailsOnLoginFailure(t *testing.T) {
	installFakeOb(t, `case "$1" in login) echo invalid credentials >&2; exit 1;; *) exit 0;; esac`)
	err := run(context.Background(), getenv(testEnv(t)), io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Errorf("err = %v", err)
	}
}

func TestRunFailsOnSyncSetupFailure(t *testing.T) {
	installFakeOb(t, `case "$1" in sync-setup) echo no such vault >&2; exit 1;; *) exit 0;; esac`)
	err := run(context.Background(), getenv(testEnv(t)), io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "sync-setup failed") {
		t.Errorf("err = %v", err)
	}
}

func TestRunFailsWhenPortUnavailable(t *testing.T) {
	installFakeOb(t, `case "$1" in sync) exec sleep 60;; *) exit 0;; esac`)
	blocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	env := testEnv(t)
	env["PORT"] = strconv.Itoa(blocker.Addr().(*net.TCPAddr).Port)
	if err := run(context.Background(), getenv(env), io.Discard, nil); err == nil {
		t.Error("run succeeded on an occupied port")
	}
}

func TestRunWithOIDC(t *testing.T) {
	installFakeOb(t, `case "$1" in sync) exec sleep 60;; *) exit 0;; esac`)
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, "http://"+r.Host, "http://"+r.Host+"/jwks")
	}))
	defer idp.Close()

	env := testEnv(t)
	env["OAUTH_ISSUER"] = idp.URL
	env["OAUTH_AUDIENCE"] = "obsidian-mcp"
	env["MCP_PUBLIC_URL"] = "https://obsidian.example.com"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, getenv(env), io.Discard, func(addr string) { addrCh <- addr })
	}()
	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not become ready")
	}

	res, err := http.Get(fmt.Sprintf("http://%s/.well-known/oauth-protected-resource", addr))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "https://obsidian.example.com") {
		t.Errorf("metadata = %d %s", res.StatusCode, body)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("run returned %v after shutdown", err)
	}
}

func TestRunOIDCConfigFailure(t *testing.T) {
	installFakeOb(t, `case "$1" in sync) exec sleep 60;; *) exit 0;; esac`)
	env := testEnv(t)
	env["OAUTH_ISSUER"] = "http://127.0.0.1:1"
	env["OAUTH_AUDIENCE"] = "obsidian-mcp"
	env["MCP_PUBLIC_URL"] = "https://obsidian.example.com"
	err := run(context.Background(), getenv(env), io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "configuring OIDC auth") {
		t.Errorf("err = %v, want OIDC config failure", err)
	}
}
