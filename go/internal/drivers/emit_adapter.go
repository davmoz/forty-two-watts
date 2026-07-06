// Canonical emit adapter — accepts the blixt/@srcful-data-models emit
// vocabulary (exact-case keys: dc_W, ac_W, SoC_nom_fract, L1_V, mppts[],
// …) alongside ftw's legacy snake_case keys, and normalizes both into
// the telemetry store's expected shape before validation.
//
// Sign convention: NO flips happen here. Sourceful's battery/PV dc axis
// (−W discharge/generation, +W charge) and meter ac axis (+W import)
// match ftw's site convention at the driver boundary — the adapter maps
// KEYS, not signs. See docs/site-convention.md.
package drivers

import (
	"encoding/json"
	"fmt"
)

// canonicalWKey returns which canonical power key applies per event.
func canonicalWKey(typ string) string {
	switch typ {
	case "battery", "pv":
		return "dc_W"
	case "meter", "inverter":
		return "ac_W"
	}
	return ""
}

// knownEmitKeys is the accepted vocabulary per event type: canonical
// blixt keys + ftw legacy keys. Anything outside the set still passes
// through into the reading's Data payload, but is debug-logged once per
// driver+event+key so a typo'd canonical key (soc_nom_fract, L1_v) is
// discoverable instead of silently dead.
var knownEmitKeys = map[string]map[string]struct{}{
	"battery": keySet(
		// canonical
		"dc_W", "W", "V", "A", "SoC_nom_fract", "temperature_C",
		"total_charge_Wh", "total_discharge_Wh",
		"available_charge_Wh", "available_discharge_Wh",
		"available_charge_W", "available_discharge_W",
		// legacy ftw
		"w", "soc", "dc_v", "dc_a", "temp_c", "charge_wh", "discharge_wh",
		"lifetime_wh", "capacity_wh", "v", "a", "rated_w", "state_label",
		"discharge_capable", "charge_capable",
	),
	"meter": keySet(
		// canonical
		"ac_W", "W", "Hz", "total_import_Wh", "total_export_Wh",
		"L1_V", "L2_V", "L3_V", "L1_A", "L2_A", "L3_A", "L1_W", "L2_W", "L3_W",
		// legacy ftw
		"w", "freq_hz", "lifetime_wh", "import_wh", "export_wh",
		"l1_v", "l2_v", "l3_v", "l1_a", "l2_a", "l3_a", "l1_w", "l2_w", "l3_w",
	),
	"pv": keySet(
		// canonical
		"dc_W", "W", "total_generation_Wh", "mppts",
		// legacy ftw
		"w", "dc_v", "dc_w", "lifetime_wh", "generation_wh", "rated_w",
		"temp_c", "pv_source",
		"mppt1_v", "mppt1_a", "mppt1_w", "mppt2_v", "mppt2_a", "mppt2_w",
		"mppt3_v", "mppt3_a", "mppt3_w", "mppt4_v", "mppt4_a", "mppt4_w",
	),
	"inverter": keySet(
		"ac_W", "W", "VA", "Hz", "heatsink_C", "rated_W",
		"available_import_W", "available_export_W",
		"L1_V", "L2_V", "L3_V", "L1_A", "L2_A", "L3_A", "L1_W", "L2_W", "L3_W",
	),
}

func keySet(keys ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s[k] = struct{}{}
	}
	return s
}

// canonicalToLegacy mirrors canonical keys onto the legacy snake_case
// names that Go-side consumers already read from the reading's Data
// payload (fuse guard l1_a, nova adapter mppt/temp/phase fields, UI).
// Only inserted when the legacy key is absent — a driver emitting both
// vocabularies keeps its own values.
var canonicalToLegacy = map[string]map[string]string{
	"battery": {
		"V": "dc_v", "A": "dc_a", "temperature_C": "temp_c",
		"total_charge_Wh": "charge_wh", "total_discharge_Wh": "discharge_wh",
	},
	"meter": {
		"Hz":   "freq_hz",
		"L1_V": "l1_v", "L2_V": "l2_v", "L3_V": "l3_v",
		"L1_A": "l1_a", "L2_A": "l2_a", "L3_A": "l3_a",
		"L1_W": "l1_w", "L2_W": "l2_w", "L3_W": "l3_w",
		"total_import_Wh": "import_wh", "total_export_Wh": "export_wh",
	},
	"pv": {
		"total_generation_Wh": "lifetime_wh",
	},
}

