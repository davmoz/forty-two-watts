package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/frahlg/forty-two-watts/go/internal/config"
	"github.com/frahlg/forty-two-watts/go/internal/driverregistry"
)

func TestDriverSecretKeysIncludePortableDriverPathAlias(t *testing.T) {
	driverDir := filepath.Join(t.TempDir(), "custom-driver-dir")
	if err := os.MkdirAll(driverDir, 0755); err != nil {
		t.Fatal(err)
	}
	luaPath := filepath.Join(driverDir, "sonnen.lua")
	if err := os.WriteFile(luaPath, []byte(`
DRIVER_MANIFEST = {
  name = "sonnen",
  version = "1.0.0",
  role = "battery",
  protocols = { "http" },
  options = {
    { name = "api_token", purpose = "always", type = "string", secret = true },
  },
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(&Deps{
		DriverDir:  driverDir,
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
	})
	secrets := srv.driverSecretKeys()
	cfg := &config.Config{Drivers: []config.Driver{{
		Name: "sonnen",
		Lua:  "drivers/sonnen.lua",
		Config: map[string]any{
			"api_token": "secret-token",
		},
	}}}

	maskDriverConfigSecrets(cfg, secrets)

	if got := cfg.Drivers[0].Config["api_token"]; got != maskedPlaceholder {
		t.Fatalf("api_token = %q, want masked placeholder", got)
	}

	incoming := &config.Config{Drivers: []config.Driver{{
		Name: "sonnen",
		Lua:  "drivers/sonnen.lua",
		Config: map[string]any{
			"api_token": maskedPlaceholder,
		},
	}}}
	existing := &config.Config{Drivers: []config.Driver{{
		Name: "sonnen",
		Config: map[string]any{
			"api_token": "secret-token",
		},
	}}}
	restoreDriverConfigSecrets(incoming, existing, secrets)
	if got := incoming.Drivers[0].Config["api_token"]; got != "secret-token" {
		t.Fatalf("restored api_token = %q, want original secret", got)
	}
}

// H2: `driver:` registry-ref entries have no Lua path, so their
// manifest-declared secrets were invisible to the mask/restore cycle.
// The manifest is now resolved from the registry client's LOCAL cache
// (never the network) and keyed "driver:<ref>".
func TestDriverSecretKeysResolveRegistryRefFromCache(t *testing.T) {
	cacheDir := t.TempDir()
	src := `
DRIVER_MANIFEST = {
  name = "cloudbat",
  version = "1.2.3",
  role = "battery",
  requires = {
    { name = "device_pin", purpose = "always", type = "string", secret = true },
  },
}
`
	if err := os.WriteFile(filepath.Join(cacheDir, "cloudbat-1.2.3.lua"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	srv := New(&Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		// Unreachable base URL: cache-first resolution must never dial.
		DriverRegistry: driverregistry.New("http://registry.invalid", cacheDir),
	})
	cfg := &config.Config{Drivers: []config.Driver{{
		Name:   "bat",
		Driver: "cloudbat@1.2.3",
		Config: map[string]any{"device_pin": "1234"},
	}}}

	secrets := srv.driverSecretKeysFor(cfg)
	maskDriverConfigSecrets(cfg, secrets)
	if got := cfg.Drivers[0].Config["device_pin"]; got != maskedPlaceholder {
		t.Fatalf("device_pin = %q, want masked placeholder", got)
	}

	incoming := &config.Config{Drivers: []config.Driver{{
		Name:   "bat",
		Driver: "cloudbat@1.2.3",
		Config: map[string]any{"device_pin": maskedPlaceholder},
	}}}
	existing := &config.Config{Drivers: []config.Driver{{
		Name:   "bat",
		Driver: "cloudbat@1.2.3",
		Config: map[string]any{"device_pin": "1234"},
	}}}
	restoreDriverConfigSecrets(incoming, existing, srv.driverSecretKeysFor(incoming))
	if got := incoming.Drivers[0].Config["device_pin"]; got != "1234" {
		t.Fatalf("restored device_pin = %q, want original", got)
	}

	// An UNCACHED ref is skipped without touching the network — its
	// declared-only secrets fall back to the heuristic.
	uncached := &config.Config{Drivers: []config.Driver{{
		Name:   "other",
		Driver: "nosuch@9.9.9",
		Config: map[string]any{"x": "y"},
	}}}
	if keys := srv.driverSecretKeysFor(uncached); keys[registryRefSecretKey("nosuch@9.9.9")] != nil {
		t.Errorf("uncached ref produced secret keys: %v", keys)
	}
}

// H3: keys matching the name heuristic (password|token|secret|api_key,
// case-insensitive substring) are masked and restored even when no
// manifest is resolvable — legacy config_secrets drivers and rotated
// refresh_token values must never reach the UI in plaintext.
func TestDriverSecretHeuristicMasksWithoutManifest(t *testing.T) {
	srv := New(&Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
	})
	cfg := &config.Config{Drivers: []config.Driver{{
		Name: "legacy",
		Lua:  "drivers/legacy_cloud.lua", // not in any catalog
		Config: map[string]any{
			"api_token":     "tok-123",
			"refresh_token": "rt-456",
			"Password":      "hunter2", // case-insensitive
			"host":          "192.168.1.5",
			"port":          80, // non-string values never masked
		},
	}}}
	maskDriverConfigSecrets(cfg, srv.driverSecretKeysFor(cfg))
	got := cfg.Drivers[0].Config
	for _, k := range []string{"api_token", "refresh_token", "Password"} {
		if got[k] != maskedPlaceholder {
			t.Errorf("%s = %q, want masked placeholder", k, got[k])
		}
	}
	if got["host"] != "192.168.1.5" || got["port"] != 80 {
		t.Errorf("non-secret keys altered: host=%v port=%v", got["host"], got["port"])
	}

	// Restore: placeholder and empty both preserve the stored value.
	incoming := &config.Config{Drivers: []config.Driver{{
		Name: "legacy",
		Lua:  "drivers/legacy_cloud.lua",
		Config: map[string]any{
			"api_token":     maskedPlaceholder,
			"refresh_token": "",
			"host":          "192.168.1.9",
		},
	}}}
	existing := &config.Config{Drivers: []config.Driver{{
		Name: "legacy",
		Config: map[string]any{
			"api_token":     "tok-123",
			"refresh_token": "rt-456",
		},
	}}}
	restoreDriverConfigSecrets(incoming, existing, srv.driverSecretKeysFor(incoming))
	in := incoming.Drivers[0].Config
	if in["api_token"] != "tok-123" || in["refresh_token"] != "rt-456" {
		t.Errorf("heuristic restore failed: %v", in)
	}
	if in["host"] != "192.168.1.9" {
		t.Errorf("host = %v, want the operator's new value untouched", in["host"])
	}
}
