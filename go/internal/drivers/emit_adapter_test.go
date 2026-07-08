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

// pv mppts[] fans out into the pv_mppt{n}_v/a/w TS DB series (the names
// every bundled driver already records) and mirrors each row onto the
// legacy flat Data keys (mppt1_v, …) the nova payload reads.
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
	var data map[string]any
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	if got, _ := data["lifetime_wh"].(float64); got != 123456 {
		t.Errorf("lifetime_wh mirror = %v", data["lifetime_wh"])
	}
	for k, want := range map[string]float64{
		"mppt1_v": 380, "mppt1_a": -5.5, "mppt1_w": -2090,
		"mppt2_v": 390, "mppt2_a": -5.4, "mppt2_w": -2110,
	} {
		if got, _ := data[k].(float64); got != want {
			t.Errorf("legacy Data mirror %s = %v, want %v", k, data[k], want)
		}
	}
	samples := tel.FlushSamples()
	for metric, want := range map[string]float64{
		"pv_mppt1_v": 380, "pv_mppt1_a": -5.5, "pv_mppt1_w": -2090,
		"pv_mppt2_v": 390, "pv_mppt2_a": -5.4, "pv_mppt2_w": -2110,
	} {
		if !sawMetricValue(samples, "adapter", metric, want) {
			t.Errorf("missing TS sample %s=%v", metric, want)
		}
	}
}

// A driver already emitting a flat legacy mppt key keeps its own value —
// the mppts[] mirror must not overwrite it.
func TestEmitAdapterMPPTLegacyKeysWin(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("pv", {
        dc_W = -100,
        mppt1_v = 111,
        mppts = { {V=380} },
    })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rd := tel.Get("adapter", telemetry.DerPV)
	if rd == nil {
		t.Fatal("no pv reading")
	}
	var data map[string]any
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	if got, _ := data["mppt1_v"].(float64); got != 111 {
		t.Errorf("driver's own mppt1_v = %v, want 111 (mirror must not clobber)", data["mppt1_v"])
	}
}

