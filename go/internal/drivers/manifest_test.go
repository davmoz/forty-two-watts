package drivers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalManifest = `
DRIVER_MANIFEST = {
    name = "test", version = "0.1.0", role = "battery",
}
`

func mustParse(t *testing.T, src string) *Manifest {
	t.Helper()
	m, err := ParseManifest(src)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m
}

func wantParseErr(t *testing.T, src, substr string) {
	t.Helper()
	_, err := ParseManifest(src)
	if err == nil {
		t.Fatalf("ParseManifest: expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("ParseManifest error = %q, want substring %q", err.Error(), substr)
	}
}

func TestParseManifestMinimal(t *testing.T) {
	m := mustParse(t, minimalManifest)
	if m.Name != "test" || m.Version != "0.1.0" || m.Role != "battery" {
		t.Errorf("core fields = %q %q %q", m.Name, m.Version, m.Role)
	}
	if m.PollIntervalMS != 0 {
		t.Errorf("PollIntervalMS = %d, want 0 (host default)", m.PollIntervalMS)
	}
	if len(m.Requires) != 0 || len(m.Options) != 0 {
		t.Errorf("expected empty requires/options, got %v / %v", m.Requires, m.Options)
	}
}

func TestParseManifestFull(t *testing.T) {
	src := `
DRIVER_MANIFEST = {
    name    = "acme",
    version = "1.2.3",
    role    = "hybrid",
    poll_interval_ms = 250,
    display_name = "Acme Hybrid",
    manufacturer = "Acme Corp",
    protocols = { "modbus", "mqtt" },
    connection_defaults = { port = 502, unit_id = 1, username = "extapi" },
    verification = {
        status = "production",
        verified_by = { "frahlg@homelab-rpi:14d" },
        verified_at = "2026-04-18",
        notes = "In continuous use.",
    },
    tested_models = { "A1", "A2" },
    requires = {
        { name = "battery_capacity_wh", purpose = "control",
          type = "integer", min = 1000, max = 1000000,
          help = "Total usable capacity in Wh." },
        { name = "api_token", purpose = "always", type = "string",
          secret = true, help = "Auth token." },
    },
    options = {
        { name = "max_c_rate", purpose = "control", type = "double",
          default = 1.0, min = 0.1, max = 5.0 },
        { name = "shared_ac", purpose = "control", type = "boolean", default = true },
    },
    provides = {
        live   = { "battery.dc_W", "battery.SoC_nom_fract" },
        static = { "make", "sn", "rated_w" },
    },
}
`
	m := mustParse(t, src)
	if m.PollIntervalMS != 250 {
		t.Errorf("PollIntervalMS = %d, want 250", m.PollIntervalMS)
	}
	if m.DisplayName != "Acme Hybrid" || m.Manufacturer != "Acme Corp" {
		t.Errorf("display/manufacturer = %q / %q", m.DisplayName, m.Manufacturer)
	}
	if len(m.Protocols) != 2 || m.Protocols[0] != "modbus" {
		t.Errorf("protocols = %v", m.Protocols)
	}
	if m.ConnectionDefaults["username"] != "extapi" {
		t.Errorf("connection_defaults = %v", m.ConnectionDefaults)
	}
	if got, ok := m.ConnectionDefaults["port"].(float64); !ok || got != 502 {
		t.Errorf("connection_defaults.port = %v", m.ConnectionDefaults["port"])
	}
	if m.Verification == nil || m.Verification.Status != "production" ||
		len(m.Verification.VerifiedBy) != 1 || m.Verification.VerifiedAt != "2026-04-18" {
		t.Errorf("verification = %+v", m.Verification)
	}
	if len(m.TestedModels) != 2 {
		t.Errorf("tested_models = %v", m.TestedModels)
	}

	if len(m.Requires) != 2 {
		t.Fatalf("requires = %+v", m.Requires)
	}
	r := m.Requires[0]
	if r.Name != "battery_capacity_wh" || r.Purpose != "control" || r.Type != "integer" {
		t.Errorf("requires[0] = %+v", r)
	}
	if r.Min == nil || *r.Min != 1000 || r.Max == nil || *r.Max != 1000000 {
		t.Errorf("requires[0] bounds = %v/%v", r.Min, r.Max)
	}
	if !m.Requires[1].Secret {
		t.Error("requires[1].Secret = false, want true")
	}
	if len(m.Options) != 2 {
		t.Fatalf("options = %+v", m.Options)
	}
	if d, ok := m.Options[0].Default.(float64); !ok || d != 1.0 {
		t.Errorf("options[0].Default = %v (%T)", m.Options[0].Default, m.Options[0].Default)
	}
	if d, ok := m.Options[1].Default.(bool); !ok || !d {
		t.Errorf("options[1].Default = %v", m.Options[1].Default)
	}
	if len(m.Provides.Live) != 2 || len(m.Provides.Static) != 3 {
		t.Errorf("provides = %+v", m.Provides)
	}
	if got := m.SecretKeys(); len(got) != 1 || got[0] != "api_token" {
		t.Errorf("SecretKeys = %v", got)
	}
}

func TestParseManifestMissingIsError(t *testing.T) {
	wantParseErr(t, `function driver_poll() end`, "missing DRIVER_MANIFEST")
}

func TestParseManifestMissingCoreFields(t *testing.T) {
	wantParseErr(t, `DRIVER_MANIFEST = { version = "1", role = "meter" }`, "manifest.name")
	wantParseErr(t, `DRIVER_MANIFEST = { name = "x", role = "meter" }`, "manifest.version")
	wantParseErr(t, `DRIVER_MANIFEST = { name = "x", version = "1" }`, "manifest.role")
}

func TestParseManifestRejectsUnknownPurpose(t *testing.T) {
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    requires = { { name = "f", purpose = "sometimes", type = "integer" } },
}`, "unknown purpose")
}

func TestParseManifestRejectsUnknownType(t *testing.T) {
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    requires = { { name = "f", purpose = "always", type = "float" } },
}`, "unknown type")
}

