package api

import (
	"testing"

	"github.com/frahlg/forty-two-watts/go/internal/drivers"
)

// catalogEmits keys V2X-driver detection off the manifest's
// provides.live entries ("v2x_charger.w") — the replacement for the old
// catalog `capabilities` array. It must match namespaced entries, bare
// type entries, and nothing else.
func TestCatalogEmits(t *testing.T) {
	entries := []drivers.CatalogEntry{
		{
			Manifest: drivers.Manifest{
				Name: "dc2",
				Provides: drivers.ManifestProvides{
					Live: []string{"v2x_charger.w"},
				},
			},
			Path:     "/opt/drivers/ferroamp_dc2_v2x.lua",
			Filename: "ferroamp_dc2_v2x.lua",
		},
		{
			Manifest: drivers.Manifest{
				Name: "meterdrv",
				Provides: drivers.ManifestProvides{
					Live: []string{"meter.W", "meter.Hz"},
				},
			},
			Path:     "/opt/drivers/sdm630.lua",
			Filename: "sdm630.lua",
		},
		{
			Manifest: drivers.Manifest{
				Name:     "barev2x",
				Provides: drivers.ManifestProvides{Live: []string{"v2x_charger"}},
			},
			Path:     "/opt/drivers/bare.lua",
			Filename: "bare.lua",
		},
	}

	cases := []struct {
		lua, event string
		want       bool
	}{
		{"/opt/drivers/ferroamp_dc2_v2x.lua", "v2x_charger", true},
		// basename match (config paths are often relative)
		{"drivers/ferroamp_dc2_v2x.lua", "v2x_charger", true},
		{"/opt/drivers/sdm630.lua", "v2x_charger", false},
		{"/opt/drivers/sdm630.lua", "meter", true},
		// bare type entry (no ".field" suffix) still matches
		{"/opt/drivers/bare.lua", "v2x_charger", true},
		// unknown driver → no match
		{"/opt/drivers/nope.lua", "v2x_charger", false},
	}
	for _, c := range cases {
		if got := catalogEmits(entries, c.lua, c.event); got != c.want {
			t.Errorf("catalogEmits(%q, %q) = %v, want %v", c.lua, c.event, got, c.want)
		}
	}
}
