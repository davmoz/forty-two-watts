// Manifest validation for POST /api/config.
//
// Drivers declare a typed config schema in their DRIVER_MANIFEST
// (requires/options fields with type + inclusive bounds). Registry.Add
// enforces it at load time, but that happens asynchronously after the
// config save — the operator would only find out from the logs. This
// file runs the same validation synchronously in the POST /api/config
// path so the settings UI can reject the save with per-field messages
// ("field \"host\" is missing …") the form renders inline.
//
// Best-effort by design: a driver whose manifest can't be resolved
// locally (registry ref not yet cached, unreadable file) is skipped
// here and re-validated by Registry.Add at load time.
package api

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/frahlg/forty-two-watts/go/internal/config"
	"github.com/frahlg/forty-two-watts/go/internal/drivers"
)

// driverManifestErrors validates each enabled driver's `config:` map
// against its DRIVER_MANIFEST. Returns one human-readable error per
// failed field, prefixed with the driver name so the UI can route the
// message to the right card: `driver "sonnen": required field "host" is
// missing — …`.
func (s *Server) driverManifestErrors(cfg *config.Config) []string {
	manifests := s.catalogManifestsByPath()
	var errs []string
	for i := range cfg.Drivers {
		d := &cfg.Drivers[i]
		if d.Disabled {
			continue // not loaded; don't block the save on a parked driver
		}
		man := s.manifestForDriver(d, manifests)
		if man == nil {
			continue // unresolvable here — Registry.Add validates at load
		}
		for _, e := range man.ValidateConfig(d.Config, d.TelemetryOnly) {
			errs = append(errs, fmt.Sprintf("driver %q: %s", d.Name, e))
		}
	}
	return errs
}

// catalogManifestsByPath maps the portable config lua path
// ("drivers/sonnen.lua") to its parsed manifest. Nil on catalog read
// errors — callers skip validation (fail-open, same policy as
// driverSecretKeys).
func (s *Server) catalogManifestsByPath() map[string]*drivers.Manifest {
	dir := s.deps.DriverDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(s.deps.ConfigPath), "drivers")
	}
	entries, err := drivers.LoadCatalogMulti(s.deps.UserDriverDir, dir)
	if err != nil {
		return nil
	}
	out := make(map[string]*drivers.Manifest, len(entries))
	for i := range entries {
		out[filepath.ToSlash(entries[i].Path)] = &entries[i].Manifest
	}
	return out
}

// manifestForDriver resolves the manifest for one config.Driver entry:
// `lua:` paths through the local catalog, `driver:` registry refs
// through the API's registry TTL cache (populated when the UI fetched
// the manifest to render the form — no network round-trip on save).
func (s *Server) manifestForDriver(d *config.Driver, byPath map[string]*drivers.Manifest) *drivers.Manifest {
	if d.Lua != "" {
		return byPath[filepath.ToSlash(d.Lua)]
	}
	if d.Driver == "" {
		return nil
	}
	s.registryCacheMu.Lock()
	defer s.registryCacheMu.Unlock()
	e, ok := s.registryCache[registryManifestCacheKey(d.Driver)]
	if !ok {
		return nil
	}
	man, _ := e.payload.(*drivers.Manifest)
	return man
}

// registryManifestCacheKey mirrors the key handleRegistryDriverManifest
// caches under ("manifest:<name>@<version>").
func registryManifestCacheKey(ref string) string {
	return "manifest:" + strings.TrimSpace(ref)
}
