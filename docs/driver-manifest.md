# Driver Manifest Contract (`DRIVER_MANIFEST`)

Every Lua driver exposes a static global table `DRIVER_MANIFEST` at file
scope. It is the single source of metadata truth: the host parses it in
a **sandboxed Lua VM** (no `host` global, minimal stdlib, execution
deadline) before any driver function runs, validates the operator's
config against it, and serves it as the driver catalog. The old
`DRIVER = {…}` block and its regex parser are gone.

A **malformed manifest is a load error** — the driver refuses to start
with a clear message. A typo'd manifest is more dangerous than no
manifest. A **missing manifest gets a legacy pass**: the driver loads
with a loud warning, skips config validation/defaults, and doesn't
appear in the catalog picker — so hand-written user drivers from before
the manifest contract survive an upgrade. Config that violates an
existing driver's manifest also soft-starts (persistent `ConfigWarning`
in driver health) rather than refusing; the hard gate lives in
`POST /api/config` for new or edited driver entries.

The contract is the blixt driver standard (Sourceful registry drivers
run unmodified) plus additive ftw extensions. Reference template:
[`drivers/skeleton.lua`](../drivers/skeleton.lua).

## Top-level fields

| Field | Required | Type | Semantics |
|---|---|---|---|
| `name` | yes | string | Driver name (registry name for fetched drivers). |
| `version` | yes | string | Semver. |
| `role` | yes | string | `battery` \| `meter` \| `pv` \| `ev` \| `heat-pump` \| `hybrid`. |
| `poll_interval_ms` | no | integer | Telemetry cadence floor. Absent/0 = host default. |
| `requires` | no | field list | Config fields that must be present (see below). |
| `options` | no | field list | Config fields validated only when present; `default` applied when absent. |
| `provides` | no | `{ live={…}, static={…} }` | Emit contract: `live` = canonical emit keys promised per poll; `static` = `host.set_*` fields promised post-init. |

ftw catalog extensions (all optional — a plain blixt manifest loads
fine without them):

| Field | Type | Semantics |
|---|---|---|
| `display_name` | string | Human name for the catalog picker. |
| `manufacturer` | string | Vendor. |
| `protocols` | string list | `mqtt` \| `modbus` \| `http` \| `websocket` \| `tcp`. |
| `connection_defaults` | table | Prefill for the connection form (e.g. `{ port = 502, unit_id = 1 }`). |
| `verification` | table | `{ status, verified_by = {…}, verified_at, notes }`. `status` normalizes to `experimental` \| `beta` \| `production` in the catalog. |
| `tested_models` | string list | Hardware the driver has been run against. |
| `http_hosts` | string list | Fixed outbound HTTP hosts a cloud driver talks to (e.g. `{ "api.myuplink.com" }`). The UI seeds `capabilities.http.allowed_hosts` from it. |

## `requires` / `options` field schema

| Key | Required | Semantics |
|---|---|---|
| `name` | yes | The `config:` key this maps to. |
| `purpose` | yes | `"always"` (validated in every mode) or `"control"` (skipped when the driver has `telemetry_only: true`). |
| `type` | yes | `"integer"` \| `"double"` \| `"boolean"` \| `"string"`. `string` fields also accept numeric config values — YAML leaves unquoted ids (`param_power_id: 10013`) as numbers and Lua coerces number → string natively. |
| `min` / `max` | numeric types only | Inclusive bounds. |
| `default` | options | Applied when the field is absent; type-checked at parse. |
| `help` | encouraged | Hint surfaced in validation errors and UI forms. |
| `secret` | no (ftw extension) | `true` → password input in the UI + config mask/restore. Replaces the old `config_secrets` list. |

Parse-time rejections (driver refuses to load): unknown `purpose` or
`type`, `default` not matching `type`, `min > max`, `min`/`max` on
boolean/string fields.

Config validation runs in `Registry.Add` before `driver_init` and
reports **all** errors at once (one log line each, with help text); any
error refuses the driver. Keys not declared in the manifest pass
through unvalidated. Option defaults are merged into the config map
handed to `driver_init`.

## Telemetry-only mode

`telemetry_only: true` on a driver's config entry runs it read-only:
`purpose = "control"` fields are not enforced, and the registry never
dispatches command verbs to it (`Send` returns an error; the watchdog's
`driver_default_mode` fallback is still delivered).

## Lifecycle

