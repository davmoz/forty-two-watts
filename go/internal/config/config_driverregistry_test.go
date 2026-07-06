package config

import (
	"strings"
	"testing"
)

// registryDriverYAML is minimalYAML with the site-meter driver sourced
// from the registry (`driver:` ref) instead of a local `lua:` path.
const registryDriverYAML = `
site:
  name: Test
fuse:
  max_amps: 16
drivers:
  - name: deye
    driver: deye@3.1.1
    is_site_meter: true
    capabilities:
      modbus:
        host: 192.168.1.42
api:
  port: 8080
`

func TestDriverRegistryRefParses(t *testing.T) {
	c, err := Parse([]byte(registryDriverYAML), "/tmp")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := c.Drivers[0]
	if d.Driver != "deye@3.1.1" {
		t.Errorf("Driver ref = %q, want deye@3.1.1", d.Driver)
	}
	if d.Lua != "" {
		t.Errorf("Lua = %q, want empty (registry ref is not a path)", d.Lua)
	}
}

func TestDriverRefAndLuaMutuallyExclusive(t *testing.T) {
	yaml := strings.Replace(registryDriverYAML,
		"driver: deye@3.1.1",
		"driver: deye@3.1.1\n    lua: drivers/deye.lua", 1)
	if _, err := Parse([]byte(yaml), "/tmp"); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually-exclusive error, got %v", err)
	}
}

func TestDriverRequiresLuaOrRef(t *testing.T) {
	yaml := strings.Replace(registryDriverYAML, "    driver: deye@3.1.1\n", "", 1)
	if _, err := Parse([]byte(yaml), "/tmp"); err == nil ||
		!strings.Contains(err.Error(), "`lua` or `driver`") {
		t.Fatalf("want lua-or-driver error, got %v", err)
	}
}

func TestDriverRefRejectsUnpinned(t *testing.T) {
	for _, bad := range []string{"deye", "deye@", "@3.1.1"} {
		yaml := strings.Replace(registryDriverYAML, "driver: deye@3.1.1", "driver: "+bad, 1)
		if _, err := Parse([]byte(yaml), "/tmp"); err == nil {
			t.Errorf("ref %q: want parse error (pinning is mandatory), got nil", bad)
		}
	}
}

func TestDriverRegistryNetValidation(t *testing.T) {
	for _, net := range []string{"devnet", "testnet", "mainnet"} {
		yaml := registryDriverYAML + "driver_registry:\n  net: " + net + "\n"
		if _, err := Parse([]byte(yaml), "/tmp"); err != nil {
			t.Errorf("net %q: unexpected error %v", net, err)
		}
	}
	yaml := registryDriverYAML + "driver_registry:\n  net: prodnet\n"
	if _, err := Parse([]byte(yaml), "/tmp"); err == nil ||
		!strings.Contains(err.Error(), "driver_registry.net") {
		t.Fatalf("want net-enum error, got %v", err)
	}
}

func TestDriverRegistryBaseURLPrecedence(t *testing.T) {
	// Nil section → devnet default.
	var c Config
	if got, want := c.DriverRegistryBaseURL(),
		"https://novacore-devnet.sourceful.dev/device-support/drivers"; got != want {
		t.Errorf("nil section base = %q, want %q", got, want)
	}
	// net picks the per-net base.
	c.DriverRegistry = &DriverRegistryConf{Net: "mainnet"}
	if got, want := c.DriverRegistryBaseURL(),
		"https://novacore-mainnet.sourceful.dev/device-support/drivers"; got != want {
		t.Errorf("mainnet base = %q, want %q", got, want)
	}
	// Explicit url beats net.
	c.DriverRegistry.URL = "http://registry.local/drivers"
	if got := c.DriverRegistryBaseURL(); got != "http://registry.local/drivers" {
		t.Errorf("url override = %q", got)
	}
	// Env beats both.
	t.Setenv("DRIVER_REGISTRY_URL", "http://127.0.0.1:9999/drivers")
	if got := c.DriverRegistryBaseURL(); got != "http://127.0.0.1:9999/drivers" {
		t.Errorf("env override = %q", got)
	}
}

func TestDriverRegistryNetDefaultApplied(t *testing.T) {
	yaml := registryDriverYAML + "driver_registry: {}\n"
	c, err := Parse([]byte(yaml), "/tmp")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.DriverRegistry == nil || c.DriverRegistry.Net != "devnet" {
		t.Errorf("driver_registry = %+v, want net devnet default", c.DriverRegistry)
	}
}

// Registry refs are NOT filesystem paths: the driver-path rewriting
// that runs on load (Resolve) and save (Unresolve) must leave both the
// ref and the empty Lua field alone.
func TestDriverRefUntouchedByPathRewriting(t *testing.T) {
	c, err := Parse([]byte(registryDriverYAML), "/base/dir")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Drivers[0].Lua != "" || c.Drivers[0].Driver != "deye@3.1.1" {
		t.Fatalf("after Resolve: lua=%q driver=%q", c.Drivers[0].Lua, c.Drivers[0].Driver)
	}
	c.UnresolveDriverPaths("/base/dir")
	if c.Drivers[0].Lua != "" || c.Drivers[0].Driver != "deye@3.1.1" {
		t.Fatalf("after Unresolve: lua=%q driver=%q", c.Drivers[0].Lua, c.Drivers[0].Driver)
	}
}

func TestDriverRegistryChangeRequiresRestart(t *testing.T) {
	oldCfg, err := Parse([]byte(registryDriverYAML), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	newCfg, err := Parse([]byte(registryDriverYAML+"driver_registry:\n  net: testnet\n"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	reasons := RestartRequiredFor(oldCfg, newCfg)
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "driver_registry") {
			found = true
		}
	}
	if !found {
		t.Errorf("driver_registry flip not flagged restart-required: %v", reasons)
	}
}
