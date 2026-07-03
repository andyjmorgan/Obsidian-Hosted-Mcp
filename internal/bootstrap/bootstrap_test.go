package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/config"
)

// fakeOb writes an executable shell script standing in for the ob binary
// and returns its path plus the file every invocation logs its args to.
func fakeOb(t *testing.T, script string) (binary, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	binary = filepath.Join(dir, "ob")
	full := "#!/bin/sh\necho \"$@\" >> " + argsLog + "\n" + script + "\n"
	if err := os.WriteFile(binary, []byte(full), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, argsLog
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Email:      "user@example.com",
		Password:   "account-pw",
		DeviceName: "test-device",
		VaultsDir:  t.TempDir(),
		Vaults: []config.Vault{
			{Name: "Work", Password: "vault-pw"},
			{Name: "Personal"},
		},
	}
}

func newTestBootstrapper(t *testing.T, script string) (*Bootstrapper, string) {
	t.Helper()
	binary, argsLog := fakeOb(t, script)
	b := New(testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.binary = binary
	b.syncOutput = io.Discard
	return b, argsLog
}

func loggedCalls(t *testing.T, argsLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestVaultPath(t *testing.T) {
	b := New(&config.Config{VaultsDir: "/data"}, slog.Default())
	if got := b.VaultPath(config.Vault{Name: "Notes"}); got != "/data/Notes" {
		t.Errorf("VaultPath = %q", got)
	}
}

func TestLogin(t *testing.T) {
	b, argsLog := newTestBootstrapper(t, "exit 0")
	if err := b.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := loggedCalls(t, argsLog)
	want := "login --email user@example.com --password account-pw"
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want [%q]", calls, want)
	}
}

func TestLoginFailure(t *testing.T) {
	b, _ := newTestBootstrapper(t, "echo bad credentials; exit 1")
	err := b.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Errorf("err = %v", err)
	}
}

func TestSetupVaults(t *testing.T) {
	b, argsLog := newTestBootstrapper(t, "exit 0")
	if err := b.SetupVaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := loggedCalls(t, argsLog)
	want := []string{
		"sync-setup --vault Work --path " + b.VaultPath(b.cfg.Vaults[0]) +
			" --device-name test-device --password vault-pw",
		"sync-setup --vault Personal --path " + b.VaultPath(b.cfg.Vaults[1]) +
			" --device-name test-device",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	for _, v := range b.cfg.Vaults {
		if info, err := os.Stat(b.VaultPath(v)); err != nil || !info.IsDir() {
			t.Errorf("vault directory %q not created: %v", b.VaultPath(v), err)
		}
	}
}

func TestSetupVaultsFailsFast(t *testing.T) {
	b, argsLog := newTestBootstrapper(t, "echo unknown vault; exit 1")
	err := b.SetupVaults(context.Background())
	if err == nil || !strings.Contains(err.Error(), `vault "Work"`) {
		t.Fatalf("err = %v", err)
	}
	if calls := loggedCalls(t, argsLog); len(calls) != 1 {
		t.Errorf("expected fail-fast after first vault, got calls %q", calls)
	}
}

func TestSetupVaultsMkdirFailure(t *testing.T) {
	b, _ := newTestBootstrapper(t, "exit 0")
	blocker := filepath.Join(b.cfg.VaultsDir, "Work")
	if err := os.WriteFile(blocker, []byte("file, not dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.SetupVaults(context.Background()); err == nil {
		t.Error("SetupVaults succeeded despite blocked directory")
	}
}

func TestSyncContinuouslyRestartsUntilCancelled(t *testing.T) {
	// Every sync invocation exits immediately, forcing restarts.
	b, argsLog := newTestBootstrapper(t, "exit 1")
	// A zero threshold means every exit counts as a stable run, exercising
	// the backoff-reset branch.
	b.stableRun = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleeps atomic.Int32
	b.sleep = func(context.Context, time.Duration) {
		// Simulate shutdown after a few restart cycles.
		if sleeps.Add(1) >= 6 {
			cancel()
		}
	}
	b.SyncContinuously(ctx)

	calls := loggedCalls(t, argsLog)
	if len(calls) < 2 {
		t.Fatalf("expected multiple sync attempts, got %q", calls)
	}
	var work, personal int
	for _, c := range calls {
		if !strings.HasPrefix(c, "sync --continuous --path ") {
			t.Errorf("unexpected call %q", c)
		}
		if strings.HasSuffix(c, "/Work") {
			work++
		}
		if strings.HasSuffix(c, "/Personal") {
			personal++
		}
	}
	if work == 0 || personal == 0 {
		t.Errorf("both vaults should sync: work=%d personal=%d (calls %q)", work, personal, calls)
	}
}

func TestSyncContinuouslyStopsWhenContextAlreadyDone(t *testing.T) {
	b, argsLog := newTestBootstrapper(t, "exit 0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.SyncContinuously(ctx)
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Errorf("sync ran despite cancelled context: %q", loggedCalls(t, argsLog))
	}
}

func TestSleepCtxReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	start := time.Now()
	sleepCtx(ctx, time.Minute)
	if time.Since(start) > 5*time.Second {
		t.Error("sleepCtx did not return promptly on cancel")
	}
	sleepCtx(context.Background(), time.Millisecond)
}

func TestRedact(t *testing.T) {
	args := []string{"login", "--email", "a@b.c", "--password", "hunter2"}
	got := redact(args, []string{"--password"})
	want := []string{"login", "--email", "a@b.c", "--password", "****"}
	if !slices.Equal(got, want) {
		t.Errorf("redact = %q, want %q", got, want)
	}
	if args[4] != "hunter2" {
		t.Error("redact mutated its input")
	}
	if got := redact([]string{"--password"}, []string{"--password"}); !slices.Equal(got, []string{"--password"}) {
		t.Errorf("trailing flag: %q", got)
	}
}