```
load driver → parse DRIVER_MANIFEST (sandboxed VM)
→ validate config vs manifest (fail driver on any error)
→ apply option defaults
→ driver_init(config)                 -- read-only identification
→ poll loop: driver_poll()            -- device stays fully passive
…
first command dispatch (lazy arming, non-telemetry_only drivers only):
→ driver_command("init", 0)           -- one-shot control arm
→ warmup hold (host.set_warmup_s)     -- commands suppressed, polls run
→ driver_command("battery", w) etc. on every later dispatch
…
clean stop → driver_command("deinit", 0)  -- armed drivers only: safe revert
          → driver_default_mode()         -- ftw fallback hook
          → driver_cleanup()
```

Arming is deliberately lazy: a driver on a site that never dispatches a
command (idle mode) never receives the `init` verb — no Remote-Mode
enable, no device-watchdog arm, no bus writes beyond polls — and,
having never been armed, never receives `deinit` on stop either.

Missing or false-returning `init`/`deinit` handlers are debug-logged,
never errors — bundled ftw drivers predate the verbs. ftw keeps its
five-function dispatch: unlike blixt, `driver_default_mode` and
`driver_cleanup` are actually invoked (watchdog fallback + shutdown).

Command verbs a control-capable driver may receive: `init`, `deinit`,
`battery` (signed watts), plus the ftw verbs `curtail`,
`curtail_disable`, and loadpoint verbs for EV chargers.

## Host API

Blixt-core surface (see [`host-api.md`](host-api.md) for the full
table): `set_make/set_model/set_sn/set_rated_w/set_warmup_s`,
`modbus_read(addr, count, kind?)` (kind defaults `"holding"`),
`write`/`write_registers` (aliases of `modbus_write`/`modbus_write_multi`),
`decode_i16/u16/i32_*/u32_*/f32_be/string`, `log(msg)` single-arg form,
`sleep`, `millis`, `now_ms`.

ftw superset (keep out of drivers meant for the shared registry):
`set_poll_interval`, `set_watchdog_timeout_s`, `emit_metric`,
`json_encode/json_decode`, and the MQTT / HTTP / WebSocket / raw-TCP
capabilities.

## Canonical emit keys

`host.emit(event, table)` reads keys by exact case. Canonical
(@srcful/data-models) vocabulary, with the legacy blixt `W` accepted as
a fallback for `dc_W`/`ac_W`:

- **battery**: `dc_W` (+charge / −discharge), `V`, `A`,
  `SoC_nom_fract` (0..1 fraction — maps to ftw's `soc`),
  `temperature_C`, `total_charge_Wh`, `total_discharge_Wh`,
  `available_charge_Wh`/`available_discharge_Wh`,
  `available_charge_W`/`available_discharge_W`.
- **meter**: `ac_W` (+import / −export), `Hz`,
  `L1_V`/`L1_A`/`L1_W` (… L2, L3), `total_import_Wh`, `total_export_Wh`.
- **pv**: `dc_W` (− = generating), `total_generation_Wh`,
  `mppts = { {V=,A=,W=}, … }` — fanned out to `pv_mppt{n}_v/a/w` TS-DB
  series (the names bundled drivers already record) and mirrored onto
  the flat `mppt{n}_v/a/w` Data keys when the driver didn't emit them
  itself.
- **inverter** (new event): `ac_W`, `VA`, `Hz`, `L*_*`, `heatsink_C`,
  `rated_W`, `available_import_W`, `available_export_W` — routed to the
  TS DB via the `emit_metric` pathway (structured diagnostics, no DER
  reading). `rated_W` backfills the host's rated power when
  `host.set_rated_w` wasn't called.

Canonical scalars are additionally mirrored onto ftw's legacy
snake_case names in the reading's Data payload (`L1_A` → `l1_a`,
`temperature_C` → `temp_c`, …) so existing consumers (per-phase fuse
guard, Nova adapter, UI) keep working. ftw's legacy emit keys remain
accepted indefinitely; new drivers should emit canonical keys. Keys
outside the known vocabulary pass through to Data and are debug-logged
once per driver+key.

## Sign convention

Sourceful's axis — battery/PV DC: **−W out of the asset**
(discharge / generation), **+W into it** (charge); meter/inverter AC:
**+W import / −W export** — matches ftw's site convention at the driver
boundary. **The emit adapter maps keys, never signs.** See
[`site-convention.md`](site-convention.md).
