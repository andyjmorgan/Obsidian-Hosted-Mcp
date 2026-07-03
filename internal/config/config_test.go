package config

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func env(m map[string]string) Getenv {
	return func(key string) string { return m[key] }
}

func validEnv() map[string]string {
	return map[string]string{
		"OBSIDIAN_EMAIL":    "user@example.com",
		"OBSIDIAN_PASSWORD": "secret",
		"OBSIDIAN_VAULTS":   "Notes",
		"MCP_AUTH_TOKEN":    "token",
		"HOME":              "/home/test",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(validEnv()), rand.Reader)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Email != "user@example.com" || cfg.Password != "secret" || cfg.AuthToken != "token" {
		t.Errorf("credentials not loaded: %+v", cfg)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.VaultsDir != "/home/test/vaults" {
		t.Errorf("VaultsDir = %q, want /home/test/vaults", cfg.VaultsDir)
	}
	if !strings.HasPrefix(cfg.DeviceName, deviceNamePrefix) || len(cfg.DeviceName) != len(deviceNamePrefix)+8 {
		t.Errorf("DeviceName = %q, want %s + 8 hex chars", cfg.DeviceName, deviceNamePrefix)
	}
	if len(cfg.Vaults) != 1 || cfg.Vaults[0] != (Vault{Name: "Notes"}) {
		t.Errorf("Vaults = %+v", cfg.Vaults)
	}
}

func TestLoadExplicitValues(t *testing.T) {
	m := validEnv()
	m["OBSIDIAN_DEVICE_NAME"] = "my-device"
	m["VAULTS_DIR"] = "/data/vaults"
	m["PORT"] = "9000"
	cfg, err := Load(env(m), rand.Reader)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceName != "my-device" {
		t.Errorf("DeviceName = %q", cfg.DeviceName)
	}
	if cfg.VaultsDir != "/data/vaults" {
		t.Errorf("VaultsDir = %q", cfg.VaultsDir)
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %d", cfg.Port)
	}
}

func TestLoadNoHomeFallsBackToRoot(t *testing.T) {
	m := validEnv()
	delete(m, "HOME")
	cfg, err := Load(env(m), rand.Reader)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VaultsDir != "/vaults" {
		t.Errorf("VaultsDir = %q, want /vaults", cfg.VaultsDir)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, key := range []string{"OBSIDIAN_EMAIL", "OBSIDIAN_PASSWORD", "MCP_AUTH_TOKEN", "OBSIDIAN_VAULTS"} {
		t.Run(key, func(t *testing.T) {
			m := validEnv()
			delete(m, key)
			if _, err := Load(env(m), rand.Reader); err == nil {
				t.Errorf("Load succeeded without %s", key)
			}
		})
	}
}

func TestLoadInvalidPort(t *testing.T) {
	for _, port := range []string{"abc", "-1", "65536", "8080x"} {
		t.Run(port, func(t *testing.T) {
			m := validEnv()
			m["PORT"] = port
			if _, err := Load(env(m), rand.Reader); err == nil {
				t.Errorf("Load accepted PORT=%q", port)
			}
		})
	}
}

func TestLoadEphemeralPort(t *testing.T) {
	m := validEnv()
	m["PORT"] = "0"
	cfg, err := Load(env(m), rand.Reader)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0", cfg.Port)
	}
}

func TestParseVaults(t *testing.T) {
	tests := []struct {
		name   string
		list   string
		shared string
		want   []Vault
		errSub string
	}{
		{
			name: "single vault no password",
			list: "Notes",
			want: []Vault{{Name: "Notes"}},
		},
		{
			name: "per-vault passwords",
			list: "Work:pw1,Personal:pw2",
			want: []Vault{{Name: "Work", Password: "pw1"}, {Name: "Personal", Password: "pw2"}},
		},
		{
			name:   "shared password fallback",
			list:   "Work,Personal:own",
			shared: "shared",
			want:   []Vault{{Name: "Work", Password: "shared"}, {Name: "Personal", Password: "own"}},
		},
		{
			name: "password containing colons",
			list: "Notes:a:b:c",
			want: []Vault{{Name: "Notes", Password: "a:b:c"}},
		},
		{
			name: "whitespace and empty entries",
			list: " Notes , ,Ideas ",
			want: []Vault{{Name: "Notes"}, {Name: "Ideas"}},
		},
		{name: "empty list", list: "", errSub: "at least one vault"},
		{name: "only separators", list: " , ,", errSub: "at least one vault"},
		{name: "empty name with password", list: ":pw", errSub: "empty vault name"},
		{name: "duplicate name", list: "Notes,Notes", errSub: "more than once"},
		{name: "path separator in name", list: "a/b", errSub: "path separators"},
		{name: "backslash in name", list: `a\b`, errSub: "path separators"},
		{name: "dotdot name", list: "..", errSub: "path separators"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVaults(tt.list, tt.shared)
			if tt.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("err = %v, want containing %q", err, tt.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVaults: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("vault %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestLoadDeviceNameEntropyFailure(t *testing.T) {
	if _, err := Load(env(validEnv()), failingReader{}); err == nil {
		t.Error("Load succeeded with failing entropy source")
	}
}
