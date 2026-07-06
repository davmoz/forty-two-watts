package drivers

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CatalogEntry describes one available driver discovered in the drivers
// directory: its parsed DRIVER_MANIFEST plus where the file lives. The
// embedded Manifest is the single source of truth for driver metadata —
// there is no separate regex-scraped DRIVER block anymore.
type CatalogEntry struct {
	Path     string `json:"path"`     // portable config lua path, e.g. "drivers/ferroamp.lua"
	Filename string `json:"filename"` // e.g. "ferroamp.lua"
	ID       string `json:"id"`       // file stem, e.g. "ferroamp"
	Manifest
}

// LoadCatalog scans dir (and any direct sub-directories) for .lua driver
// files and parses their DRIVER_MANIFEST tables.
func LoadCatalog(dir string) ([]CatalogEntry, error) {
	return LoadCatalogMulti(dir)
}

// LoadCatalogMulti scans one or more directories for .lua driver files and
// merges the results. Directories are scanned in order; when the same
// filename appears in more than one directory the first occurrence wins
// (earlier dirs take precedence). This allows a "user" directory passed
// first to shadow bundled drivers of the same name.
//
// Directories that don't exist or can't be read are silently skipped so
// callers don't need to guard against an empty user-drivers dir. Files
// whose manifest fails to parse are skipped with a warning — one broken
// driver must not blank the whole catalog.
func LoadCatalogMulti(dirs ...string) ([]CatalogEntry, error) {
	seen := make(map[string]struct{}) // keyed by Filename (e.g. "ferroamp.lua")
	var out []CatalogEntry

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			continue // missing or inaccessible — skip silently
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable, don't fail the whole scan
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".lua") {
				return nil
			}
			filename := filepath.Base(path)
			if _, exists := seen[filename]; exists {
				return nil // earlier dir already claimed this name
			}
			m, err := LoadManifest(path)
			if err != nil {
				slog.Warn("driver manifest unreadable, skipping from catalog", "path", path, "err", err)
				return nil
			}
			normalizeCatalogVerification(m)
			rel, _ := filepath.Rel(dir, path)
			entry := CatalogEntry{
				Path:     filepath.ToSlash(filepath.Join("drivers", rel)),
				Filename: filename,
				ID:       strings.TrimSuffix(filename, ".lua"),
				Manifest: *m,
			}
			seen[filename] = struct{}{}
			out = append(out, entry)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", dir, err)
		}
	}

	// Stable sort by display name (then filename as tiebreaker).
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].catalogSortName(), out[j].catalogSortName()
		if a == b {
			return out[i].Filename < out[j].Filename
		}
		if a == "" {
			return false
		}
		if b == "" {
			return true
		}
		return a < b
	})
	return out, nil
}

func (e CatalogEntry) catalogSortName() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Name
}

// normalizeCatalogVerification coerces verification.status into one of
// the three canonical values the UI renders badges for. Anything else
// (absent, typo, unknown) falls back to "experimental" — the safest
// default for a driver with no declared provenance.
func normalizeCatalogVerification(m *Manifest) {
	if m.Verification == nil {
		m.Verification = &ManifestVerification{Status: "experimental"}
		return
	}
	m.Verification.Status = normalizeVerificationStatus(m.Verification.Status)
}

func normalizeVerificationStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "production":
		return "production"
	case "beta":
		return "beta"
	default:
		return "experimental"
	}
}
