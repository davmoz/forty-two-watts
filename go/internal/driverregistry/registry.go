// Package driverregistry resolves pinned driver references
// ("name@version", e.g. "deye@3.1.1") against the Sourceful Novacore
// device-support registry and caches the fetched Lua source locally.
//
// Design rules (mirrors blixt-gateway's l1 driver_registry):
//
//   - Pinning is mandatory. A ref without an explicit "@version" is a
//     parse error — there is no implicit "latest" at the runtime layer.
//     Registry versions are immutable, so a pinned ref + a warm cache
//     is fully deterministic and works offline.
//   - Cache-first. Resolve checks {cache_dir}/{name}-{version}.lua
//     before touching the network; a hit costs zero round-trips.
//   - Atomic writes. Fetched sources land via .tmp + rename so a
//     crashed fetch can never leave a truncated driver in the cache.
//
// The base URL defaults per-net to
// https://novacore-{net}.sourceful.dev/device-support/drivers; the
// operator picks the net (or an explicit URL) in config.driver_registry
// and the DRIVER_REGISTRY_URL env var overrides both (resolution lives
// in config.DriverRegistryBaseURL). See docs/driver-registry.md.
package driverregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/driverref"
	"github.com/frahlg/forty-two-watts/go/internal/drivers"
)

// fetchTimeout bounds every registry round-trip. The registry is a
// public CDN-fronted read path; anything slower than this is down.
const fetchTimeout = 10 * time.Second

// maxSourceBytes caps a fetched driver body. The largest bundled
// driver is ~50 KB; 2 MB is two orders of magnitude of headroom while
// still refusing to spool a runaway response to disk.
const maxSourceBytes = 2 << 20

// ParseRef splits a pinned driver reference "name@version" into its
// parts (whitespace-trimmed; "@" mandatory, both sides non-empty,
// path-hostile characters rejected). The parser lives in the
// dependency-free driverref package so the config validator can share
// it without importing this package (which now imports drivers for
// manifest validation — config → driverregistry would cycle).
func ParseRef(ref string) (name, version string, err error) {
	return driverref.Parse(ref)
}

// RegistryDriver is one entry in the registry index (GET {base}).
// Unknown upstream fields are dropped; the ones below are what the
// settings UI renders. Devices stays raw because the upstream shape
// is registry-defined and only ever passed through.
type RegistryDriver struct {
	ID            string          `json:"id,omitempty"`
	Name          string          `json:"name"`
	DisplayName   string          `json:"display_name,omitempty"`
	Tier          string          `json:"tier,omitempty"`
	IsActive      bool            `json:"is_active,omitempty"`
	Author        string          `json:"author,omitempty"`
	LatestVersion string          `json:"latest_version,omitempty"`
	Description   string          `json:"description,omitempty"`
	Devices       json.RawMessage `json:"devices,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

// RegistryIndex is the JSON body of GET {base}.
type RegistryIndex struct {
	Count   int              `json:"count"`
	Drivers []RegistryDriver `json:"drivers"`
}

// RegistryVersion is one published version of a driver
// (GET {base}/{name}/versions).
type RegistryVersion struct {
	Version        string          `json:"version"`
	Protocols      []string        `json:"protocols,omitempty"`
	Ders           []string        `json:"ders,omitempty"`
	Capabilities   []string        `json:"capabilities,omitempty"`
	SizeBytes      int64           `json:"size_bytes,omitempty"`
	MinHostVersion string          `json:"min_host_version,omitempty"`
	Changelog      string          `json:"changelog,omitempty"`
	IsActive       bool            `json:"is_active,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	Extra          json.RawMessage `json:"-"`
}

// Client fetches drivers from one registry base URL and caches them
// under CacheDir. Safe for concurrent use — it holds no mutable state
// beyond the http.Client.
type Client struct {
	// BaseURL is the registry root, e.g.
	// https://novacore-devnet.sourceful.dev/device-support/drivers
	// (no trailing slash).
	BaseURL string
	// CacheDir holds fetched sources as {name}-{version}.lua.
	CacheDir string
	// HTTPClient carries the 10 s timeout. Never nil after New.
	HTTPClient *http.Client
}

// New builds a Client for the given registry base URL and cache dir.
func New(baseURL, cacheDir string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		CacheDir:   cacheDir,
		HTTPClient: &http.Client{Timeout: fetchTimeout},
	}
}

