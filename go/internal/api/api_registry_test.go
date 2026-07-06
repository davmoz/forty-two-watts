package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/frahlg/forty-two-watts/go/internal/driverregistry"
	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

// newRegistryTestServer stands up an API server whose DriverRegistry
// client points at a fake upstream registry. hits counts upstream
// round-trips so TTL-cache behaviour is observable.
func newRegistryTestServer(t *testing.T, hits *atomic.Int64) *Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"count":1,"drivers":[{"id":"d1","name":"deye","display_name":"Deye","tier":"gold","is_active":true,"latest_version":"3.1.1"}]}`))
	})
	mux.HandleFunc("GET /deye/versions", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"versions":[{"version":"3.1.1"},{"version":"3.1.0"}]}`))
	})
	mux.HandleFunc("GET /deye/3.1.1", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`DRIVER_MANIFEST = {
			name = "deye", version = "3.1.1", role = "battery",
			requires = {
				{ name = "battery_capacity_wh", purpose = "control",
				  type = "integer", min = 1000, max = 1000000,
				  help = "Usable capacity in Wh." },
			},
		}
		function driver_init(config) end
		function driver_poll() end`))
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	client := driverregistry.New(upstream.URL, t.TempDir())
	return New(&Deps{
		Tel:            telemetry.NewStore(),
		DriverRegistry: client,
		Version:        "test",
	})
}

func doJSON(t *testing.T, srv *Server, method, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var body map[string]any
	if len(w.Body.Bytes()) > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s %s: %v (%s)", method, path, err, w.Body.String())
		}
	}
	return w.Code, body
}

func TestRegistryDriversListAndTTLCache(t *testing.T) {
	var hits atomic.Int64
	srv := newRegistryTestServer(t, &hits)

	code, body := doJSON(t, srv, http.MethodGet, "/api/registry/drivers")
	if code != 200 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	drivers, ok := body["drivers"].([]any)
	if !ok || len(drivers) != 1 {
		t.Fatalf("drivers = %v", body["drivers"])
	}
	d := drivers[0].(map[string]any)
	if d["name"] != "deye" || d["display_name"] != "Deye" || d["latest_version"] != "3.1.1" {
		t.Errorf("driver = %v", d)
	}

	// Second request must be served from the 5-min TTL cache.
	if code, _ := doJSON(t, srv, http.MethodGet, "/api/registry/drivers"); code != 200 {
		t.Fatalf("cached status = %d", code)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (TTL cache)", got)
	}
}

func TestRegistryDriverVersions(t *testing.T) {
	var hits atomic.Int64
	srv := newRegistryTestServer(t, &hits)

	code, body := doJSON(t, srv, http.MethodGet, "/api/registry/drivers/deye/versions")
	if code != 200 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if body["name"] != "deye" {
		t.Errorf("name = %v", body["name"])
	}
	vs, ok := body["versions"].([]any)
	if !ok || len(vs) != 2 {
		t.Fatalf("versions = %v", body["versions"])
	}
	if v0 := vs[0].(map[string]any); v0["version"] != "3.1.1" {
		t.Errorf("versions[0] = %v", v0)
	}
}

func TestRegistryDriverManifest(t *testing.T) {
	var hits atomic.Int64
	srv := newRegistryTestServer(t, &hits)

	code, body := doJSON(t, srv, http.MethodGet, "/api/registry/drivers/deye/3.1.1/manifest")
	if code != 200 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if body["name"] != "deye" || body["version"] != "3.1.1" || body["role"] != "battery" {
		t.Errorf("manifest header = %v", body)
	}
	reqs, ok := body["requires"].([]any)
	if !ok || len(reqs) != 1 {
		t.Fatalf("requires = %v", body["requires"])
	}
	f := reqs[0].(map[string]any)
	if f["name"] != "battery_capacity_wh" || f["type"] != "integer" || f["min"] != float64(1000) {
		t.Errorf("field = %v", f)
	}

	// Served from the TTL cache on repeat.
	doJSON(t, srv, http.MethodGet, "/api/registry/drivers/deye/3.1.1/manifest")
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (TTL cache)", got)
	}
}

func TestRegistryRefreshFlushesCache(t *testing.T) {
	var hits atomic.Int64
	srv := newRegistryTestServer(t, &hits)

	// Warm the cache, flush it, hit again → upstream sees two list GETs.
	doJSON(t, srv, http.MethodGet, "/api/registry/drivers")
	code, body := doJSON(t, srv, http.MethodPost, "/api/registry/refresh")
	if code != 200 {
		t.Fatalf("refresh status = %d", code)
	}
	if body["cleared"] != float64(1) {
		t.Errorf("cleared = %v, want 1", body["cleared"])
	}
	doJSON(t, srv, http.MethodGet, "/api/registry/drivers")
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits = %d, want 2 (cache was flushed)", got)
	}
}

func TestRegistryUpstreamFailureNotCached(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", 500)
			return
		}
		_, _ = w.Write([]byte(`{"count":0,"drivers":[]}`))
	}))
	defer upstream.Close()
	srv := New(&Deps{
		Tel:            telemetry.NewStore(),
		DriverRegistry: driverregistry.New(upstream.URL, t.TempDir()),
		Version:        "test",
	})

	if code, _ := doJSON(t, srv, http.MethodGet, "/api/registry/drivers"); code != 502 {
		t.Fatalf("failure status = %d, want 502", code)
	}
	// Error was not cached — the retry reaches upstream and succeeds.
	if code, _ := doJSON(t, srv, http.MethodGet, "/api/registry/drivers"); code != 200 {
		t.Fatalf("retry status = %d, want 200", code)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
}

func TestRegistryEndpointsWithoutClient503(t *testing.T) {
	srv := New(&Deps{Tel: telemetry.NewStore(), Version: "test"})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/registry/drivers"},
		{http.MethodGet, "/api/registry/drivers/deye/versions"},
		{http.MethodGet, "/api/registry/drivers/deye/3.1.1/manifest"},
		{http.MethodPost, "/api/registry/refresh"},
	} {
		if code, _ := doJSON(t, srv, tc.method, tc.path); code != 503 {
			t.Errorf("%s %s without client = %d, want 503", tc.method, tc.path, code)
		}
	}
}
