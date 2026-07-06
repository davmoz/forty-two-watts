package drivers

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/config"
	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

func loadTestDriver(t *testing.T, src string) (*LuaDriver, *telemetry.Store, *HostEnv) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter_test.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tel := telemetry.NewStore()
	env := NewHostEnv("adapter", tel)
	env.BatteryCapacityWh = 10000
	d, err := NewLuaDriver(path, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(d.Cleanup)
	return d, tel, env
}

// Canonical blixt battery keys must land as a valid ftw reading: dc_W →
// w, SoC_nom_fract (0..1 fraction) → soc, canonical scalars mirrored to
// the legacy Data names the Go-side consumers read.
func TestEmitAdapterCanonicalBattery(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("battery", {
        dc_W = -1500,
        SoC_nom_fract = 0.42,
        V = 402.5, A = -3.7, temperature_C = 21.5,
        total_charge_Wh = 1000, total_discharge_Wh = 900,
    })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rd := tel.Get("adapter", telemetry.DerBattery)
	if rd == nil {
		t.Fatal("no battery reading")
	}
	if rd.RawW != -1500 {
		t.Errorf("w = %v, want -1500 (sign passes through)", rd.RawW)
	}
	if rd.SoC == nil || *rd.SoC != 0.42 {
		t.Errorf("soc = %v, want 0.42", rd.SoC)
	}
	var data map[string]any
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{
		"dc_v": 402.5, "dc_a": -3.7, "temp_c": 21.5,
		"charge_wh": 1000, "discharge_wh": 900,
	} {
		if got, _ := data[k].(float64); got != want {
			t.Errorf("legacy mirror %s = %v, want %v", k, data[k], want)
		}
	}
	// Canonical originals stay in Data verbatim.
	if got, _ := data["temperature_C"].(float64); got != 21.5 {
		t.Errorf("canonical temperature_C = %v", data["temperature_C"])
	}
}

// Legacy-blixt `W` fallback: accepted when dc_W/ac_W absent.
func TestEmitAdapterLegacyWFallback(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("battery", { W = 800, SoC_nom_fract = 0.5 })
    host.emit("meter",   { W = 1200 })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if rd := tel.Get("adapter", telemetry.DerBattery); rd == nil || rd.RawW != 800 {
		t.Errorf("battery via W fallback: %+v", rd)
	}
	if rd := tel.Get("adapter", telemetry.DerMeter); rd == nil || rd.RawW != 1200 {
		t.Errorf("meter via W fallback: %+v", rd)
	}
}

// Canonical meter: ac_W → w, per-phase L*_* mirrored onto the l*_*
// names the fuse guard reads from Data.
func TestEmitAdapterCanonicalMeter(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("meter", {
        ac_W = 2300, Hz = 50.02,
        L1_V = 231, L2_V = 232, L3_V = 233,
        L1_A = 5.1, L2_A = -2.0, L3_A = 3.3,
        L1_W = 1177, L2_W = -463, L3_W = 762,
        total_import_Wh = 5000, total_export_Wh = 100,
    })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rd := tel.Get("adapter", telemetry.DerMeter)
	if rd == nil || rd.RawW != 2300 {
		t.Fatalf("meter reading: %+v", rd)
	}
	var data struct {
		FreqHz *float64 `json:"freq_hz"`
		L1A    *float64 `json:"l1_a"`
		L2A    *float64 `json:"l2_a"`
		L3W    *float64 `json:"l3_w"`
	}
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.FreqHz == nil || *data.FreqHz != 50.02 {
		t.Errorf("freq_hz mirror = %v", data.FreqHz)
	}
	if data.L1A == nil || *data.L1A != 5.1 || data.L2A == nil || *data.L2A != -2.0 {
		t.Errorf("per-phase amp mirrors = %v / %v", data.L1A, data.L2A)
	}
	if data.L3W == nil || *data.L3W != 762 {
		t.Errorf("l3_w mirror = %v", data.L3W)
	}
}

// A driver already emitting legacy keys must not be overridden by
// canonical siblings in the same table.
func TestEmitAdapterLegacyKeysWin(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("meter", { w = 111, ac_W = 999 })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if rd := tel.Get("adapter", telemetry.DerMeter); rd == nil || rd.RawW != 111 {
		t.Errorf("legacy w should win: %+v", rd)
	}
}

