# Writing a Lua driver

A driver is a single Lua file that translates one physical device
(inverter, battery, meter, EV charger, gateway) into the EMS's unified
telemetry and control vocabulary. This guide is the authoritative path
for new drivers.

Start here:

- `drivers/skeleton.lua` — the annotated template. **Copy this file.**
- [`docs/driver-manifest.md`](driver-manifest.md) — the `DRIVER_MANIFEST`
  contract in full (field schema, parse rules, lifecycle verbs, canonical
  emit keys).
- [`docs/host-api.md`](host-api.md) — every `host.*` function.

Reference implementations worth reading whole:

- `drivers/sungrow.lua` — Modbus TCP with SN read, battery control,
  curtailment, watchdog fallback, canonical emit keys.
- `drivers/ferroamp.lua` — MQTT with cached topic state, per-unit
  aggregation, JSON command payloads.
- `drivers/tibber.lua` — WebSocket subscription + HTTP bootstrap.
- `drivers/zuidwijk_p1.lua` — raw-TCP stream parsing (DSMR framing).

The host side lives in `go/internal/drivers/`: `manifest.go` (manifest
parse + config validation), `lua.go` (the `host.*` surface),
`emit_adapter.go` (canonical emit-key normalization), `registry.go`
(spawn / poll / command dispatch).

## 1. Why Lua

