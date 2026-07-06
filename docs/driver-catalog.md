# Driver Catalog

forty-two-watts drivers are Lua files in [`drivers/`](../drivers/). Each
file declares a `DRIVER_MANIFEST` table (the blixt driver standard —
see [`driver-manifest.md`](driver-manifest.md)) that the host parses in
a sandboxed Lua VM: typed config requirements validated before
`driver_init`, the emit contract, and catalog metadata. The Settings UI
and `GET /api/drivers/catalog` serve the same parsed manifests.

This document is a human-readable snapshot of the bundled catalog. When
a driver is added or removed, regenerate this table from the manifests
rather than hand-inventing metadata here.

Drivers can also be fetched pinned from the Sourceful registry
(`driver: name@version` in `config.yaml`) — see
[`driver-registry.md`](driver-registry.md).

## Protocols

Bundled drivers currently use:

- Modbus TCP
- MQTT
- HTTP
- WebSocket
- raw TCP

Every configured driver must be granted its protocol capability in
`config.yaml`; see [`configuration.md`](configuration.md).

## Verification status

Each manifest carries a `verification` block. `production` requires a
non-empty `verified_by` + `verified_at` (enforced by
`catalog_verification_test.go`); anything other than
`experimental | beta | production` normalizes to `experimental`.

## Bundled Drivers

| Driver | Manufacturer | Role | Protocols | Status | Tested models | File |
|---|---|---|---|---|---|---|
| CTEK Chargestorm (API v1) | CTEK | ev | Modbus | beta | Chargestorm Connected 2/3 | `drivers/ctek.lua` |
| CTEK Chargestorm (API v2) | CTEK | ev | Modbus | beta | Chargestorm Connected 2/3 | `drivers/ctek_v2.lua` |
| CTEK Chargestorm (Modbus + MQTT) | CTEK | ev | Modbus, MQTT | experimental | Chargestorm Connected 2/3 | `drivers/ctek_hybrid.lua` |
| Deye hybrid inverter | Deye | hybrid | Modbus | experimental | SUN-SG03LP1, SUN-SG04LP3 | `drivers/deye.lua` |
| Easee Cloud | Easee | ev | HTTP | production | Home, Charge | `drivers/easee_cloud.lua` |
| Eastron SDM630 meter | Eastron | meter | Modbus | experimental | SDM630 Modbus, SDM72D-M | `drivers/sdm630.lua` |
| Ferroamp EnergyHub | Ferroamp | hybrid | MQTT | production | EnergyHub XL | `drivers/ferroamp.lua` |
| Ferroamp EnergyHub (Modbus) | Ferroamp | hybrid | Modbus | experimental | EnergyHub XL | `drivers/ferroamp_modbus.lua` |
| Fronius GEN24 | Fronius | hybrid | Modbus | experimental | Symo GEN24, Primo GEN24 | `drivers/fronius.lua` |
| Fronius Smart Meter | Fronius | meter | Modbus | experimental | Smart Meter 50kA-3, 63A-3, TS 65A-3 | `drivers/fronius_smart_meter.lua` |
| GoodWe hybrid inverter | GoodWe | hybrid | Modbus | experimental | ET-Plus, EH series | `drivers/goodwe.lua` |
| Growatt hybrid inverter | Growatt | hybrid | Modbus | experimental | SPH, MOD | `drivers/growatt.lua` |
| Huawei SUN2000 Hybrid Inverter | Huawei | hybrid | Modbus | experimental | SUN2000L1, SUN2000-LUNA2000 | `drivers/huawei.lua` |
| Kostal Plenticore | Kostal | hybrid | Modbus | experimental | Plenticore Plus, Piko IQ | `drivers/kostal.lua` |
| Pixii PowerShaper | Pixii | battery | Modbus | experimental | PowerShaper | `drivers/pixii.lua` |
| Pixii PowerShaper (PV + meter) | Pixii | pv | MQTT | experimental | PowerShaper PV telemetry | `drivers/pixii_pv.lua` |
| SMA hybrid inverter | SMA | hybrid | Modbus | experimental | Sunny Tripower, Sunny Boy Storage | `drivers/sma.lua` |
| SMA PV inverter (non-hybrid) | SMA | pv | Modbus | beta | Sunny Tripower CORE1/CORE2 | `drivers/sma_pv.lua` |
| Sofar hybrid inverter | Sofar Solar | hybrid | Modbus | experimental | HYD-ES, HYD-EP | `drivers/sofar.lua` |
| SolarEdge inverter + meter | SolarEdge | pv | Modbus | experimental | HD-Wave, StorEdge | `drivers/solaredge.lua` |
| SolarEdge inverter (PV only) | SolarEdge | pv | Modbus | experimental | HD-Wave, StorEdge | `drivers/solaredge_pv.lua` |
| SolarEdge legacy (K-series with display) | SolarEdge | pv | Modbus | experimental | SE17K display firmware | `drivers/solaredge_legacy.lua` |
| Solis hybrid inverter | Ginlong Solis | hybrid | Modbus | experimental | S6-EH, S5-GR, S6-GR | `drivers/solis.lua` |
| Solis string inverter | Ginlong Solis | pv | Modbus | experimental | S5-GC, S6-GR1P, 3P-G4, 1P-G4 | `drivers/solis_string.lua` |
| sonnenBatterie (local API) | sonnen | battery | HTTP | experimental | sonnen JSON API v2 | `drivers/sonnen.lua` |
| Sourceful Zap | Sourceful | meter | HTTP | beta | Zap local JSON gateway | `drivers/zap.lua` |
| Sungrow SH Hybrid Inverter | Sungrow | hybrid | Modbus | production | SH5.0RT, SH6.0RT, SH8.0RT, SH10RT | `drivers/sungrow.lua` |
| Tesla Vehicle (BLE Proxy) | Tesla | ev | HTTP | beta | Model Y, Model 3 | `drivers/tesla_vehicle.lua` |
| Tibber Pulse | Tibber | meter | WebSocket, HTTP | experimental | Pulse IR, Pulse HAN, Pulse P1 | `drivers/tibber.lua` |
| Victron Energy GX | Victron Energy | hybrid | Modbus | experimental | Cerbo GX, Venus GX | `drivers/victron.lua` |
| Zuidwijk P1 Reader Ethernet | Zuidwijk | meter | raw TCP | experimental | Sagemcom T210-D, Kaifa MA105/MA304, Iskra ME382 | `drivers/zuidwijk_p1.lua` |

