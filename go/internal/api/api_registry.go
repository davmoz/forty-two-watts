// Sourceful driver-registry proxy: browse the public registry (driver
// list + per-driver versions) from the settings UI without CORS games,
// with a 5-minute in-process TTL cache in front — the registry only
// changes when someone publishes a driver, and on a tunnelled UI the
// upstream round-trip dominates page-load time. POST /api/registry/
// refresh gives the operator a flush button ("I just published 2.3.10").
//
// Endpoints (nil Deps.DriverRegistry → 503):
//
//	GET  /api/registry/drivers                            — registry index
//	GET  /api/registry/drivers/{name}/versions            — published versions
//	GET  /api/registry/drivers/{name}/{version}/manifest  — parsed DRIVER_MANIFEST
//	POST /api/registry/refresh                            — flush the TTL cache
package api

import (
	"net/http"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/driverregistry"
	"github.com/frahlg/forty-two-watts/go/internal/drivers"
)

// registryCacheTTL — see the file comment for why 5 minutes.
const registryCacheTTL = 5 * time.Minute

type registryCacheEntry struct {
	expires time.Time
	payload any
}

// registryCached returns the cached payload for key, or calls fetch and
// caches the result. Only successful fetches are cached, so a registry
// outage retries on the next request instead of pinning an error for
// five minutes.
func (s *Server) registryCached(key string, fetch func() (any, error)) (any, error) {
	s.registryCacheMu.Lock()
	if e, ok := s.registryCache[key]; ok && time.Now().Before(e.expires) {
		s.registryCacheMu.Unlock()
		return e.payload, nil
	}
	s.registryCacheMu.Unlock()

	payload, err := fetch()
	if err != nil {
		return nil, err
	}
	s.registryCacheMu.Lock()
	if s.registryCache == nil {
		s.registryCache = make(map[string]registryCacheEntry)
	}
	s.registryCache[key] = registryCacheEntry{expires: time.Now().Add(registryCacheTTL), payload: payload}
	s.registryCacheMu.Unlock()
	return payload, nil
}

func (s *Server) registryClient(w http.ResponseWriter) *driverregistry.Client {
	if s.deps.DriverRegistry == nil {
		writeJSON(w, 503, map[string]string{"error": "driver registry not configured"})
		return nil
	}
	return s.deps.DriverRegistry
}

// GET /api/registry/drivers — the registry index (list of publishable
// drivers with display_name / tier / latest_version for the UI picker).
func (s *Server) handleRegistryDrivers(w http.ResponseWriter, r *http.Request) {
	c := s.registryClient(w)
	if c == nil {
		return
	}
	payload, err := s.registryCached("drivers", func() (any, error) {
		return c.List(r.Context())
	})
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, payload)
}

// GET /api/registry/drivers/{name}/versions — published versions of one
// driver, newest-first as delivered by the registry.
func (s *Server) handleRegistryDriverVersions(w http.ResponseWriter, r *http.Request) {
	c := s.registryClient(w)
	if c == nil {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "missing driver name"})
		return
	}
	payload, err := s.registryCached("versions:"+name, func() (any, error) {
		vs, err := c.Versions(r.Context(), name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"name": name, "versions": vs}, nil
	})
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, payload)
}

// GET /api/registry/drivers/{name}/{version}/manifest — fetch the pinned
// driver source from the registry and return its parsed DRIVER_MANIFEST
// as JSON. This is what the settings UI renders the per-driver config
// form from (requires/options field schema).
func (s *Server) handleRegistryDriverManifest(w http.ResponseWriter, r *http.Request) {
	c := s.registryClient(w)
	if c == nil {
		return
	}
	name, version := r.PathValue("name"), r.PathValue("version")
	if name == "" || version == "" {
		writeJSON(w, 400, map[string]string{"error": "missing driver name or version"})
		return
	}
	payload, err := s.registryCached(registryManifestCacheKey(name+"@"+version), func() (any, error) {
		src, err := c.Source(r.Context(), name, version)
		if err != nil {
			return nil, err
		}
		man, err := drivers.ParseManifest(string(src))
		if err != nil {
			return nil, err
		}
		return man, nil
	})
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, payload)
}

// POST /api/registry/refresh — drop every cached registry response so
// the next GET re-fetches upstream. Returns the number of entries
// cleared (UI shows it as confirmation).
func (s *Server) handleRegistryRefresh(w http.ResponseWriter, r *http.Request) {
	if s.registryClient(w) == nil {
		return
	}
	s.registryCacheMu.Lock()
	n := len(s.registryCache)
	s.registryCache = nil
	s.registryCacheMu.Unlock()
	writeJSON(w, 200, map[string]int{"cleared": n})
}