Lua via [gopher-lua](https://github.com/yuin/gopher-lua) is the driver
runtime. In order of how much it matters:

- **Contributor-friendly**: no toolchain. Edit a `.lua` file and
  restart.
- **Hot-editable on the device**: `ssh` in, tweak a register offset,
  restart, done. Load-bearing for field work.
- **Registry-portable**: the same contract runs on other Sourceful
  hosts (blixt); drivers can be fetched pinned from the Sourceful
  registry (`docs/driver-registry.md`).
- **Good enough performance**: a 1 Hz poll reading a dozen Modbus
  registers is nowhere near the VM budget. The EMS's hot loop is the
  controller, not the driver.

## 2. The contract

Every driver defines one static `DRIVER_MANIFEST` table plus five
lifecycle functions. The host runs them in this order:

```
load file            → top-level runs in a sandboxed VM, DRIVER_MANIFEST parsed
validate config      → typed fields, inclusive bounds, ALL errors at once;
                       any error refuses the driver before it ever runs
apply option defaults
driver_init(config)  → one-shot READ-ONLY identification (set_make/sn/…)
driver_command("init", 0)   → control-capable drivers only: arm control mode
warmup hold          → if driver_init called host.set_warmup_s(n)
driver_poll()        → forever; returns next-poll-ms
driver_command(…)    → on each EMS control tick
…
clean stop           → driver_command("deinit", 0)  — explicit safe revert
                     → driver_default_mode()        — watchdog/shutdown fallback
                     → driver_cleanup()
```

A **missing or malformed manifest is a load error** — the driver
refuses to start. The old `DRIVER = {…}` regex-scraped block is gone.

### 2.1 The manifest

```lua
DRIVER_MANIFEST = {
  name    = "my-device",      -- registry name; match the file stem
  version = "0.1.0",          -- semver
  role    = "battery",        -- battery|meter|pv|ev|heat-pump|hybrid

  poll_interval_ms = 5000,    -- optional cadence floor; absent/0 = host default

  -- ftw catalog extensions (optional):
  display_name = "My Device Brand X",
  manufacturer = "BrandCo",
  protocols    = { "modbus" },              -- mqtt|modbus|http|websocket|tcp
  connection_defaults = { port = 502, unit_id = 1 },
  tested_models = { "BX-5000" },
  verification  = { status = "experimental" },  -- experimental|beta|production

  -- Config fields the driver READS. Declare every single one — a
  -- contract test greps driver bodies for config.<key> accesses and
  -- fails CI on undeclared keys (and on declared-but-never-read keys).
  requires = {
    { name = "battery_capacity_wh", purpose = "control",
      type = "integer", min = 1000, max = 1000000,
      help = "Total usable capacity in Wh. Cannot be read off the bus." },
  },
  options = {
    { name = "max_c_rate", purpose = "control",
      type = "double", default = 1.0, min = 0.1, max = 5.0,
      help = "Battery max C-rate. 1.0 = rated power == capacity per hour." },
    -- { name = "api_token", purpose = "always", type = "string",
    --   secret = true, help = "…" },   -- secret → password input + masking
  },

  -- Emit contract: live = canonical keys promised per poll,
  -- static = host.set_* fields promised after driver_init.
  provides = {
    live   = { "battery.dc_W", "battery.SoC_nom_fract" },
    static = { "make", "model", "sn", "rated_w" },
  },
}
```

Field rules that trip people up:

- `purpose = "always"` means "needed even to read the device";
  `purpose = "control"` means "needed only to write setpoints" —
  control fields are skipped when the operator sets
  `telemetry_only: true`, so declare capacities, SoC floors/ceilings,
  and nameplate ratings as `control`.
- `default` values must match the in-driver fallback (if your Lua does
  `config.foo or 42`, the manifest option defaults to `42` — the host
  merges defaults before `driver_init`, and the two must agree).
- `min`/`max` are inclusive and only valid on numeric types. Give every
  bound a physical justification (ports 1..65535, SoC 0..100, IEC 61851
  charge current ≥ 6 A, …).
- `help` is mandatory in practice: it renders under the form field and
  inside validation errors. Write it for an operator — what the field
  is, where to find the value, and the unit.
- `secret = true` on tokens/passwords → password input in the UI and
  config masking.

`go/internal/drivers/manifest_audit_test.go` enforces most of this
mechanically against every bundled driver.

### 2.2 The lifecycle functions

```lua
-- READ-ONLY identification. No bus writes here — control arming lives
-- in driver_command("init"). config has manifest defaults applied.
function driver_init(config)
    host.set_make("BrandCo")
    host.set_model("BX-5000")
    host.set_sn(read_serial())     -- make+sn anchor the persistent device_id
    host.set_rated_w(5000)
    -- host.set_warmup_s(5)        -- settle hold at 0 W after the init verb
    return true
end

-- Periodic state read. Return the next-poll-interval in ms.
function driver_poll()
    -- read bus → decode → host.emit(...) each live field once
    return 5000
end

-- CONTROL. All bus writes live here. Never crash on an unknown action.
--   "init"     value 0 — arm control mode (Remote Mode, device watchdog)
--   "battery"  value = signed W (site convention) — the setpoint write
--   "deinit"   value 0 — safe revert on clean stop
--   ftw extras: "curtail" / "curtail_disable" (PV export limiting),
--   loadpoint verbs for EV chargers.
function driver_command(action, value, cmd)
    if action == "battery" then return set_battery_power(value) end
    if action == "init" or action == "deinit" then return true end
    return false
end

-- Watchdog / shutdown fallback: ALWAYS revert to safe autonomous
-- self-consumption, assuming nothing about current control state.
function driver_default_mode()
    set_self_consumption()
end

function driver_cleanup()
    -- resource release before the VM closes; capability connections
    -- are closed by the host.
end
```

Dispatch is serialized per driver — you will never see two callbacks
racing for the same VM.

## 3. Telemetry: canonical emit keys

`host.emit(event, table)` reads keys by **exact case**. New drivers
emit the canonical @srcful/data-models vocabulary:

```lua
host.emit("meter", {
    ac_W = 1500,             -- + import / − export
    Hz   = 50.01,
    L1_V = 230.1, L1_A = 2.2, L1_W = 500,   -- … L2, L3
    total_import_Wh = 12345.6,
    total_export_Wh = 7890.1,
})

host.emit("pv", {
    dc_W = -3200,            -- ALWAYS ≤ 0 (generation)
    total_generation_Wh = 1234567,
    mppts = {                -- one row per real tracker; the host fans
        { V = 380.5, A = -4.2, W = -1600 },  -- these out to pv_mppt{n}_v/a/w
        { V = 391.0, A = -4.1, W = -1600 },  -- TS series + legacy Data keys
    },
})

host.emit("battery", {
    dc_W = 2000,             -- + charging / − discharging
    SoC_nom_fract = 0.65,    -- 0..1 FRACTION, not percent
    V = 48.2, A = 41.5,
    temperature_C = 21.5,
    total_charge_Wh = 98765,
    total_discharge_Wh = 54321,
})

host.emit("ev", {            -- ftw shape (no canonical EV schema yet)
    w = 7200,                -- + when charging, 0 when idle
    connected = true, charging = true,
    session_wh = 14500, max_a = 16, phases = 3,
})

host.emit("vehicle", {       -- read-only BMS readings (tesla_vehicle.lua)
    soc = 0.64,              -- 0..1 fraction
    charge_limit_pct = 80, charging_state = "Charging",
})
```

The adapter in `emit_adapter.go` normalizes canonical keys into the
telemetry store's shape and mirrors them onto the legacy snake_case
Data names (`L1_A` → `l1_a`, `temperature_C` → `temp_c`,
`total_import_Wh` → `import_wh`, …) so existing consumers (per-phase
fuse guard, Nova adapter, UI) keep working. ftw's legacy emit keys
(`w`, `soc`, `l1_v`, …) remain accepted indefinitely — but new drivers
should emit canonical keys only. Keys outside the known vocabulary
pass through to Data and are debug-logged once per driver+key.

For scalar diagnostics that don't fit the structured shape —
temperatures, DC-link voltages, status codes, vendor counters — use:

```lua
host.emit_metric("inverter_temp_c", 42.3, "°C")
host.emit_metric("battery_dc_v",    48.7, "V")
host.emit_metric("grid_hz",         50.01)         -- unit optional
```

Naming convention: snake_case with a unit suffix. The optional 3rd
argument is a display unit (`"°C"`, `"Hz"`, `"kW"`, …) carried into the
live snapshot so the UI can group + label the metric (e.g. the heat-pump
detail drill-in groups by unit class). These land in the long-format
TSDB where the UI charts them on demand. There's no allow-list — pick a
stable name and keep using it. Emitting a metric also counts as a driver
health success (a metric-only driver stays online).

## 4. Sign convention

The single most important rule in driver-land. Sourceful's axis
matches ftw's site convention at the driver boundary — emit device
values on it and **never flip signs above the driver**:

| Channel | Positive | Negative |
|---|---|---|
| `meter.ac_W` | importing from grid | exporting to grid |
| `pv.dc_W` | (never — always ≤ 0) | generating |
| `battery.dc_W` | charging (energy INTO battery) | discharging |
| `ev.w` | vehicle charging | (never) |

Drivers convert at the boundary. If your device reports PV as a
positive number (almost all do), negate it before `host.emit`. If it
encodes battery direction in a status register (Sungrow does), decode
direction first, then apply sign, then emit. Full rationale in
[`docs/site-convention.md`](site-convention.md).

## 5. The host API in one breath

Blixt-core surface (a registry driver runs unmodified on both hosts):
`set_make/set_model/set_sn/set_rated_w/set_warmup_s`,
`modbus_read(addr, count, kind?)` (kind defaults `"holding"`),
`write`/`write_registers` (+ ftw names `modbus_write`/`modbus_write_multi`),
`decode_i16/u16/u32_le/u32_be/i32_le/i32_be/f32_be/string`,
`log(msg)` or `log(level, msg)`, `sleep`, `millis`, `now_ms`,
`emit(event, tbl)`.

ftw superset (use freely in ftw-only drivers; avoid in drivers meant
for the shared registry): `set_poll_interval`,
`set_watchdog_timeout_s`, `emit_metric`, `json_encode/json_decode`,
`persist_secret(key, value)` (durably store a provider-rotated secret
— e.g. an OAuth `refresh_token` — in the unwatched state KV; layered
back over `config.<key>` at next `driver_init`; operator-entered
credentials stay in `config.<key>` with `secret = true` in the
manifest, never write those back), MQTT
(`mqtt_subscribe/mqtt_publish/mqtt_messages`), HTTP
(`http_get/http_post`, allowlisted hosts), WebSocket
(`ws_open/ws_send/ws_messages/ws_is_open/ws_close`), raw TCP
(`tcp_open/tcp_recv/tcp_is_open/tcp_close`).

Capabilities are granted per driver in `config.yaml` — calling an
ungranted capability returns an error string. Always wrap Modbus reads
in `pcall`; one failed register must not kill the poll.

Full signatures: [`docs/host-api.md`](host-api.md).

## 6. Step-by-step: adding a new device

1. Copy `drivers/skeleton.lua` to `drivers/my-device.lua`.
2. Fill in `DRIVER_MANIFEST`: name/version/role, catalog metadata, a
   `requires`/`options` entry for **every** config key you read, and
   the `provides` contract.
3. Replace the placeholder bodies with your device's protocol code.
   Convert signs at the boundary; emit canonical keys.
4. Sanity-check manifest + lifecycle against the unit harness:

   ```bash
   cd go
   go test -count=1 -run 'TestAudit|TestLuaDriverLifecycle' ./internal/drivers/
   ```

5. Wire the driver into `config.yaml`:

   ```yaml
   drivers:
     - name: my-device
       lua: drivers/my-device.lua          # or driver: name@version (registry)
       battery_capacity_wh: 10000
       # telemetry_only: true              # run read-only; control fields not required
       config:                             # validated against the manifest
         battery_capacity_wh: 10000
       capabilities:
         modbus: { host: 192.168.1.50, port: 502, unit_id: 1 }
   ```

6. Probe it without restarting — `POST /api/drivers/test` runs a real
   one-shot init+poll against the device and returns what it emitted:

   ```bash
   curl -s -X POST localhost:8080/api/drivers/test -d '{
     "lua": "drivers/my-device.lua",
     "capabilities": { "modbus": { "host": "192.168.1.50", "port": 502, "unit_id": 1 } },
     "config": { "battery_capacity_wh": 10000 }
   }' | jq
   ```

   (The Settings → Devices "Test connection" button uses the same
   endpoint.)

7. Restart the service (or let `fsnotify` hot-reload the config), then
   confirm in Settings → Devices that the driver appears with the
   correct `device_id` and that `curl localhost:8080/api/status` shows
   sane, correctly-signed numbers.

## 7. Common pitfalls

- **Forgetting the sign flip.** `pv_w` must be negative when the sun
  is up — the host rejects positive PV emits outright.
- **Undeclared config keys.** Every `config.<key>` your body reads must
  be in `requires`/`options` — `TestAuditEveryConfigReadIsDeclared`
  fails CI otherwise, because undeclared keys can never be set from
  the UI form.
- **Percent SoC.** `SoC_nom_fract` (and legacy `soc`) is a 0..1
  fraction. If the device reports 0–100, divide.
- **Blocking `driver_poll`.** Poll runs on a single goroutine per
  driver. Read, emit, return the next-poll-ms.
- **Re-emitting stale cache.** For push transports (MQTT/WS), track
  per-topic arrival timestamps and stop emitting when the cache ages
  out — otherwise `host.emit` keeps advancing LastSuccess and the
  watchdog can never flip a dead device offline (see the 2026-05-02
  incident notes in `drivers/ferroamp.lua`).
- **Ignoring `driver_default_mode`.** Without a safe revert your device
  stays in the last forced mode if the EMS crashes. Always revert to
  autonomous self-consumption.
- **Bus writes in `driver_init`.** Identification only. Control arming
  belongs in `driver_command("init", 0)` so `telemetry_only` drivers
  never touch a control register.
- **Emitting too often.** ≈1 Hz is plenty; the control loop runs at
  `control_interval_s` (default 2 s).

## 8. Testing your driver

Unit harness (real LuaDriver against fake capabilities — see
`sungrow_driver_test.go`, `tibber_test.go`, `pixii_driver_test.go` for
patterns worth copying):

```bash
cd go
go test -count=1 ./internal/drivers/
```

Live with the simulators:

```bash
make dev       # sims + main app
```

Against real hardware: `POST /api/drivers/test` (§6), then
`curl localhost:8080/api/status` and the TSDB series browser for your
`emit_metric` diagnostics. See also
[`docs/testing-drivers-live.md`](testing-drivers-live.md).

## 9. Catalog + registry

`GET /api/drivers/catalog` returns every parsed manifest (bundled +
user dir); Settings → Devices renders the "Add device" picker and the
per-driver config form directly from it. A human-readable snapshot
lives in [`docs/driver-catalog.md`](driver-catalog.md).

Drivers can also be fetched from the Sourceful registry pinned as
`driver: name@version` — see [`docs/driver-registry.md`](driver-registry.md).

## 9. Installing a custom driver on a Docker deploy

The Docker image ships the bundled drivers in `/app/drivers/`, which is
part of the **immutable image layer** — replaced wholesale on every
`docker compose pull`. A driver you wrote yourself, or a patched copy of
a bundled one, lives nowhere in the image, so it needs a home that
survives image upgrades. That home is the persistent `./data/drivers/`
directory.

`docker-compose.yml` bind-mounts `./data:/app/data`, and the container
launches with `-user-drivers /app/data/drivers` (see the `CMD` in
`Dockerfile`). At config-resolution time the EMS probes this directory
**first** and only falls back to the bundled `/app/drivers/` when a file
isn't found there (`go/internal/config/config.go`, `ResolveDriverPaths`).
So a file in `./data/drivers/` is loaded, shadows any bundled driver of
the same name, and persists across both image upgrades and power loss.
Available since v0.100.0.

To install one (the deploy directory is `~/forty-two-watts` after
`scripts/install.sh`):

```bash
mkdir -p ~/forty-two-watts/data/drivers
cp my-device.lua ~/forty-two-watts/data/drivers/

# Linux only: the container runs as uid 100 / gid 101, so the file must
# be readable by that user. (Docker Desktop on macOS maps ownership
# transparently — skip this there.)
sudo chown 100:101 ~/forty-two-watts/data/drivers/my-device.lua

cd ~/forty-two-watts && sudo docker compose restart forty-two-watts
```

Reference it in `config.yaml` the normal way — `lua: drivers/my-device.lua`.
The `drivers/` prefix resolves to the user directory first, so no
absolute paths leak into the config and it stays portable.

> **Don't `docker cp` into the running container.** Copying a driver to
> `forty-two-watts:/app/drivers/` writes into the container's ephemeral
> writable layer — not the image, not the volume. It works until the next
> `docker compose pull && up -d` recreates the container and discards that
> layer, and it can be lost on an unclean power-off. Use `./data/drivers/`
> instead; that overlay exists precisely so you never have to.

During a [pair session](ftw-pair.md) the friend's `deploy_driver` tool
writes here automatically, so a driver added remotely is already
persistent without any of the above.