func TestParseManifestRejectsDefaultTypeMismatch(t *testing.T) {
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    options = { { name = "ratio", purpose = "control", type = "integer", default = 1.5 } },
}`, "default does not match type")
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    options = { { name = "s", purpose = "always", type = "string", default = 5 } },
}`, "default does not match type")
}

func TestParseManifestRejectsMinGreaterThanMax(t *testing.T) {
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    requires = { { name = "f", purpose = "always", type = "integer", min = 10, max = 1 } },
}`, "min (10) > max (1)")
}

func TestParseManifestRejectsBoundsOnBooleanAndString(t *testing.T) {
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    requires = { { name = "f", purpose = "always", type = "boolean", min = 0, max = 1 } },
}`, "min/max are only meaningful")
	wantParseErr(t, `
DRIVER_MANIFEST = {
    name = "x", version = "0", role = "battery",
    requires = { { name = "f", purpose = "always", type = "string", min = 1 } },
}`, "min/max are only meaningful")
}

func TestParseManifestHTTPHosts(t *testing.T) {
	m := mustParse(t, `
DRIVER_MANIFEST = {
    name = "cloud", version = "0", role = "heat-pump",
    http_hosts = { "api.myuplink.com" },
}`)
	if len(m.HTTPHosts) != 1 || m.HTTPHosts[0] != "api.myuplink.com" {
		t.Errorf("HTTPHosts = %v, want [api.myuplink.com]", m.HTTPHosts)
	}
	// Absent → nil (omitted from JSON).
	if m := mustParse(t, minimalManifest); m.HTTPHosts != nil {
		t.Errorf("HTTPHosts = %v, want nil when undeclared", m.HTTPHosts)
	}
}

func TestParseManifestPollIntervalZeroNormalized(t *testing.T) {
	m := mustParse(t, `DRIVER_MANIFEST = { name = "x", version = "0", role = "meter", poll_interval_ms = 0 }`)
	if m.PollIntervalMS != 0 {
		t.Errorf("PollIntervalMS = %d, want 0", m.PollIntervalMS)
	}
}

// The sandbox must survive drivers with top-level local state and
// function definitions (all bundled drivers) — and must NOT expose a
// host global.
func TestParseManifestSandboxNoHost(t *testing.T) {
	src := `
local counter = 0
local UPPER = string.upper("x")
local floor = math.floor(1.5)
DRIVER_MANIFEST = { name = "x", version = "0", role = "meter" }
function driver_init(config) host.set_make("X") end
function driver_poll() counter = counter + 1 end
`
	m := mustParse(t, src)
	if m.Name != "x" {
		t.Errorf("name = %q", m.Name)
	}
	// A driver that calls host at TOP level must fail cleanly, not panic.
	_, err := ParseManifest(`host.log("boom")` + minimalManifest)
	if err == nil {
		t.Fatal("expected error for top-level host call")
	}
}

