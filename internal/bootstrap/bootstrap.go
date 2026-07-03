// Package bootstrap drives the official obsidian-headless CLI (ob) to log
// in, connect each configured vault, and keep vaults continuously synced.
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/config"
)

const (
	initialBackoff = time.Second
	maxBackoff     = time.Minute
	// stableRunThreshold is how long a sync process must run before its
	// next failure resets the restart backoff.
	stableRunThreshold = time.Minute
)

// Bootstrapper runs ob commands for the configured account and vaults.
type Bootstrapper struct {
	cfg *config.Config
	log *slog.Logger

	// binary is the ob executable name, overridable in tests.
	binary string
	// syncOutput receives stdout/stderr of long-running sync processes.
	syncOutput io.Writer
	// sleep waits for d or until ctx is done, injectable in tests.
	sleep func(ctx context.Context, d time.Duration)
	// stableRun is how long a sync process must live before its next
	// failure resets the restart backoff, shortened in tests.
	stableRun time.Duration
}

// New returns a Bootstrapper for cfg that executes "ob" from PATH.
func New(cfg *config.Config, log *slog.Logger) *Bootstrapper {
	return &Bootstrapper{
		cfg:        cfg,
		log:        log,
		binary:     "ob",
		syncOutput: os.Stdout,
		sleep:      sleepCtx,
		stableRun:  stableRunThreshold,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// VaultPath returns the local directory a vault syncs into.
func (b *Bootstrapper) VaultPath(v config.Vault) string {
	return filepath.Join(b.cfg.VaultsDir, v.Name)
}

// Login authenticates the Obsidian account.
func (b *Bootstrapper) Login(ctx context.Context) error {
	b.log.Info("logging in to Obsidian", "email", b.cfg.Email)
	args := []string{"login", "--email", b.cfg.Email, "--password", b.cfg.Password}
	if err := b.runOb(ctx, args, []string{"--password"}); err != nil {
		return fmt.Errorf("ob login failed: %w", err)
	}
	return nil
}

// SetupVaults connects every configured vault to its local directory. Any
// failure is fatal: a misconfigured vault should stop the container.
func (b *Bootstrapper) SetupVaults(ctx context.Context) error {
	for _, v := range b.cfg.Vaults {
		path := b.VaultPath(v)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("creating vault directory %q: %w", path, err)
		}
		b.log.Info("connecting vault", "vault", v.Name, "path", path, "device", b.cfg.DeviceName)
		args := []string{
			"sync-setup",
			"--vault", v.Name,
			"--path", path,
			"--device-name", b.cfg.DeviceName,
		}
		if v.Password != "" {
			args = append(args, "--password", v.Password)
		}
		if err := b.runOb(ctx, args, []string{"--password"}); err != nil {
			return fmt.Errorf("ob sync-setup failed for vault %q: %w", v.Name, err)
		}
	}
	return nil
}

// SyncContinuously runs one "ob sync --continuous" process per vault,
// restarting crashed processes with exponential backoff, until ctx is done.
func (b *Bootstrapper) SyncContinuously(ctx context.Context) {
	var wg sync.WaitGroup
	for _, v := range b.cfg.Vaults {
		wg.Add(1)
		go func(v config.Vault) {
			defer wg.Done()
			b.superviseVault(ctx, v)
		}(v)
	}
	wg.Wait()
}

func (b *Bootstrapper) superviseVault(ctx context.Context, v config.Vault) {
	backoff := initialBackoff
	for ctx.Err() == nil {
		b.log.Info("starting continuous sync", "vault", v.Name)
		start := time.Now()
		cmd := exec.CommandContext(ctx, b.binary, "sync", "--continuous", "--path", b.VaultPath(v))
		cmd.Stdout = b.syncOutput
		cmd.Stderr = b.syncOutput
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= b.stableRun {
			backoff = initialBackoff
		}
		b.log.Warn("continuous sync exited, restarting",
			"vault", v.Name, "error", err, "backoff", backoff.String())
		b.sleep(ctx, backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

// runOb executes ob with args, logging the command with the values of the
// flags named in secretFlags redacted.
func (b *Bootstrapper) runOb(ctx context.Context, args, secretFlags []string) error {
	b.log.Debug("running", "command", b.binary, "args", redact(args, secretFlags))
	cmd := exec.CommandContext(ctx, b.binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

// redact returns a copy of args with the value following each flag in
// secretFlags replaced, safe for logging.
func redact(args, secretFlags []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		for _, flag := range secretFlags {
			if out[i] == flag {
				out[i+1] = "****"
			}
		}
	}
	return out
}
