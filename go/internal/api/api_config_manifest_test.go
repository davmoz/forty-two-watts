package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/config"
	"github.com/frahlg/forty-two-watts/go/internal/control"
	"github.com/frahlg/forty-two-watts/go/internal/drivers"
	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

// testManifestLua declares one always-required field (host), one bounded
// option (port) and one control-required field (max_w) so the three
// validation paths — missing required, out-of-bounds, telemetry_only
// skip — are all exercisable.
const testManifestLua = `
DRIVER_MANIFEST = {
  name = "unit-device",
  version = "1.0.0",
  role = "battery",
  protocols = { "http" },
  requires = {
    { name = "host", purpose = "always", type = "string",
      help = "LAN IP of the device." },
    { name = "max_w", purpose = "control", type = "integer", min = 0,
      help = "Inverter power limit in W." },
  },
  options = {
    { name = "port", purpose = "always", type = "integer", min = 1, max = 65535,
      help = "API port." },
  },
}
function driver_init(config) end
function driver_poll() end
`

func newManifestConfigServer(t *testing.T, saved *config.Config) *Server {
	t.Helper()
	driverDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(driverDir, "unit.lua"), []byte(testManifestLua), 0o644); err != nil {
		t.Fatal(err)
	}
	live := &config.Config{}
	return New(&Deps{
		Tel:        telemetry.NewStore(),
		Ctrl:       control.NewState(0, 50, "unit"),
		CtrlMu:     &sync.Mutex{},
		Cfg:        live,
		CfgMu:      &sync.RWMutex{},
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		DriverDir:  driverDir,
		SaveConfig: func(path string, c *config.Config) error {
			if saved != nil {
				*saved = *c
			}
			return nil
		},
		Version: "test",
	})
}

func baseDriverConfig(drvCfg map[string]any, telemetryOnly bool) config.Config {
	return config.Config{
		Site: config.Site{SmoothingAlpha: 0.3},
		Fuse: config.Fuse{MaxAmps: 16, Phases: 3, Voltage: 230},
		Drivers: []config.Driver{{
			Name:          "unit",
			Lua:           "drivers/unit.lua",
			IsSiteMeter:   true,
			TelemetryOnly: telemetryOnly,
			Capabilities: config.Capabilities{
				HTTP: &config.HTTPCapability{AllowedHosts: []string{"192.168.1.9"}},
			},
			Config: drvCfg,
		}},
	}
}

func postConfig(t *testing.T, srv *Server, cfg config.Config) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var out map[string]any
	if len(w.Body.Bytes()) > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v (%s)", err, w.Body.String())
		}
	}
	return w.Code, out
}

func TestPostConfigRejectsMissingRequiredManifestField(t *testing.T) {
	srv := newManifestConfigServer(t, nil)
	code, body := postConfig(t, srv, baseDriverConfig(map[string]any{"max_w": 5000}, false))
	if code != 400 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	errStr, _ := body["error"].(string)
	if !strings.Contains(errStr, `driver "unit"`) || !strings.Contains(errStr, `required field "host"`) {
		t.Errorf("error = %q, want driver-scoped missing-host message", errStr)
	}
	if merrs, ok := body["manifest_errors"].([]any); !ok || len(merrs) == 0 {
		t.Errorf("manifest_errors = %v, want non-empty list", body["manifest_errors"])
	}
}

func TestPostConfigRejectsOutOfBoundsManifestField(t *testing.T) {
	srv := newManifestConfigServer(t, nil)
	cfg := baseDriverConfig(map[string]any{"host": "192.168.1.9", "max_w": 5000, "port": 0}, false)
	code, body := postConfig(t, srv, cfg)
	if code != 400 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if errStr, _ := body["error"].(string); !strings.Contains(errStr, `field "port"`) {
		t.Errorf("error = %q, want port bounds message", errStr)
	}
}

func TestPostConfigTelemetryOnlySkipsControlFields(t *testing.T) {
	var saved config.Config
	srv := newManifestConfigServer(t, &saved)
	// max_w (purpose=control, required) omitted — telemetry_only waives it.
	code, body := postConfig(t, srv, baseDriverConfig(map[string]any{"host": "192.168.1.9"}, true))
	if code != 200 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if len(saved.Drivers) != 1 || !saved.Drivers[0].TelemetryOnly {
		t.Errorf("saved config = %+v, want telemetry_only driver persisted", saved.Drivers)
	}
}

func TestPostConfigValidManifestConfigSaves(t *testing.T) {
	var saved config.Config
	srv := newManifestConfigServer(t, &saved)
	cfg := baseDriverConfig(map[string]any{"host": "192.168.1.9", "max_w": 5000, "port": 80}, false)
	code, body := postConfig(t, srv, cfg)
	if code != 200 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if len(saved.Drivers) != 1 {
		t.Fatalf("saved drivers = %d, want 1", len(saved.Drivers))
	}
}

func TestPostConfigValidatesRegistryRefFromTTLCache(t *testing.T) {
	srv := newManifestConfigServer(t, nil)
	man, err := drivers.ParseManifest(testManifestLua)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the UI having fetched the registry manifest (the manifest
	// endpoint caches under this exact key).
	srv.registryCacheMu.Lock()
	srv.registryCache = map[string]registryCacheEntry{
		registryManifestCacheKey("unit-device@1.0.0"): {
			expires: time.Now().Add(time.Minute),
			payload: man,
		},
	}
	srv.registryCacheMu.Unlock()

	cfg := baseDriverConfig(nil, false)
	cfg.Drivers[0].Lua = ""
	cfg.Drivers[0].Driver = "unit-device@1.0.0"
	cfg.Drivers[0].Config = map[string]any{"max_w": 5000} // host missing
	code, body := postConfig(t, srv, cfg)
	if code != 400 {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if errStr, _ := body["error"].(string); !strings.Contains(errStr, `required field "host"`) {
		t.Errorf("error = %q, want missing-host message from cached registry manifest", errStr)
	}
}
