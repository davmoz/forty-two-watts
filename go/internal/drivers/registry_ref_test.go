package drivers

// Tests for the registry-ref load path: cfg.Driver ("name@version")
// resolved to a local Lua path via the injected ResolveDriverRef hook
// (wired to driverregistry.Client.Resolve in main.go). Kept separate
// from registry_restart_test.go so the PR1 manifest work and this PR2
// surface don't collide in the same file.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frahlg/forty-two-watts/go/internal/config"
	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

// writeRefTestDriver drops a minimal no-op driver where the fake
// resolver can hand it out.
func writeRefTestDriver(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deye-3.1.1.lua")
	src := `
DRIVER_MANIFEST = {
    name = "deye", version = "3.1.1", role = "battery",
}
function driver_init(config) end
function driver_poll() end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAddResolvesRegistryRef(t *testing.T) {
	luaPath := writeRefTestDriver(t)
	reg := NewRegistry(telemetry.NewStore())
	var gotRef string
	reg.ResolveDriverRef = func(ref string) (string, error) {
		gotRef = ref
		return luaPath, nil
	}
	defer reg.ShutdownAll()

	err := reg.Add(context.Background(), config.Driver{
		Name:   "deye",
		Driver: "deye@3.1.1",
	})
	if err != nil {
		t.Fatalf("Add with registry ref: %v", err)
	}
	if gotRef != "deye@3.1.1" {
		t.Errorf("resolver got ref %q, want deye@3.1.1", gotRef)
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "deye" {
		t.Errorf("Names() = %v, want [deye]", names)
	}
}

func TestAddRefusedWithoutResolver(t *testing.T) {
	reg := NewRegistry(telemetry.NewStore())
	defer reg.ShutdownAll()
	err := reg.Add(context.Background(), config.Driver{
		Name:   "deye",
		Driver: "deye@3.1.1",
	})
	if err == nil || !strings.Contains(err.Error(), "no driver-registry resolver") {
		t.Fatalf("want no-resolver error, got %v", err)
	}
}

func TestAddRefusedOnResolveFailure(t *testing.T) {
	reg := NewRegistry(telemetry.NewStore())
	reg.ResolveDriverRef = func(ref string) (string, error) {
		return "", errors.New("cache miss + fetch failed")
	}
	defer reg.ShutdownAll()
	err := reg.Add(context.Background(), config.Driver{
		Name:   "deye",
		Driver: "deye@3.1.1",
	})
	if err == nil || !strings.Contains(err.Error(), "deye@3.1.1") {
		t.Fatalf("want resolve error carrying the ref, got %v", err)
	}
	if names := reg.Names(); len(names) != 0 {
		t.Errorf("driver registered despite resolve failure: %v", names)
	}
}

// M1: EVERY Add refusal must land in driver health so /api/drivers and
// /api/status show why the driver is absent. Exercises the resolve-fail,
// missing-source, load-fail, capability-fail, and driver_init-fail
// returns.
func TestAddFailuresRecordedInDriverHealth(t *testing.T) {
	lastError := func(tel *telemetry.Store, name string) string {
		t.Helper()
		h := tel.DriverHealth(name)
		if h == nil {
			t.Fatalf("no health record for %q — Add failure not surfaced", name)
		}
		return h.LastError
	}

	t.Run("resolve failure", func(t *testing.T) {
		tel := telemetry.NewStore()
		reg := NewRegistry(tel)
		reg.ResolveDriverRef = func(ref string) (string, error) {
			return "", errors.New("cache miss + fetch failed")
		}
		if err := reg.Add(context.Background(), config.Driver{Name: "d", Driver: "deye@1.0.0"}); err == nil {
			t.Fatal("want error")
		}
		if got := lastError(tel, "d"); !strings.Contains(got, "cache miss") {
			t.Errorf("LastError = %q, want resolve failure surfaced", got)
		}
	})

	t.Run("missing source", func(t *testing.T) {
		tel := telemetry.NewStore()
		reg := NewRegistry(tel)
		if err := reg.Add(context.Background(), config.Driver{Name: "d"}); err == nil {
			t.Fatal("want error")
		}
		if got := lastError(tel, "d"); !strings.Contains(got, "must specify") {
			t.Errorf("LastError = %q, want missing-source error", got)
		}
	})

	t.Run("lua load failure", func(t *testing.T) {
		tel := telemetry.NewStore()
		reg := NewRegistry(tel)
		path := filepath.Join(t.TempDir(), "broken.lua")
		if err := os.WriteFile(path, []byte("function driver_init( -- syntax error"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := reg.Add(context.Background(), config.Driver{Name: "d", Lua: path}); err == nil {
			t.Fatal("want error")
		}
		if got := lastError(tel, "d"); !strings.Contains(got, "load lua") {
			t.Errorf("LastError = %q, want lua load error", got)
		}
	})

	t.Run("capability failure", func(t *testing.T) {
		tel := telemetry.NewStore()
		reg := NewRegistry(tel)
		reg.MQTTFactory = func(name string, c *config.MQTTConfig) (MQTTCap, error) {
			return nil, errors.New("broker unreachable")
		}
		path := writeRefTestDriver(t)
		err := reg.Add(context.Background(), config.Driver{
			Name: "d", Lua: path,
			Capabilities: config.Capabilities{MQTT: &config.MQTTConfig{Host: "h", Port: 1883}},
		})
		if err == nil {
			t.Fatal("want error")
		}
		if got := lastError(tel, "d"); !strings.Contains(got, "broker unreachable") {
			t.Errorf("LastError = %q, want capability failure surfaced", got)
		}
	})

	t.Run("driver_init failure", func(t *testing.T) {
		tel := telemetry.NewStore()
		reg := NewRegistry(tel)
		path := filepath.Join(t.TempDir(), "initfail.lua")
		src := `
DRIVER_MANIFEST = { name = "initfail", version = "0.0.0", role = "meter" }
function driver_init(config) error("device said no") end
function driver_poll() end
`
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := reg.Add(context.Background(), config.Driver{Name: "d", Lua: path}); err == nil {
			t.Fatal("want error")
		}
		if got := lastError(tel, "d"); !strings.Contains(got, "driver_init") {
			t.Errorf("LastError = %q, want driver_init failure surfaced", got)
		}
	})
}

// L4 support: Has reflects the running set only — a failed Add leaves a
// health record but must not count as running (the watchdog loops use
// Has to skip SendDefault for absent drivers).
func TestRegistryHas(t *testing.T) {
	reg := NewRegistry(telemetry.NewStore())
	luaPath := writeRefTestDriver(t)
	if reg.Has("d") {
		t.Error("Has before Add = true")
	}
	if err := reg.Add(context.Background(), config.Driver{Name: "d", Lua: luaPath}); err != nil {
		t.Fatal(err)
	}
	if !reg.Has("d") {
		t.Error("Has after Add = false")
	}
	reg.Remove("d")
	if reg.Has("d") {
		t.Error("Has after Remove = true")
	}
	// Failed Add (missing source) → health record exists, Has stays false.
	_ = reg.Add(context.Background(), config.Driver{Name: "failed"})
	if reg.Has("failed") {
		t.Error("Has = true for a driver whose Add failed")
	}
}

// A changed (or added/removed) registry ref must restart the driver on
// hot-reload — sameDriverConfig has to compare the ref exactly like it
// compares the Lua path.
func TestSameDriverConfigComparesRef(t *testing.T) {
	a := config.Driver{Name: "deye", Driver: "deye@3.1.1"}
	b := config.Driver{Name: "deye", Driver: "deye@3.1.2"}
	if sameDriverConfig(a, b) {
		t.Error("version bump in ref not detected as config change")
	}
	if !sameDriverConfig(a, a) {
		t.Error("identical ref detected as change")
	}
	c := config.Driver{Name: "deye", Lua: "drivers/deye.lua"}
	if sameDriverConfig(a, c) {
		t.Error("ref→lua switch not detected as config change")
	}
}