// inverterMetricNames maps the canonical inverter emit keys onto TS DB
// metric names. The "inverter" event has no DER type in this series —
// it is structured diagnostics routed through the emit_metric pathway.
var inverterMetricNames = map[string]string{
	"ac_W": "inverter_w", "W": "inverter_w",
	"VA": "inverter_va", "Hz": "inverter_hz",
	"heatsink_C": "inverter_heatsink_c", "rated_W": "inverter_rated_w",
	"available_import_W": "inverter_available_import_w",
	"available_export_W": "inverter_available_export_w",
	"L1_V":               "inverter_l1_v", "L2_V": "inverter_l2_v", "L3_V": "inverter_l3_v",
	"L1_A": "inverter_l1_a", "L2_A": "inverter_l2_a", "L3_A": "inverter_l3_a",
	"L1_W": "inverter_l1_w", "L2_W": "inverter_l2_w", "L3_W": "inverter_l3_w",
}

// emitEvent is the host.emit entry point: adapt canonical keys, then
// hand the normalized payload to emitTelemetry. m is the driver's emit
// table (already bridged to Go types); ownership transfers here.
func (h *HostEnv) emitEvent(typ string, m map[string]any) error {
	h.logUnknownEmitKeys(typ, m)

	if typ == "inverter" {
		return h.emitInverter(m)
	}

	switch typ {
	case "battery", "pv", "meter":
		h.normalizeCanonical(typ, m)
	}

	m["type"] = typ
	blob, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("emit: encode failed: %w", err)
	}
	return h.emitTelemetry(blob)
}

// normalizeCanonical maps canonical keys into the fields the telemetry
// store validates (`w`, `soc`) and mirrors the rest onto their legacy
// names. Canonical originals stay in the payload verbatim.
func (h *HostEnv) normalizeCanonical(typ string, m map[string]any) {
	if _, ok := m["w"]; !ok {
		if v, ok := numValue(m[canonicalWKey(typ)]); ok {
			m["w"] = v
		} else if v, ok := numValue(m["W"]); ok { // legacy blixt fallback
			m["w"] = v
		}
	}
	if typ == "battery" {
		if _, ok := m["soc"]; !ok {
			// SoC_nom_fract is a 0..1 fraction — exactly what the
			// telemetry store validates. Pass through unscaled.
			if v, ok := numValue(m["SoC_nom_fract"]); ok {
				m["soc"] = v
			}
		}
	}
	for canon, legacy := range canonicalToLegacy[typ] {
		if v, ok := m[canon]; ok {
			if _, exists := m[legacy]; !exists {
				m[legacy] = v
			}
		}
	}
	if typ == "pv" {
		h.adaptMPPTs(m)
	}
}

// adaptMPPTs fans the canonical mppts=[{V,A,W},…] array out into the
// per-tracker TS DB series every bundled driver already records
// (pv_mppt1_v/pv_mppt1_a/pv_mppt1_w, …) and mirrors each row onto the
// legacy flat Data keys (mppt1_v, …) that Go-side consumers (nova
// payload) read — only when the driver didn't emit those keys itself.
func (h *HostEnv) adaptMPPTs(m map[string]any) {
	arr, ok := m["mppts"].([]any)
	if !ok {
		return
	}
	for i, e := range arr {
		row, ok := e.(map[string]any)
		if !ok {
			continue
		}
		for canon, suffix := range map[string]string{"V": "v", "A": "a", "W": "w"} {
			val, ok := numValue(row[canon])
			if !ok {
				continue
			}
			_ = h.emitMetric(fmt.Sprintf("pv_mppt%d_%s", i+1, suffix), val)
			legacy := fmt.Sprintf("mppt%d_%s", i+1, suffix)
			if _, exists := m[legacy]; !exists {
				m[legacy] = val
			}
		}
	}
}

// emitInverter routes the canonical "inverter" event into the
// emit_metric pathway. rated_W additionally backfills the HostEnv's
// rated power when the driver hasn't called host.set_rated_w.
func (h *HostEnv) emitInverter(m map[string]any) error {
	for key, metric := range inverterMetricNames {
		v, ok := numValue(m[key])
		if !ok {
			continue
		}
		if err := h.emitMetric(metric, v); err != nil {
			return err
		}
		if key == "rated_W" && v > 0 {
			h.setRatedWIfUnset(v)
		}
	}
	return nil
}

// logUnknownEmitKeys debug-logs keys outside the known vocabulary, once
// per driver+event+key.
func (h *HostEnv) logUnknownEmitKeys(typ string, m map[string]any) {
	known, ok := knownEmitKeys[typ]
	if !ok {
		return // ev/vehicle keep free-form legacy payloads
	}
	for k := range m {
		if _, ok := known[k]; ok {
			continue
		}
		id := typ + "." + k
		h.mu.Lock()
		if h.loggedEmitKeys == nil {
			h.loggedEmitKeys = make(map[string]struct{})
		}
		_, seen := h.loggedEmitKeys[id]
		if !seen {
			h.loggedEmitKeys[id] = struct{}{}
		}
		h.mu.Unlock()
		if !seen {
			h.Logger.Debug("emit key outside known vocabulary (passed through to Data)",
				"event", typ, "key", k)
		}
	}
}

func numValue(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}
