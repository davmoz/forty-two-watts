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
		_, _ = w.Write([]byte("-- deye driver\nfunction driver_poll() end\n"))
	})
	mux.HandleFunc("GET /empty/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 200 with an empty body — must be rejected.
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