(`drivers/skeleton.lua` is the annotated template, not a device driver.)

## Canonical emit keys

The test-covered drivers (ferroamp, ferroamp_modbus, sungrow, pixii,
solaredge, solis_string, tibber) emit the canonical
@srcful/data-models keys (`battery.dc_W`, `meter.ac_W`,
`SoC_nom_fract`, `mppts[]`, …). The remaining drivers still emit ftw's
legacy snake_case keys — accepted indefinitely by the emit adapter,
which normalizes both vocabularies into the same telemetry shape. See
[`driver-manifest.md`](driver-manifest.md) § Canonical emit keys.

## Adding a Driver

Use [`writing-a-driver.md`](writing-a-driver.md) as the canonical
guide. The short version:

1. Copy `drivers/skeleton.lua`.
2. Fill in `DRIVER_MANIFEST` — declare **every** config key you read
   (typed, bounded, with operator-grade help; `secret = true` on
   credentials) and the `provides` contract.
3. Convert all telemetry to the site sign convention before
   `host.emit`; emit canonical keys.
4. Call `host.set_make` and `host.set_sn` as soon as identity is known.
5. Run the driver tests (the manifest-audit contract tests run against
   every bundled driver):

   ```bash
   cd go
   go test -count=1 ./internal/drivers/
   ```

Read-only drivers are useful and welcome — set operators can run any
driver read-only with `telemetry_only: true`. Control support can
follow once the native command path has been verified on real
hardware.
