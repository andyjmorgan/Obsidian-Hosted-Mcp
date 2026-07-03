// Command obsidian-mcp bootstraps Obsidian Sync vaults via the official
// headless client and serves them over a bearer-token-protected MCP HTTP
// endpoint.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/bootstrap"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/config"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/oidcauth"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/search"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/server"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/vault"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stderr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// run is the testable body of main. onReady, when non-nil, is called with
// the bound listen address once the HTTP server is accepting connections.
func run(ctx context.Context, getenv config.Getenv, logOut io.Writer, onReady func(addr string)) error {
	logger := slog.New(slog.NewTextHandler(logOut, nil))

	cfg, err := config.Load(getenv, rand.Reader)
	if err != nil {
		return err
	}

	b := bootstrap.New(cfg, logger)
	if err := b.Login(ctx); err != nil {
		return err
	}
	if err := b.SetupVaults(ctx); err != nil {
		return err
	}
	go b.SyncContinuously(ctx)

	vaults := make([]*vault.Vault, 0, len(cfg.Vaults))
	for _, v := range cfg.Vaults {
		vaults = append(vaults, vault.New(v.Name, b.VaultPath(v)))
	}
	srv := server.New(vaults, search.New("rg", nil))

	authCfg := server.AuthConfig{StaticToken: cfg.AuthToken}
	if cfg.OAuth != nil {
		logger.Info("delegating auth to OIDC provider", "issuer", cfg.OAuth.Issuer, "audience", cfg.OAuth.Audience)
		verifier, err := oidcauth.New(ctx, cfg.OAuth, nil)
		if err != nil {
			return fmt.Errorf("configuring OIDC auth: %w", err)
		}
		authCfg.OIDC = &server.OIDCAuth{
			Verify:    verifier.Verify,
			Issuer:    cfg.OAuth.Issuer,
			Scopes:    cfg.OAuth.Scopes,
			PublicURL: cfg.PublicURL,
		}
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(authCfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("listening on port %d: %w", cfg.Port, err)
	}
	logger.Info("MCP server listening", "addr", listener.Addr().String(), "vaults", len(vaults))
	if onReady != nil {
		onReady(listener.Addr().String())
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
