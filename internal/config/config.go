// Package config loads and validates server configuration from the
// environment.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultPort is the HTTP port used when PORT is not set.
const DefaultPort = 8080

// deviceNamePrefix prefixes the generated device name when
// OBSIDIAN_DEVICE_NAME is not set.
const deviceNamePrefix = "ObsidianMCP-"

// Vault describes a single remote vault to sync and serve.
type Vault struct {
	// Name is the remote vault name as shown in Obsidian Sync. It is also
	// used as the local directory name under VaultsDir.
	Name string
	// Password is the vault's end-to-end encryption password. Empty means
	// no password is passed to sync-setup.
	Password string
}

// Config holds the full server configuration.
type Config struct {
	// Email and Password authenticate the Obsidian account (ob login).
	Email    string
	Password string
	// DeviceName identifies this client in sync version history.
	DeviceName string
	// Vaults lists the remote vaults to sync and expose over MCP.
	Vaults []Vault
	// VaultsDir is the local directory under which each vault is synced.
	VaultsDir string
	// AuthToken is the static bearer token required on MCP requests.
	AuthToken string
	// Port is the HTTP listen port.
	Port int
}

// Getenv is the subset of os.Getenv needed by Load, injectable for tests.
type Getenv func(key string) string

// Load reads configuration from the environment. randSource supplies
// entropy for the generated device name and is typically crypto/rand.Reader.
func Load(getenv Getenv, randSource io.Reader) (*Config, error) {
	cfg := &Config{
		Email:     getenv("OBSIDIAN_EMAIL"),
		Password:  getenv("OBSIDIAN_PASSWORD"),
		AuthToken: getenv("MCP_AUTH_TOKEN"),
	}
	if cfg.Email == "" {
		return nil, errors.New("OBSIDIAN_EMAIL must be set")
	}
	if cfg.Password == "" {
		return nil, errors.New("OBSIDIAN_PASSWORD must be set")
	}
	if cfg.AuthToken == "" {
		return nil, errors.New("MCP_AUTH_TOKEN must be set: the MCP endpoint is bearer-token protected")
	}

	vaults, err := parseVaults(getenv("OBSIDIAN_VAULTS"), getenv("OBSIDIAN_VAULT_PASSWORD"))
	if err != nil {
		return nil, err
	}
	cfg.Vaults = vaults

	cfg.DeviceName = getenv("OBSIDIAN_DEVICE_NAME")
	if cfg.DeviceName == "" {
		name, err := randomDeviceName(randSource)
		if err != nil {
			return nil, err
		}
		cfg.DeviceName = name
	}

	cfg.VaultsDir = getenv("VAULTS_DIR")
	if cfg.VaultsDir == "" {
		home := getenv("HOME")
		if home == "" {
			home = "/"
		}
		cfg.VaultsDir = filepath.Join(home, "vaults")
	}

	cfg.Port = DefaultPort
	if port := getenv("PORT"); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return nil, fmt.Errorf("PORT must be an integer between 0 and 65535 (0 picks an ephemeral port), got %q", port)
		}
		cfg.Port = n
	}

	return cfg, nil
}

// parseVaults parses the OBSIDIAN_VAULTS list. Each comma-separated entry is
// either "Name" or "Name:password"; entries without a password fall back to
// sharedPassword.
func parseVaults(list, sharedPassword string) ([]Vault, error) {
	var vaults []Vault
	seen := make(map[string]bool)
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, password := entry, sharedPassword
		if i := strings.Index(entry, ":"); i >= 0 {
			name, password = entry[:i], entry[i+1:]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("OBSIDIAN_VAULTS entry %q has an empty vault name", entry)
		}
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return nil, fmt.Errorf("vault name %q must not contain path separators or be a relative path", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("vault %q is listed more than once in OBSIDIAN_VAULTS", name)
		}
		seen[name] = true
		vaults = append(vaults, Vault{Name: name, Password: password})
	}
	if len(vaults) == 0 {
		return nil, errors.New("OBSIDIAN_VAULTS must list at least one vault (comma-separated, optionally Name:password)")
	}
	return vaults, nil
}

// randomDeviceName returns deviceNamePrefix followed by 8 hex characters.
func randomDeviceName(randSource io.Reader) (string, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(randSource, buf); err != nil {
		return "", fmt.Errorf("generating device name: %w", err)
	}
	return deviceNamePrefix + hex.EncodeToString(buf), nil
}
