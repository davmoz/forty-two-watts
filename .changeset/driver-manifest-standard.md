---
"forty-two-watts": major
---

Adopt the blixt driver standard: `DRIVER_MANIFEST` replaces the `DRIVER` metadata block in every Lua driver.

BREAKING CHANGE: driver metadata contract rewritten.

- Every driver now declares a `DRIVER_MANIFEST` table (name/version/role, typed `requires`/`options` config schema with bounds + defaults, `provides` emit contract, catalog metadata). Missing or malformed manifest = driver refuses to load. The regex `DRIVER={…}` parser is gone; manifests are parsed in a sandboxed Lua VM, and `/api/drivers/catalog` now serves the manifest shape (`id` = file stem; verification data under `verification`; secrets via per-field `secret = true` instead of `config_secrets`).
- Driver config is validated against the manifest before `driver_init` — all errors reported at once, option defaults applied. New per-driver `telemetry_only: true` runs a driver read-only: control-purpose fields aren't enforced and command dispatch is refused.
- Blixt-compatible lifecycle verbs: control-capable drivers receive `driver_command("init", 0)` after `driver_init` and `("deinit", 0)` before `driver_default_mode` on clean stop; `host.set_warmup_s(n)` holds command dispatch (not polls) after the init verb.
- Blixt-compatible host API additions: `host.set_model`, `host.set_rated_w`, `host.set_warmup_s`, `host.now_ms`, `host.decode_u16`, `host.decode_f32_be`, `host.decode_string`, `host.write`/`host.write_registers` aliases, optional `modbus_read` kind (defaults holding), single-arg `host.log(msg)`.
- Canonical emit schema accepted natively: `dc_W`/`ac_W` (legacy `W` fallback), `SoC_nom_fract`, `temperature_C`, per-phase `L*_*`, `pv.mppts[]` (fanned out to `mppt{n}_v/a/w` TS series), and a new `"inverter"` diagnostics event. Signs pass through — the Sourceful axis matches ftw site convention at the driver boundary. Legacy ftw emit keys keep working.
- All 31 bundled drivers converted; `drivers/skeleton.lua` added as the template. Contract documented in `docs/driver-manifest.md`.
