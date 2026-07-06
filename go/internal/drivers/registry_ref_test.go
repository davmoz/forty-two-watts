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
