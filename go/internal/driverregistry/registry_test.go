package driverregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		ref         string
		wantName    string
		wantVersion string
		wantErr     bool
	}{
		{"deye@3.1.1", "deye", "3.1.1", false},
		{"sungrow@0.1.0-rc1", "sungrow", "0.1.0-rc1", false},
		// Missing @ — no implicit latest, ever.
		{"deye", "", "", true},
		// Empty version.
		{"deye@", "", "", true},
		// Empty name.
		{"@3.1.1", "", "", true},
		// Empty everything.
		{"", "", "", true},
		{"@", "", "", true},
		// Path-hostile parts must not reach the cache filename.
		{"../evil@1.0.0", "", "", true},
		{"deye@../../etc", "", "", true},
		{`de\ye@1.0.0`, "", "", true},
	}
	for _, tc := range cases {
		name, version, err := ParseRef(tc.ref)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRef(%q): want error, got (%q, %q)", tc.ref, name, version)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q): unexpected error: %v", tc.ref, err)
			continue
		}
		if name != tc.wantName || version != tc.wantVersion {
			t.Errorf("ParseRef(%q) = (%q, %q), want (%q, %q)", tc.ref, name, version, tc.wantName, tc.wantVersion)
		}
	}
}

// deyeTestSource is a valid registry driver: registry drivers are
// manifest-mandatory, so the fake must serve one that passes
// drivers.ParseManifest.
const deyeTestSource = "-- deye driver\n" +
	`DRIVER_MANIFEST = { name = "deye", version = "3.1.1", role = "battery" }` + "\n" +
	"function driver_poll() end\n"