// Canonical meter energy counters mirror onto the legacy import_wh /
// export_wh Data names.
func TestEmitAdapterMeterEnergyMirror(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("meter", { ac_W = 100, total_import_Wh = 5000, total_export_Wh = 700 })
end
`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rd := tel.Get("adapter", telemetry.DerMeter)
	if rd == nil {
		t.Fatal("no meter reading")
	}
	var data map[string]any
	if err := json.Unmarshal(rd.Data, &data); err != nil {
		t.Fatal(err)
	}
	if got, _ := data["import_wh"].(float64); got != 5000 {
		t.Errorf("import_wh mirror = %v, want 5000", data["import_wh"])
	}
	if got, _ := data["export_wh"].(float64); got != 700 {
		t.Errorf("export_wh mirror = %v, want 700", data["export_wh"])
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

// lifecycleVerbsDriver records every command verb with its arrival
// order (verb_<action> = position in the seen list) so tests can assert
// both presence and ordering of init/deinit relative to real commands.
const lifecycleVerbsDriver = `
DRIVER_MANIFEST = { name = "verbs", version = "0.0.0", role = "battery" }
seen = {}
function driver_init(config) end
function driver_poll() return 60000 end
function driver_command(action, w, cmd)
    seen[#seen+1] = action
    host.emit_metric("verb_" .. tostring(action), #seen)
    return true
end
function driver_default_mode()
    host.emit_metric("default_mode_verbs_seen", #seen)
end
`

// Registry lifecycle: control arming is LAZY. Add never sends the
// "init" verb; the first real command dispatch arms the driver (init
// verb, then the command), and clean stop sends "deinit" before
// driver_default_mode only because the driver was armed.
func TestRegistryLifecycleVerbs(t *testing.T) {
	path := writeTestDriver(t, lifecycleVerbsDriver)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	cfg := config.Driver{Name: "verbs", Lua: path, BatteryCapacityWh: 5000}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if sawAnyMetric(tel.FlushSamples(), "verbs", "verb_init") {
		t.Error("init verb dispatched at Add — arming must wait for the first command")
	}
	payload, _ := json.Marshal(map[string]any{"action": "battery", "power_w": 100})
	if err := r.Send(context.Background(), "verbs", payload); err != nil {
		t.Fatalf("Send = %v", err)
	}
	samples := tel.FlushSamples()
	if !sawMetricValue(samples, "verbs", "verb_init", 1) {
		t.Error("init verb not dispatched before the first command")
	}
	if !sawMetricValue(samples, "verbs", "verb_battery", 2) {
		t.Error("battery command not dispatched after the arming init verb")
	}
	// Second Send must NOT re-arm.
	if err := r.Send(context.Background(), "verbs", payload); err != nil {
		t.Fatalf("second Send = %v", err)
	}
	if sawAnyMetric(tel.FlushSamples(), "verbs", "verb_init") {
		t.Error("init verb dispatched twice")
	}
	r.Remove("verbs")
	samples = tel.FlushSamples()
	if !sawMetricValue(samples, "verbs", "verb_deinit", 4) {
		t.Error("deinit verb not dispatched on clean stop of an armed driver")
	}
	// default_mode ran AFTER deinit — it saw all four verbs recorded.
	if !sawMetricValue(samples, "verbs", "default_mode_verbs_seen", 4) {
		t.Error("driver_default_mode did not run after the deinit verb")
	}
}

// C1 safety property: a control-capable driver on a site that never
// dispatches a command (idle mode) must NEVER receive the init verb —
// no Remote-Mode enable, no device-watchdog arm — and consequently no
// deinit on stop. Polls and the watchdog SendDefault path stay verb-free.
func TestRegistryIdleDriverNeverArmed(t *testing.T) {
	path := writeTestDriver(t, lifecycleVerbsDriver)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	cfg := config.Driver{Name: "verbs", Lua: path, BatteryCapacityWh: 5000}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// Watchdog safety path must not arm either.
	if err := r.SendDefault(context.Background(), "verbs"); err != nil {
		t.Fatalf("SendDefault = %v", err)
	}
	r.Remove("verbs")
	samples := tel.FlushSamples()
	if sawAnyMetric(samples, "verbs", "verb_init") {
		t.Error("idle driver received the init verb — control was armed without any command dispatch")
	}
	if sawAnyMetric(samples, "verbs", "verb_deinit") {
		t.Error("unarmed driver received the deinit verb on stop")
	}
	// driver_default_mode still ran (watchdog + clean-stop hook) and saw
	// zero command verbs.
	if !sawMetricValue(samples, "verbs", "default_mode_verbs_seen", 0) {
		t.Error("driver_default_mode did not run verb-free for the idle driver")
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

	// Control mode: missing control-purpose required field soft-starts
	// with a persistent ConfigWarning (upgrade safety — the hard gate
	// for new/edited configs is POST /api/config).
	if err := r.Add(context.Background(), config.Driver{Name: "telem", Lua: path}); err != nil {
		t.Fatalf("Add = %v, want soft-start with warning in control mode", err)
	}
	if h := tel.DriverHealth("telem"); h == nil || !strings.Contains(h.ConfigWarning, "capacity_wh") {
		t.Errorf("ConfigWarning = %+v, want missing capacity_wh surfaced", h)
	}
	r.Remove("telem")
	tel.FlushSamples() // drop any metrics from the control-mode instance

	// Telemetry-only: same config validates CLEAN (control-purpose
	// fields waived) — no warning; no init verb; Send refused;
	// SendDefault (watchdog path) still works.
	cfg := config.Driver{Name: "telem", Lua: path, TelemetryOnly: true}
	if err := r.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer r.Remove("telem")
	if h := tel.DriverHealth("telem"); h != nil && h.ConfigWarning != "" {
		t.Errorf("ConfigWarning = %q, want none in telemetry-only mode", h.ConfigWarning)
	}
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
// seconds after the lazy arming init verb: the first Send arms the
// driver, and both it and every later command inside the hold are
// refused with the warming-up error.
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
function driver_command(action, w, cmd)
    host.emit_metric("verb_" .. tostring(action), 1)
    return true
end
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
	// First Send arms (init verb dispatched) but the triggering command
	// itself is held back by the warmup the driver requested.
	err := r.Send(context.Background(), "warm", payload)
	if err == nil || !strings.Contains(err.Error(), "warming up") {
		t.Errorf("first Send during warmup = %v, want warming-up refusal", err)
	}
	samples := tel.FlushSamples()
	if !sawAnyMetric(samples, "warm", "verb_init") {
		t.Error("first Send did not arm the driver with the init verb")
	}
	if sawAnyMetric(samples, "warm", "verb_battery") {
		t.Error("battery command executed inside the warmup hold")
	}
	// Later Sends inside the hold are refused up-front (Send fast path).
	err = r.Send(context.Background(), "warm", payload)
	if err == nil || !strings.Contains(err.Error(), "warming up") {
		t.Errorf("second Send during warmup = %v, want warming-up refusal", err)
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

// A driver file WITHOUT a DRIVER_MANIFEST loads with a warning (blixt's
// legacy rule) — pre-manifest user drivers must survive an upgrade. No
// validation or defaults apply.
func TestRegistryLoadsDriverWithoutManifest(t *testing.T) {
	src := `
function driver_init(config)
    host.emit_metric("legacy_init_ran", 1)
end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	if err := r.Add(context.Background(), config.Driver{Name: "noman", Lua: path}); err != nil {
		t.Fatalf("Add = %v, want legacy driver to load without a manifest", err)
	}
	defer r.Remove("noman")
	if !sawAnyMetric(tel.FlushSamples(), "noman", "legacy_init_ran") {
		t.Error("driver_init never ran for the manifest-less driver")
	}
}

// A legacy driver whose TOP-LEVEL code fails in the manifest sandbox
// (e.g. os.time() — the sandbox has no os table) but runs fine in the
// full driver VM must still load via the warn-and-load path when it
// defines no manifest at all.
func TestRegistryLoadsLegacyDriverWithSandboxHostileTopLevel(t *testing.T) {
	src := `
local boot_ts = os.time() -- fails in the manifest sandbox, fine in the driver VM
function driver_init(config)
    host.emit_metric("hostile_init_ran", 1)
end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	if err := r.Add(context.Background(), config.Driver{Name: "hostile", Lua: path}); err != nil {
		t.Fatalf("Add = %v, want legacy warn-and-load despite top-level exec error", err)
	}
	defer r.Remove("hostile")
	if !sawAnyMetric(tel.FlushSamples(), "hostile", "hostile_init_ran") {
		t.Error("driver_init never ran for the sandbox-hostile legacy driver")
	}
}

// A MALFORMED manifest (typo'd schema) still refuses outright — only a
// completely absent DRIVER_MANIFEST gets the legacy pass.
func TestRegistryRefusesMalformedManifest(t *testing.T) {
	src := `
DRIVER_MANIFEST = {
    name = "bad", version = "0.0.0", role = "meter",
    requires = {
        { name = "x", purpose = "sometimes", type = "integer" },
    },
}
function driver_init(config) end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	r := NewRegistry(telemetry.NewStore())
	err := r.Add(context.Background(), config.Driver{Name: "bad", Lua: path})
	if err == nil || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("Add = %v, want malformed-manifest (unknown purpose) error", err)
	}
}

// Config that violates the manifest on an existing entry starts the
// driver anyway (soft-landing: an upgrade must not turn into telemetry
// loss) but surfaces the violation in driver health. The hard gate for
// new/edited entries lives in POST /api/config.
func TestRegistryStartsDriverDespiteConfigViolation(t *testing.T) {
	src := `
DRIVER_MANIFEST = {
    name = "soft", version = "0.0.0", role = "meter",
    requires = {
        { name = "port", purpose = "always", type = "integer", min = 1, max = 65535 },
    },
}
function driver_init(config)
    host.emit_metric("soft_init_ran", 1)
end
function driver_poll() return 60000 end
`
	path := writeTestDriver(t, src)
	tel := telemetry.NewStore()
	r := NewRegistry(tel)
	err := r.Add(context.Background(), config.Driver{
		Name: "soft", Lua: path,
		Config: map[string]any{"port": 99999}, // out of bounds
	})
	if err != nil {
		t.Fatalf("Add = %v, want soft-start despite config violation", err)
	}
	defer r.Remove("soft")
	if !sawAnyMetric(tel.FlushSamples(), "soft", "soft_init_ran") {
		t.Error("driver_init never ran on soft-start")
	}
	h := tel.DriverHealth("soft")
	if h == nil || !strings.Contains(h.ConfigWarning, "violates manifest") {
		t.Errorf("health ConfigWarning = %+v, want the manifest violation surfaced", h)
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