// A top-level exec failure without any manifest is the legacy-driver
// signature (pre-manifest drivers may use stdlib the sandbox lacks,
// e.g. os.time()): ErrNoManifest, carrying the exec error, so the
// registry warn-and-loads instead of refusing.
func TestParseManifestTopLevelExecErrorWithoutManifestIsLegacy(t *testing.T) {
	for _, src := range []string{
		`DRIVER_MANIFEST = { name = `, // syntax error
		`local t = os.time()` + "\n" + // sandbox has no os table
			`function driver_poll() end`,
	} {
		_, err := ParseManifest(src)
		if !errors.Is(err, ErrNoManifest) {
			t.Errorf("ParseManifest(%q) = %v, want ErrNoManifest for legacy tolerance", src, err)
		}
	}
}

// A manifest defined BEFORE the failing top-level line is honoured —
// the sandbox got far enough to read the contract.
func TestParseManifestHonoredDespiteLaterExecError(t *testing.T) {
	src := minimalManifest + `
local t = os.time() -- fails: sandbox has no os
function driver_poll() end
`
	m, err := ParseManifest(src)
	if err != nil {
		t.Fatalf("ParseManifest = %v, want manifest honoured despite later exec error", err)
	}
	if m.Name != "test" {
		t.Errorf("name = %q", m.Name)
	}
}

// A MALFORMED manifest stays fatal even when the top-level also failed
// to execute — a typo'd manifest is more dangerous than no manifest.
func TestParseManifestMalformedWithExecErrorStillRefused(t *testing.T) {
	src := `
DRIVER_MANIFEST = { name = "x", version = "0", role = "meter", poll_interval_ms = "fast" }
local t = os.time()
`
	_, err := ParseManifest(src)
	if err == nil || errors.Is(err, ErrNoManifest) {
		t.Fatalf("ParseManifest = %v, want fatal malformed-manifest error", err)
	}
	if !strings.Contains(err.Error(), "poll_interval_ms") {
		t.Errorf("error = %v, want the schema violation surfaced", err)
	}
}

func TestLoadManifestFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drv.lua")
	if err := os.WriteFile(path, []byte(minimalManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "test" {
		t.Errorf("name = %q", m.Name)
	}
	if _, err := LoadManifest(filepath.Join(dir, "missing.lua")); err == nil {
		t.Error("expected error for missing file")
	}
}

func manifestForValidate(t *testing.T) *Manifest {
	return mustParse(t, `
DRIVER_MANIFEST = {
    name = "v", version = "0", role = "battery",
    requires = {
        { name = "host", purpose = "always", type = "string", help = "device IP" },
        { name = "capacity_wh", purpose = "control", type = "integer", min = 1000, max = 100000 },
    },
    options = {
        { name = "c_rate", purpose = "control", type = "double", default = 1.0, min = 0.1, max = 5.0 },
        { name = "verbose", purpose = "always", type = "boolean", default = false },
    },
}`)
}

func TestValidateConfigAllErrorsReported(t *testing.T) {
	m := manifestForValidate(t)
	errs := m.ValidateConfig(map[string]any{
		"capacity_wh": 500,   // below min
		"c_rate":      "one", // wrong type
	}, false)
	if len(errs) != 3 {
		t.Fatalf("errs = %v, want 3 (missing host, capacity below min, c_rate type)", errs)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{`required field "host" is missing`, "below min", `"c_rate" must be a number`} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors missing %q:\n%s", want, joined)
		}
	}
	// Help text rides along on the missing-required error.
	if !strings.Contains(joined, "device IP") {
		t.Errorf("missing-field error should carry help text:\n%s", joined)
	}
}

