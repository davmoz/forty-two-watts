---
"forty-two-watts": minor
---

Driver manifest deep-audit + canonical emit keys for test-covered drivers.

Every bundled driver's `DRIVER_MANIFEST` got a per-field audit: defaults
now match the in-driver fallbacks, numeric bounds follow the physics
(ports, SoC percentages, IEC 61851 charge currents, rated-power caps),
control-only fields are marked `purpose = "control"` so
`telemetry_only` installs don't have to fill them, every field carries
operator-grade help text, and `provides` declares the full canonical
signal set each driver actually emits. Catalog corrections: Kostal
connection defaults to its factory 1502/71, Victron to the Venus OS
system unit 100, sma_pv/ctek_hybrid verification statuses normalized to
real catalog values, and `static = "sn"` removed where no serial
exists on the wire.

The seven test-covered drivers (ferroamp, ferroamp_modbus, sungrow,
pixii, solaredge, solis_string, tibber) now emit the canonical
@srcful/data-models keys (`battery.dc_W`, `SoC_nom_fract`,
`meter.ac_W`, `L1_V`, `total_import_Wh`, `pv.mppts[]`, …). Signs are
unchanged. Minor (not patch) because the raw telemetry `Data` payloads
these drivers publish now carry the canonical key names — the emit
adapter mirrors them back onto every legacy snake_case name Go/UI/Nova
consumers read (`l1_a`, `charge_wh`, `import_wh`, `mppt1_v`, …), so
in-tree consumers are unaffected, but anything external that parsed the
old exact keys `battery.v`/`battery.a` from `/api` Data blobs should
switch to `dc_v`/`dc_a` (mirrored) or the canonical `V`/`A`.

New `manifest_audit_test.go` contract tests enforce the audit
mechanically: every driver manifest parses, help on every field,
secrets are strings, defaults inside bounds, and a two-way cross-check
between manifests and the `config.<key>` reads in driver bodies.
