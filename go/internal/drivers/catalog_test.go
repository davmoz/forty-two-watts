package drivers

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCatalogDriver(t *testing.T, dir, filename, name string) {
	t.Helper()
	src := "DRIVER_MANIFEST = {\n  name = \"" + name + "\",\n  version = \"0.1.0\",\n  role = \"meter\",\n}\n"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCatalogUsesPortableDriverPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-driver-dir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeCatalogDriver(t, dir, "custom.lua", "custom")

	entries, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("LoadCatalog returned %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Path != "drivers/custom.lua" {
		t.Fatalf("catalog path = %q, want portable drivers/custom.lua", entries[0].Path)
	}
	if entries[0].ID != "custom" {
		t.Fatalf("catalog id = %q, want file stem custom", entries[0].ID)
	}
}

func TestLoadCatalogMultiUnionAndFirstWins(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "user-drivers")
	bundledDir := filepath.Join(t.TempDir(), "bundled-drivers")
	if err := os.Mkdir(userDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bundledDir, 0755); err != nil {
		t.Fatal(err)
	}

	// shared.lua exists in both dirs — user version should win.
	writeCatalogDriver(t, userDir, "shared.lua", "shared_user")
	writeCatalogDriver(t, bundledDir, "shared.lua", "shared_bundled")

	// bundled.lua only in bundled dir.
	writeCatalogDriver(t, bundledDir, "bundled.lua", "bundled_only")

	entries, err := LoadCatalogMulti(userDir, bundledDir)
	if err != nil {
		t.Fatalf("LoadCatalogMulti: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}

	byFilename := make(map[string]CatalogEntry)
	for _, e := range entries {
		byFilename[e.Filename] = e
	}

	if e, ok := byFilename["shared.lua"]; !ok {
		t.Fatal("shared.lua missing from catalog")
	} else if e.Name != "shared_user" {
		t.Errorf("shared.lua: want name=shared_user (user wins), got %q", e.Name)
	}

	if _, ok := byFilename["bundled.lua"]; !ok {
		t.Fatal("bundled.lua missing from catalog (union should include it)")
	}
}

func TestLoadCatalogMultiMissingDirSkipped(t *testing.T) {
	bundledDir := filepath.Join(t.TempDir(), "bundled")
	if err := os.Mkdir(bundledDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeCatalogDriver(t, bundledDir, "drv.lua", "x")

	// nonexistent is fine — should be silently skipped.
	entries, err := LoadCatalogMulti("/nonexistent/path/that/does/not/exist", bundledDir)
	if err != nil {
		t.Fatalf("LoadCatalogMulti: unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry from bundledDir, got %d", len(entries))
	}
}

// A driver whose manifest fails to parse must be skipped, not blank the
// whole catalog.
func TestLoadCatalogSkipsMalformedManifest(t *testing.T) {
	dir := t.TempDir()
	writeCatalogDriver(t, dir, "good.lua", "good")
	if err := os.WriteFile(filepath.Join(dir, "broken.lua"),
		[]byte("DRIVER_MANIFEST = { name = \"broken\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "good" {
		t.Fatalf("want only the good entry, got %+v", entries)
	}
}
