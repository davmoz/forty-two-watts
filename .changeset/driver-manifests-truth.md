---
"forty-two-watts": patch
---

Driver manifest truth pass: `provides.live` now tells the truth.

The 19 bundled drivers that still emit ftw legacy snake_case keys
(deye, goodwe, growatt, huawei, kostal, sma, sma_pv, sofar, solis,
victron, fronius, fronius_smart_meter, zap, sdm630, zuidwijk_p1,
pixii_pv, sonnen, solaredge_pv, solaredge_legacy) declared canonical
`provides.live` contracts (`meter.ac_W`, `battery.SoC_nom_fract`,
`pv.mppts[]`, ...) that their bodies never emit. Each manifest's
`provides.live` is rewritten to exactly the keys the driver's
`host.emit` tables carry (`meter.w`, `meter.l1_v`, `battery.soc`,
`pv.mppt1_v`, ...), verified per emit call site. The seven
canonical-migrated drivers (ferroamp, ferroamp_modbus, pixii, sungrow,
solaredge, solis_string, tibber) are untouched.

Fixes riding along:

- **deye** now emits `freq_hz` on the meter event instead of `hz` —
  `hz` is outside the emit adapter's vocabulary, so grid frequency
  never reached the meter reading's typed field or the Nova adapter's
  `freq_hz`→`Hz` mapping.
- **ferroamp** drops the `default = 0` on `pplim_release_w`, which made
  ApplyDefaults inject 0 and trip a spurious
  "pplim_release_w=0 ignored (must be > 0)" warning on every start.
  Absent/0 both mean "never publish a release", as before.
- **myuplink / easee_cloud / tibber** declare the new optional
  `http_hosts` manifest field with their pinned cloud endpoints, ready
  for the UI to seed `capabilities.http.allowed_hosts` (Go-side parse
  lands separately; the parser ignores unknown manifest keys).
- **myuplink** header comment no longer references the removed
  `config_secrets` mechanism.
