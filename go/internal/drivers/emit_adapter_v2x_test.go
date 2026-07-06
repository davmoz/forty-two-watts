package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

// The v2x_charger event (bidirectional EV charger, upstream master) is
// not part of the canonical blixt vocabulary — it must pass through the
// emit adapter untouched and land as a DerV2X reading with its sign
// preserved (positive charging, negative discharging into the site).
func TestEmitAdapterV2XPassthrough(t *testing.T) {
	d, tel, _ := loadTestDriver(t, `
function driver_poll()
    host.emit("v2x_charger", {
        w = -7400,          -- vehicle discharging into the site
        connected = true,
        charging = false,
        soc = 0.72,
    })
    return 1000
end`)
	if _, err := d.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	r := tel.Get("adapter", telemetry.DerV2X)
	if r == nil {
		t.Fatal("no v2x_charger reading stored")
	}
	if r.RawW != -7400 {
		t.Errorf("w = %v, want -7400 (discharge sign must survive the adapter)", r.RawW)
	}
	if r.SoC == nil || *r.SoC != 0.72 {
		t.Errorf("soc = %v, want 0.72", r.SoC)
	}
	if time.Since(r.UpdatedAt) > time.Minute {
		t.Errorf("stale timestamp %v", r.UpdatedAt)
	}
}

// Unit derivation for the inverter→emit_metric pathway: canonical key
// suffixes map onto display units the UI groups by.
func TestInverterMetricUnit(t *testing.T) {
	cases := map[string]string{
		"ac_W": "W", "W": "W", "rated_W": "W",
		"available_import_W": "W", "L1_W": "W",
		"VA": "VA", "Hz": "Hz",
		"heatsink_C": "°C",
		"L1_V":       "V", "L2_V": "V",
		"L3_A": "A",
	}
	for key, want := range cases {
		if got := inverterMetricUnit(key); got != want {
			t.Errorf("inverterMetricUnit(%q) = %q, want %q", key, got, want)
		}
	}
}