// CachePath returns where a ref's source lives (or would live) in the
// local cache. Does not touch the network or the filesystem.
func (c *Client) CachePath(name, version string) string {
	return filepath.Join(c.CacheDir, name+"-"+version+".lua")
}

// Resolve turns a pinned ref into a local file path containing the Lua
// source. Cache hit = zero network. Cache miss fetches from the
// registry and writes atomically (.tmp + rename), so an offline host
// with a warm cache keeps working and a crashed fetch never leaves a
// truncated driver behind.
func (c *Client) Resolve(ctx context.Context, ref string) (string, error) {
	name, version, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	cachePath := c.CachePath(name, version)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	body, err := c.Source(ctx, name, version)
	if err != nil {
		return "", err
	}
	// Validate BEFORE caching: registry drivers are manifest-mandatory,
	// so an HTML error page, captive-portal splash, or truncated body
	// served with a 200 must never land in the cache — a cache hit is
	// trusted forever and never re-validated.
	if _, err := drivers.ParseManifest(string(body)); err != nil {
		return "", fmt.Errorf("registry driver %s@%s failed validation (not cached): %w", name, version, err)
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create driver cache dir %s: %w", c.CacheDir, err)
	}
	// Unique temp file per fetch so concurrent Resolves of the same ref
	// can't interleave writes into one .tmp; each renames its own
	// complete copy into place.
	tmp, err := os.CreateTemp(c.CacheDir, name+"-"+version+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp in %s: %w", c.CacheDir, err)
	}
	// CreateTemp defaults to 0600; keep the cache world-readable like
	// the bundled drivers.
	_ = tmp.Chmod(0o644)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("rename %s -> %s: %w", tmp.Name(), cachePath, err)
	}
	slog.Info("driver registry: fetched", "ref", ref, "path", cachePath, "bytes", len(body))
	return cachePath, nil
}

// Source fetches one driver version's raw Lua source
// (GET {base}/{name}/{version} — text/plain, not JSON). An empty body
// is rejected: the registry never publishes empty drivers, so an empty
// 200 is a broken proxy, not a driver.
func (c *Client) Source(ctx context.Context, name, version string) ([]byte, error) {
	body, err := c.get(ctx, url.PathEscape(name)+"/"+url.PathEscape(version))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("registry returned empty body for %s@%s", name, version)
	}
	return body, nil
}

// List fetches the registry index (GET {base}).
func (c *Client) List(ctx context.Context) (*RegistryIndex, error) {
	body, err := c.get(ctx, "")
	if err != nil {
		return nil, err
	}
	var idx RegistryIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("registry index: bad JSON: %w", err)
	}
	return &idx, nil
}

// Versions fetches the published versions of one driver
// (GET {base}/{name}/versions). Accepts both a bare JSON array and a
// {"versions": [...]} wrapper so a registry-side envelope change
// doesn't break pinned installs.
func (c *Client) Versions(ctx context.Context, name string) ([]RegistryVersion, error) {
	body, err := c.get(ctx, url.PathEscape(name)+"/versions")
	if err != nil {
		return nil, err
	}
	var bare []RegistryVersion
	if err := json.Unmarshal(body, &bare); err == nil {
		return bare, nil
	}
	var wrapped struct {
		Versions []RegistryVersion `json:"versions"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Versions != nil {
		return wrapped.Versions, nil
	}
	return nil, fmt.Errorf("registry versions for %q: unrecognized JSON shape", name)
}

// get performs one registry GET. path is relative to BaseURL ("" = the
// index). Non-200 statuses become errors carrying the status code.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	u := c.BaseURL
	if path != "" {
		u += "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("registry request %s: %w", u, err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry fetch %s: %w", u, err)
	}
	defer resp.Body.Close()
	// Read one byte past the cap so an oversized body is DETECTED, not
	// silently truncated — a truncated driver source must never be
	// mistaken for a complete one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("registry read %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry fetch %s: status %d: %s", u, resp.StatusCode, truncate(body, 200))
	}
	if len(body) > maxSourceBytes {
		return nil, fmt.Errorf("registry fetch %s: body exceeds %d-byte limit", u, maxSourceBytes)
	}
	return body, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
