---
"forty-two-watts": major
---

Adopt the blixt driver standard: `DRIVER_MANIFEST` replaces the `DRIVER` metadata block in every Lua driver.

BREAKING CHANGE: driver metadata contract rewritten.

- Every driver now declares a `DRIVER_MANIFEST` table (name/version/role, typed `requires`/`options` config schema with bounds + defaults, `provides` emit contract, catalog metadata). A malformed manifest refuses to load; a MISSING manifest loads with a loud warning (legacy pass for hand-written user drivers — no validation, not shown in the catalog picker). The regex `DRIVER={…}` parser is gone; manifests are parsed in a sandboxed Lua VM, and `/api/drivers/catalog` now serves the manifest shape (`id` = file stem; verification data under `verification`; secrets via per-field `secret = true` instead of `config_secrets`).
- Driver config is validated against the manifest before `driver_init` — all errors reported at once, option defaults applied. New per-driver `telemetry_only: true` runs a driver read-only: control-purpose fields aren't enforced and command dispatch is refused.

MIGRATION (upgrading an existing install):

- **Custom drivers on the Pi keep running.** A user driver without a `DRIVER_MANIFEST` starts with a warning in the logs and driver health; add a manifest (copy the shape from `drivers/skeleton.lua`) to restore config validation and Settings-form rendering. A manifest with schema typos still refuses to load — check the startup log after upgrading.
- **Existing driver configs keep running.** Config values that violate the new manifest schemas do NOT stop the driver: it starts with a persistent warning surfaced in `/api/drivers` (`ConfigWarning`) and the logs. The hard validation gate applies only when a driver entry is added or edited via `POST /api/config` (untouched entries are grandfathered), so an upgraded install can always save unrelated settings.
- **`/api/drivers/catalog` consumers must update.** The old fields (`config_secrets`, `capabilities`, `verification_status`, `http_hosts`) are gone; read `requires`/`options` (with `secret: true`), `provides.live`, and `verification.status` instead.
- **Deye behavior fix.** `config.soc_max` / `config.soc_min` now actually take effect (a scoping bug wrote them to dead globals — reg 166 always got 100/20). Sites that set these values will see the configured SoC targets applied after upgrading.
- Blixt-compatible lifecycle verbs: control-capable drivers receive `driver_command("init", 0)` after `driver_init` and `("deinit", 0)` before `driver_default_mode` on clean stop; `host.set_warmup_s(n)` holds command dispatch (not polls) after the init verb.
- Blixt-compatible host API additions: `host.set_model`, `host.set_rated_w`, `host.set_warmup_s`, `host.now_ms`, `host.decode_u16`, `host.decode_f32_be`, `host.decode_string`, `host.write`/`host.write_registers` aliases, optional `modbus_read` kind (defaults holding), single-arg `host.log(msg)`.
- Canonical emit schema accepted natively: `dc_W`/`ac_W` (legacy `W` fallback), `SoC_nom_fract`, `temperature_C`, per-phase `L*_*`, `pv.mppts[]` (fanned out to `mppt{n}_v/a/w` TS series), and a new `"inverter"` diagnostics event. Signs pass through — the Sourceful axis matches ftw site convention at the driver boundary. Legacy ftw emit keys keep working.
- All 31 bundled drivers converted; `drivers/skeleton.lua` added as the template. Contract documented in `docs/driver-manifest.md`.