func TestValidateConfigPasses(t *testing.T) {
	m := manifestForValidate(t)
	errs := m.ValidateConfig(map[string]any{
		"host":        "192.168.1.10",
		"capacity_wh": 10000,
		"c_rate":      0.5,
		"verbose":     true,
	}, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// Unknown extra keys are allowed — the manifest gates declared
	// fields only.
	errs = m.ValidateConfig(map[string]any{
		"host": "x", "capacity_wh": 2000, "custom_key": "anything",
	}, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors with extra key: %v", errs)
	}
}

func TestValidateConfigTelemetryOnlySkipsControlFields(t *testing.T) {
	m := manifestForValidate(t)
	// capacity_wh (control) missing + c_rate (control) invalid — both
	// ignored in telemetry mode; host (always) still enforced.
	errs := m.ValidateConfig(map[string]any{"c_rate": 99.0}, true)
	if len(errs) != 1 || !strings.Contains(errs[0], `"host"`) {
		t.Fatalf("errs = %v, want only missing host", errs)
	}
}

func TestValidateConfigTypeCoercion(t *testing.T) {
	m := manifestForValidate(t)
	// YAML decodes 10000 as int; JSON as float64 — both must pass integer.
	for _, v := range []any{10000, int64(10000), float64(10000)} {
		errs := m.ValidateConfig(map[string]any{"host": "x", "capacity_wh": v}, false)
		if len(errs) != 0 {
			t.Errorf("capacity_wh=%T: unexpected errors %v", v, errs)
		}
	}
	// 1000.5 is not an integer.
	errs := m.ValidateConfig(map[string]any{"host": "x", "capacity_wh": 1000.5}, false)
	if len(errs) != 1 {
		t.Errorf("fractional integer accepted: %v", errs)
	}
}

// YAML unquoted numeric ids (param_power_id: 10013) decode as Go
// numbers but validate against string fields — Lua coerces number →
// string natively, and hard-refusing would break configs that have
// always worked unquoted.
func TestValidateConfigStringAcceptsNumeric(t *testing.T) {
	m := mustParse(t, `
DRIVER_MANIFEST = {
    name = "s", version = "0", role = "battery",
    requires = {
        { name = "param_power_id", purpose = "always", type = "string" },
    },
}`)
	for _, v := range []any{"10013", 10013, int64(10013), float64(10013)} {
		if errs := m.ValidateConfig(map[string]any{"param_power_id": v}, false); len(errs) != 0 {
			t.Errorf("param_power_id=%v (%T): unexpected errors %v", v, v, errs)
		}
	}
	// Non-stringy, non-numeric values still refuse.
	if errs := m.ValidateConfig(map[string]any{"param_power_id": true}, false); len(errs) != 1 {
		t.Errorf("boolean accepted for string field: %v", errs)
	}
}

func TestValidateConfigInclusiveBounds(t *testing.T) {
	m := manifestForValidate(t)
	for _, v := range []int{1000, 100000} {
		if errs := m.ValidateConfig(map[string]any{"host": "x", "capacity_wh": v}, false); len(errs) != 0 {
			t.Errorf("bound %d should be inclusive: %v", v, errs)
		}
	}
	if errs := m.ValidateConfig(map[string]any{"host": "x", "capacity_wh": 100001}, false); len(errs) != 1 {
		t.Errorf("above-max accepted: %v", errs)
	}
}

func TestApplyDefaults(t *testing.T) {
	m := manifestForValidate(t)
	in := map[string]any{"host": "x", "c_rate": 2.0}
	out := m.ApplyDefaults(in)
	if out["c_rate"] != 2.0 {
		t.Errorf("explicit value clobbered: %v", out["c_rate"])
	}
	if out["verbose"] != false {
		t.Errorf("default not applied: %v", out["verbose"])
	}
	if _, ok := in["verbose"]; ok {
		t.Error("ApplyDefaults mutated its input")
	}
	// nil input works.
	out = m.ApplyDefaults(nil)
	if out["c_rate"] != 1.0 {
		t.Errorf("default from nil input: %v", out["c_rate"])
	}
}

// Every bundled driver must carry a parseable manifest — this is the
// PR1 conversion gate.
func TestAllBundledDriversHaveManifests(t *testing.T) {
	dir := "../../../drivers"
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".lua") {
			continue
		}
		n++
		m, err := LoadManifest(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Errorf("%s: %v", f.Name(), err)
			continue
		}
		if m.Name == "" || m.Version == "" || m.Role == "" {
			t.Errorf("%s: incomplete manifest %+v", f.Name(), m)
		}
	}
	if n < 30 {
		t.Errorf("only %d driver files found — wrong dir?", n)
	}
}