// fakeRegistry serves a minimal registry: index, versions, and one
// driver source. hits counts network round-trips so cache-hit tests
// can assert zero.
func fakeRegistry(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":2,"drivers":[
			{"id":"d1","name":"deye","display_name":"Deye SUN-xK","tier":"gold","is_active":true,"author":"sourceful","latest_version":"3.1.1"},
			{"id":"d2","name":"sungrow","display_name":"Sungrow SHxRT","tier":"silver","is_active":true,"author":"sourceful","latest_version":"1.0.0"}
		]}`))
	})
	mux.HandleFunc("GET /deye/versions", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"versions":[
			{"version":"3.1.1","size_bytes":12345,"min_host_version":"0.9.0"},
			{"version":"3.1.0","size_bytes":12000}
		]}`))
	})
	mux.HandleFunc("GET /deye/3.1.1", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(deyeTestSource))
	})
	mux.HandleFunc("GET /empty/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 200 with an empty body — must be rejected.
	})
	mux.HandleFunc("GET /garbage/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// A proxy/captive portal serving HTML with a 200 — not a driver.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>sign in to continue</body></html>"))
	})
	mux.HandleFunc("GET /huge/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// One byte over the 2 MB cap — must be detected, not truncated.
		_, _ = w.Write(make([]byte, 2<<20+1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveFetchesAndCaches(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	cacheDir := filepath.Join(t.TempDir(), "driver-cache")
	c := New(srv.URL, cacheDir)

	path, err := c.Resolve(context.Background(), "deye@3.1.1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(cacheDir, "deye-3.1.1.lua"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached: %v", err)
	}
	if !strings.Contains(string(body), "driver_poll") {
		t.Errorf("cached body = %q, want lua source", body)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("network hits = %d, want 1", got)
	}
	// No leftover .tmp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file left behind after rename")
	}
}

func TestResolveCacheHitSkipsNetwork(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	cacheDir := t.TempDir()
	c := New(srv.URL, cacheDir)

	// Pre-warm the cache by hand, then kill the server: a hit must
	// never touch the network.
	warm := filepath.Join(cacheDir, "deye-3.1.1.lua")
	if err := os.WriteFile(warm, []byte("-- warm cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	path, err := c.Resolve(context.Background(), "deye@3.1.1")
	if err != nil {
		t.Fatalf("Resolve with warm cache: %v", err)
	}
	if path != warm {
		t.Errorf("path = %q, want %q", path, warm)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("network hits = %d, want 0 (cache-first)", got)
	}
}

func TestResolveRejectsEmptyBody(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	if _, err := c.Resolve(context.Background(), "empty@1.0.0"); err == nil {
		t.Fatal("Resolve of empty body: want error, got nil")
	}
	// The failed fetch must not poison the cache.
	if _, err := os.Stat(c.CachePath("empty", "1.0.0")); !os.IsNotExist(err) {
		t.Error("empty body was cached")
	}
}

// H4: a 200 body that isn't a valid driver (HTML splash, garbage) is
// refused BEFORE caching, and the next Resolve retries the fetch
// instead of serving a poisoned cache entry.
func TestResolveRejectsGarbageBodyAndRetries(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := c.Resolve(context.Background(), "garbage@1.0.0")
		if err == nil {
			t.Fatalf("attempt %d: Resolve of HTML body: want error, got nil", attempt)
		}
		if !strings.Contains(err.Error(), "validation") {
			t.Errorf("attempt %d: error = %v, want manifest-validation refusal", attempt, err)
		}
	}
	if _, err := os.Stat(c.CachePath("garbage", "1.0.0")); !os.IsNotExist(err) {
		t.Error("garbage body was cached")
	}
	// Both attempts hit the network — nothing was cached in between.
	if got := hits.Load(); got != 2 {
		t.Errorf("network hits = %d, want 2 (failed fetch must not cache)", got)
	}
	// No stray temp files left behind.
	entries, _ := os.ReadDir(c.CacheDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// H4: a body at exactly limit+1 bytes is a truncation risk — refused,
// never cached.
func TestResolveRejectsOversizeBody(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	_, err := c.Resolve(context.Background(), "huge@1.0.0")
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("Resolve of oversize body = %v, want byte-limit error", err)
	}
	if _, err := os.Stat(c.CachePath("huge", "1.0.0")); !os.IsNotExist(err) {
		t.Error("oversize body was cached")
	}
}

// H4: concurrent Resolves of the same uncached ref must all succeed and
// leave one intact cached file (unique temp files + atomic rename).
func TestResolveConcurrentSameRef(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, filepath.Join(t.TempDir(), "cache"))

	const n = 8
	errs := make(chan error, n)
	paths := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			p, err := c.Resolve(context.Background(), "deye@3.1.1")
			paths <- p
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Resolve: %v", err)
		}
	}
	want := c.CachePath("deye", "3.1.1")
	for i := 0; i < n; i++ {
		if p := <-paths; p != want {
			t.Errorf("path = %q, want %q", p, want)
		}
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != deyeTestSource {
		t.Errorf("cached body corrupted:\n%s", body)
	}
	entries, _ := os.ReadDir(c.CacheDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// L2: hand-edited configs pick up stray whitespace around the ref.
func TestParseRefTrimsWhitespace(t *testing.T) {
	for _, ref := range []string{" deye@3.1.1", "deye@3.1.1 ", " deye @ 3.1.1 "} {
		name, version, err := ParseRef(ref)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", ref, err)
			continue
		}
		if name != "deye" || version != "3.1.1" {
			t.Errorf("ParseRef(%q) = (%q, %q), want (deye, 3.1.1)", ref, name, version)
		}
	}
	// Whitespace-only sides are still empty → error.
	if _, _, err := ParseRef("deye@ "); err == nil {
		t.Error("ParseRef(\"deye@ \"): want error for empty version")
	}
}

func TestResolveFetchFailureCacheMiss(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	_, err := c.Resolve(context.Background(), "nosuch@9.9.9")
	if err == nil {
		t.Fatal("Resolve of unknown driver: want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want status 404 surfaced", err)
	}
}

func TestList(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	idx, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if idx.Count != 2 || len(idx.Drivers) != 2 {
		t.Fatalf("index = %+v, want count=2 drivers=2", idx)
	}
	d := idx.Drivers[0]
	if d.Name != "deye" || d.DisplayName != "Deye SUN-xK" || d.Tier != "gold" || d.LatestVersion != "3.1.1" {
		t.Errorf("driver[0] = %+v", d)
	}
}

func TestVersionsWrappedAndBare(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	vs, err := c.Versions(context.Background(), "deye")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 2 || vs[0].Version != "3.1.1" || vs[0].MinHostVersion != "0.9.0" {
		t.Errorf("versions = %+v", vs)
	}

	// Bare-array shape.
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"version":"1.0.0"}]`))
	}))
	defer bare.Close()
	vs, err = New(bare.URL, t.TempDir()).Versions(context.Background(), "x")
	if err != nil {
		t.Fatalf("Versions bare: %v", err)
	}
	if len(vs) != 1 || vs[0].Version != "1.0.0" {
		t.Errorf("bare versions = %+v", vs)
	}
}

func TestSourceRawLua(t *testing.T) {
	var hits atomic.Int64
	srv := fakeRegistry(t, &hits)
	c := New(srv.URL, t.TempDir())

	body, err := c.Source(context.Background(), "deye", "3.1.1")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if !strings.HasPrefix(string(body), "-- deye driver") {
		t.Errorf("body = %q", body)
	}
}