// pv mppts[] fans out into TS DB series mppt{n}_v/a/w.
func TestEmitAdapterPVMPPTs(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("pv", {
        dc_W = -4200,
        total_generation_Wh = 123456,
        mppts = { {V=380, A=-5.5, W=-2090}, {V=390, A=-5.4, W=-2110} },
    })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rd := tel.Get("adapter", telemetry.DerPV)
	if rd == nil || rd.RawW != -4200 {
		t.Fatalf("pv reading: %+v", rd)
	}
	var data struct {
		LifetimeWh *float64 `json:"lifetime_wh"`
	}
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.LifetimeWh == nil || *data.LifetimeWh != 123456 {
		t.Errorf("lifetime_wh mirror = %v", data.LifetimeWh)
	}
	samples := tel.FlushSamples()
	for metric, want := range map[string]float64{
		"mppt1_v": 380, "mppt1_a": -5.5, "mppt1_w": -2090,
		"mppt2_v": 390, "mppt2_a": -5.4, "mppt2_w": -2110,
	} {
		if !sawMetricValue(samples, "adapter", metric, want) {
			t.Errorf("missing TS sample %s=%v", metric, want)
		}
	}
}

// The new "inverter" event routes to emit_metric diagnostics and
// backfills rated power when the driver didn't set it explicitly.
func TestEmitAdapterInverterEvent(t *testing.T) {
	d, tel, env := loadTestDriver(t, `
function driver_poll()
    host.emit("inverter", {
        ac_W = 3000, VA = 3100, Hz = 49.98,
        L1_V = 230, heatsink_C = 44.5, rated_W = 8000,
        available_import_W = 8000, available_export_W = 7500,
    })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	samples := tel.FlushSamples()
	for metric, want := range map[string]float64{
		"inverter_w": 3000, "inverter_va": 3100, "inverter_hz": 49.98,
		"inverter_l1_v": 230, "inverter_heatsink_c": 44.5,
		"inverter_rated_w":            8000,
		"inverter_available_import_w": 8000,
		"inverter_available_export_w": 7500,
	} {
		if !sawMetricValue(samples, "adapter", metric, want) {
			t.Errorf("missing TS sample %s=%v", metric, want)
		}
	}
	if got := env.IdentityInfo().RatedW; got != 8000 {
		t.Errorf("RatedW backfill = %v, want 8000", got)
	}
	// No DER reading for inverter — diagnostics only in this series.
	for _, der := range []telemetry.DerType{telemetry.DerMeter, telemetry.DerPV, telemetry.DerBattery} {
		if rd := tel.Get("adapter", der); rd != nil {
			t.Errorf("unexpected %s reading from inverter emit: %+v", der, rd)
		}
	}
}

// An explicit host.set_rated_w wins over the inverter emit backfill.
func TestEmitAdapterRatedWExplicitWins(t *testing.T) {
	d, _, env := loadTestDriver(t, `
function driver_init(config) host.set_rated_w(10000) end
function driver_poll()
    host.emit("inverter", { rated_W = 8000 })
end
`)
	if err := d.Init(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.IdentityInfo().RatedW; got != 10000 {
		t.Errorf("RatedW = %v, want explicit 10000", got)
	}
}

// New decode helpers: u16, f32_be (IEEE-754 hi word first), string
// (2 ASCII chars/reg, hi then lo, trailing NUL/space trimmed).
func TestDecodeHelpers(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit_metric("u16", host.decode_u16(0xFFFF))
    -- 0x42C8 0x0000 = 100.0f
    host.emit_metric("f32", host.decode_f32_be(0x42C8, 0x0000))
    -- "AB", "C\0" from index 2 of {junk, 0x4142, 0x4300}
    local regs = { 0x9999, 0x4142, 0x4300 }
    local s = host.decode_string(regs, 2, 2)
    host.emit_metric("strlen", string.len(s))
    if s == "ABC" then host.emit_metric("str_ok", 1) end
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	samples := tel.FlushSamples()
	if !sawMetricValue(samples, "adapter", "u16", 65535) {
		t.Error("decode_u16(0xFFFF) != 65535")
	}
	found := false
	for _, s := range samples {
		if s.Metric == "f32" && math.Abs(s.Value-100.0) < 1e-6 {
			found = true
		}
	}
	if !found {
		t.Error("decode_f32_be(0x42C8,0) != 100.0")
	}
	if !sawMetricValue(samples, "adapter", "str_ok", 1) {
		t.Error("decode_string trim/order wrong")
	}
}

// host.log single-arg form (blixt drivers) + write / write_registers
// aliases + optional modbus_read kind must all resolve.
func TestBlixtHostCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compat.lua")
	src := `
function driver_init(config)
    host.log("single-arg info line")
    host.set_model("Model-X")
    host.set_warmup_s(7)
end
function driver_poll()
    local regs = host.modbus_read(0x0100, 2) -- kind omitted → holding
    host.write(0x0200, 1)
    host.write_registers(0x0300, {1, 2, 3})
    host.emit_metric("now_ms_positive", host.now_ms() > 0 and 1 or 0)
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tel := telemetry.NewStore()
	mb := &compatModbus{}
	env := NewHostEnv("compat", tel).WithModbus(mb)
	d, err := NewLuaDriver(path, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Cleanup()
	if err := d.Init(context.Background(), nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	id := env.IdentityInfo()
	if id.Model != "Model-X" {
		t.Errorf("Model = %q", id.Model)
	}
	if env.Warmup().Seconds() != 7 {
		t.Errorf("Warmup = %v, want 7s", env.Warmup())
	}
	if mb.lastReadKind != ModbusHolding {
		t.Errorf("modbus_read default kind = %d, want holding", mb.lastReadKind)
	}
	if mb.singleWrites != 1 || mb.multiWrites != 1 {
		t.Errorf("write aliases: single=%d multi=%d, want 1/1", mb.singleWrites, mb.multiWrites)
	}
	if !sawMetricValue(tel.FlushSamples(), "compat", "now_ms_positive", 1) {
		t.Error("host.now_ms() not positive")
	}
}

type compatModbus struct {
	lastReadKind int32
	singleWrites int
	multiWrites  int
}

func (m *compatModbus) Read(addr, count uint16, kind int32) ([]uint16, error) {
	m.lastReadKind = kind
	out := make([]uint16, count)
	return out, nil
}
func (m *compatModbus) WriteSingle(addr, value uint16) error     { m.singleWrites++; return nil }
func (m *compatModbus) WriteMulti(addr uint16, v []uint16) error { m.multiWrites++; return nil }
func (m *compatModbus) Close() error                             { return nil }

// Registry lifecycle: control-capable drivers get the "init" verb once
// after driver_init and the "deinit" verb before driver_default_mode on
// clean stop. Telemetry-only drivers get neither, and Send refuses them.
func TestRegistryLifecycleVerbs(t *testing.T) {
	src := `
DRIVER_MANIFEST = { name = "verbs", version = "0.0.0", role = "battery" }
seen = {}
function driver_init(config) end
function driver_poll() return 60000 end
function driver_command(action, w, cmd)
    seen[#seen+1] = action
    host.emit_metric("verb_" .. tostring(action), w + 1)
    return true
end
function driver_default_mode()
    host.emit_metric("default_mode_after_deinit", #seen)
end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	cfg := config.Driver{Name: "verbs", Lua: path, BatteryCapacityWh: 5000}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	samples := tel.FlushSamples()
	if !sawMetricValue(samples, "verbs", "verb_init", 1) {
		t.Error("init verb not dispatched after driver_init")
	}
	r.Remove("verbs")
	samples = tel.FlushSamples()
	if !sawMetricValue(samples, "verbs", "verb_deinit", 1) {
		t.Error("deinit verb not dispatched on clean stop")
	}
	// default_mode ran AFTER deinit — it saw both verbs recorded.
	if !sawMetricValue(samples, "verbs", "default_mode_after_deinit", 2) {
		t.Error("driver_default_mode did not run after the deinit verb")
	}
}

func TestRegistryTelemetryOnly(t *testing.T) {
	src := `
DRIVER_MANIFEST = {
    name = "telem", version = "0.0.0", role = "battery",
    requires = {
        { name = "capacity_wh", purpose = "control", type = "integer", min = 1 },
    },
}
function driver_init(config) end
function driver_poll() return 60000 end
function driver_command(action, w, cmd)
    host.emit_metric("verb_" .. tostring(action), 1)
    return true
end
function driver_default_mode()
    host.emit_metric("default_mode_called", 1)
end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)

	// Control mode: missing control-purpose required field refuses the
	// driver with all errors.
	if err := r.Add(context.Background(), config.Driver{Name: "telem", Lua: path}); err == nil {
		t.Fatal("Add should fail manifest validation in control mode")
		r.Remove("telem")
	}

	// Telemetry-only: same config loads; no init verb; Send refused;
	// SendDefault (watchdog path) still works.
	cfg := config.Driver{Name: "telem", Lua: path, TelemetryOnly: true}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer r.Remove("telem")
	if sawMetricValue(tel.FlushSamples(), "telem", "verb_init", 1) {
		t.Error("telemetry-only driver received init verb")
	}
	payload, _ := json.Marshal(map[string]any{"action": "battery", "power_w": 100})
	err := r.Send(context.Background(), "telem", payload)
	if err == nil || !strings.Contains(err.Error(), "telemetry-only") {
		t.Errorf("Send = %v, want telemetry-only refusal", err)
	}
	if err := r.SendDefault(context.Background(), "telem"); err != nil {
		t.Errorf("SendDefault should still be allowed: %v", err)
	}
	if !sawMetricValue(tel.FlushSamples(), "telem", "default_mode_called", 1) {
		t.Error("SendDefault did not reach driver_default_mode")
	}
}

// host.set_warmup_s suppresses command dispatch (not polls) for n
// seconds after the init verb.
func TestRegistryWarmupSuppressesCommands(t *testing.T) {
	src := `
DRIVER_MANIFEST = { name = "warm", version = "0.0.0", role = "battery" }
function driver_init(config)
    host.set_warmup_s(60)
    host.set_poll_interval(20)
end
polls = 0
function driver_poll()
    polls = polls + 1
    host.emit_metric("polls", polls)
    return 20
end
function driver_command(action, w, cmd) return true end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	cfg := config.Driver{Name: "warm", Lua: path, BatteryCapacityWh: 5000}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer r.Remove("warm")
	payload, _ := json.Marshal(map[string]any{"action": "battery", "power_w": 100})
	err := r.Send(context.Background(), "warm", payload)
	if err == nil || !strings.Contains(err.Error(), "warming up") {
		t.Errorf("Send during warmup = %v, want warming-up refusal", err)
	}
	// Polls keep running during warmup.
	waitFor(t, func() bool {
		return sawAnyMetric(tel.FlushSamples(), "warm", "polls")
	})
}

// Manifest defaults are applied to the config handed to driver_init.
func TestRegistryAppliesManifestDefaults(t *testing.T) {
	src := `
DRIVER_MANIFEST = {
    name = "defs", version = "0.0.0", role = "meter",
    options = {
        { name = "cadence_ms", purpose = "always", type = "integer", default = 1234 },
    },
}
function driver_init(config)
    host.emit_metric("cadence_from_config", config.cadence_ms)
end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	if err := r.Add(context.Background(), config.Driver{Name: "defs", Lua: path}); err != nil {
		t.Fatal(err)
	}
	defer r.Remove("defs")
	if !sawMetricValue(tel.FlushSamples(), "defs", "cadence_from_config", 1234) {
		t.Error("manifest option default not applied before driver_init")
	}
}

// A driver file without a DRIVER_MANIFEST refuses to load via the
// registry (missing manifest = load error).
func TestRegistryRefusesDriverWithoutManifest(t *testing.T) {
	src := `
function driver_init(config) end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	r := NewRegistry(telemetry.NewStore())
	err := r.Add(context.Background(), config.Driver{Name: "noman", Lua: path})
	if err == nil || !strings.Contains(err.Error(), "DRIVER_MANIFEST") {
		t.Fatalf("Add = %v, want missing-manifest error", err)
	}
}

func sawAnyMetric(samples []telemetry.MetricSample, driver, metric string) bool {
	for _, s := range samples {
		if s.Driver == driver && s.Metric == metric {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
