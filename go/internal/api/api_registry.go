// Sourceful driver-registry proxy: browse the public registry (driver
// list + per-driver versions) from the settings UI without CORS games,
// with a 5-minute in-process TTL cache in front — the registry only
// changes when someone publishes a driver, and on a tunnelled UI the
// upstream round-trip dominates page-load time. POST /api/registry/
// refresh gives the operator a flush button ("I just published 2.3.10").
//
// Endpoints (nil Deps.DriverRegistry → 503):
//
//	GET  /api/registry/drivers                 — registry index
//	GET  /api/registry/drivers/{name}/versions — published versions
//	POST /api/registry/refresh                 — flush the TTL cache
//
// GET /api/registry/drivers/{name}/{version}/manifest (fetch source via
// Client.Source → drivers.ParseManifest → manifest JSON) intentionally
// does NOT ship in this file yet: drivers.ParseManifest lands with the
// PR1 manifest-core branch. The integrator wires that endpoint after
// both PRs merge — see docs/driver-registry.md ("API endpoints").
package api

import (
	"net/http"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/driverregistry"
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
