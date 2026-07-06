-- skeleton.lua — forty-two-watts driver template (blixt driver standard).
--
-- WHAT GOES IN A DRIVER
--
--   A driver is the only place in the stack that knows about a specific
--   device's wire protocol, register map, sign conventions, and quirks.
--   Everything above it (dispatch, MPC, telemetry, UI) talks to the
--   driver through the contract below — nothing else ever reads a
--   Modbus register or MQTT topic directly.
--
--   The contract has two halves:
--
--     1. DRIVER_MANIFEST (the Lua table below)
--        Static, parsed by the host in a sandboxed VM BEFORE any driver
--        function runs. Declares which config fields the driver needs
--        (typed, bounded, validated before driver_init), what it emits,
--        and catalog metadata for the UI. Full contract:
--        docs/driver-manifest.md.
--
--     2. Five lifecycle functions — ALL invoked by forty-two-watts:
--          driver_init(config)         read-only identification
--          driver_poll()               read bus, host.emit(...)
--          driver_command(action, value, cmd)  all bus writes
--          driver_default_mode()       watchdog / shutdown safe-revert
--          driver_cleanup()            resource release before VM close
--
-- TO ADD A NEW DEVICE
--
--   Copy this file (e.g. drivers/huawei_luna.lua), bump name + version,
--   fill in the manifest, replace the placeholder bodies, add a config
--   entry with `lua: drivers/huawei_luna.lua` and a capabilities block.
--   Full walkthrough: docs/writing-a-driver.md.
--
-- HOST API — the only surface a driver may call
--
--   The blixt-core surface (a blixt registry driver runs unmodified):
--
--     Identification (driver_init only):
--       host.set_make(s)   host.set_model(s)   host.set_sn(s)
--       host.set_rated_w(w)                    -- nameplate AC rating
--       host.set_warmup_s(n)                   -- post-init command hold
--
--     Modbus I/O (capability-gated via config `capabilities.modbus`):
--       host.modbus_read(addr, count, kind)    -- kind "holding"|"input"|
--                                              -- "coil"|"discrete";
--                                              -- optional (holding)
--       host.write(addr, value)                -- single register
--       host.write_registers(addr, values)     -- FC16 multi-register
--       (aliases: host.modbus_write / host.modbus_write_multi)
--
--     Decode helpers (raw u16 regs → typed values):
--       host.decode_i16(reg)          host.decode_u16(reg)
--       host.decode_i32_be(hi, lo)    host.decode_u32_be(hi, lo)
--       host.decode_i32_le(lo, hi)    host.decode_u32_le(lo, hi)
--       host.decode_f32_be(hi, lo)              -- IEEE-754, hi word first
--       host.decode_string(regs, start, count)  -- 2 ASCII chars/reg
--
--     Emit (driver_poll only) — see EMIT SHAPE below:
--       host.emit("battery"|"pv"|"meter"|"inverter"|"ev"|"vehicle", { … })
--
--     Misc:
--       host.log(msg)  or  host.log(level, msg)   -- level "debug".."error"
--       host.sleep(ms)      host.millis()      host.now_ms()
--
--   forty-two-watts superset (NOT in blixt — use freely in ftw-only
--   drivers, avoid in drivers meant for the shared registry):
--
--       host.set_poll_interval(ms)             -- runtime cadence hint
--       host.set_watchdog_timeout_s(secs)      -- per-driver staleness
--       host.emit_metric(name, value)          -- scalar → long-format TS DB
--       host.json_encode(t) / host.json_decode(s)
--       host.mqtt_sub/mqtt_pub/mqtt_messages   -- capability: mqtt
--       host.http_get/http_post                -- capability: http (allowlist)
--       host.ws_open/ws_send/ws_messages/ws_is_open/ws_close  -- websocket
--       host.tcp_open/tcp_recv/tcp_is_open/tcp_close          -- raw tcp
--
-- EMIT SHAPE — field names are a CONTRACT
--
--   Keys are read by EXACT case-sensitive name (@srcful/data-models
--   vocabulary). Canonical keys per event:
--     battery:  dc_W (+charge / −discharge), V, A,
--               SoC_nom_fract (0..1 fraction, NOT percent),
--               temperature_C, total_charge_Wh, total_discharge_Wh,
--               available_charge_Wh/_discharge_Wh, available_charge_W/_discharge_W
--     meter:    ac_W (+import / −export), Hz, L1_V/L1_A/L1_W (… L2, L3),
--               total_import_Wh, total_export_Wh
--     pv:       dc_W (− = generating), total_generation_Wh,
--               mppts = { {V=,A=,W=}, … }   -- length = real tracker count
--     inverter: ac_W, VA, Hz, L1_V/…, heatsink_C, rated_W,
--               available_import_W, available_export_W
--               (routed to the TS DB as diagnostics, no DER reading)
--   Legacy `W` is accepted as a fallback for dc_W/ac_W. ftw's legacy
--   snake_case keys (w, soc, l1_v, temp_c, …) also keep working — but
--   new drivers should emit canonical keys only.
--
--   SIGN CONVENTION: Sourceful's axis (battery/pv: −W out of the asset,
--   +W into it; meter: +W import) matches ftw's site convention at the
--   driver boundary. Emit device values on that axis; never flip signs
--   above the driver. See docs/site-convention.md.

PROTOCOL = "modbus"   -- "modbus" | "mqtt" | "http" | "websocket" | "tcp" — informational

-- ────────────────────────────────────────────────────────────────────
-- Manifest. Static table, parsed before driver_init() runs. The host:
--   1. validates every declared config field (type + inclusive bounds)
--      and refuses the driver with ALL errors listed on any failure,
--   2. applies option defaults before driver_init,
--   3. skips purpose="control" fields when the driver runs with
--      `telemetry_only: true` in config.yaml,
--   4. surfaces the metadata in GET /api/drivers/catalog for the UI.
-- ────────────────────────────────────────────────────────────────────
DRIVER_MANIFEST = {
  name    = "skeleton",
  version = "0.1.0",

  -- "battery" | "meter" | "pv" | "ev" | "heat-pump" | "hybrid"
  role    = "battery",

  -- Optional telemetry cadence floor in ms. Absent/0 = host default.
  -- poll_interval_ms = 1000,

  -- ftw catalog extensions (all optional; a blixt manifest without
  -- them still loads):
  display_name = "Skeleton template",
  manufacturer = "Acme",
  protocols    = { "modbus" },
  connection_defaults = { port = 502, unit_id = 1 },
  tested_models = {},
  verification = {
    status = "experimental",  -- experimental | beta | production
    -- verified_by = { "user@site:days" }, verified_at = "YYYY-MM-DD",
    -- notes = "…",
  },

  -- Required config fields, validated before driver_init.
  --   purpose = "always"  — needed even to read the device
  --   purpose = "control" — needed only for setpoint writes; skipped
  --                         when the driver is telemetry_only
  requires = {
    { name = "battery_capacity_wh", purpose = "control",
      type = "integer", min = 1000, max = 1000000,
      help = "Total usable capacity in Wh. Cannot be read off the bus." },
  },

  -- Optional fields: validated when present, default applied when
  -- absent. `secret = true` renders as a password input in the UI and
  -- joins the config mask/restore cycle.
  options = {
    { name = "max_c_rate", purpose = "control",
      type = "double", default = 1.0, min = 0.1, max = 5.0,
      help = "Battery max C-rate. 1.0 = rated power == capacity per hour." },
    -- { name = "api_token", purpose = "always", type = "string",
    --   secret = true, help = "…" },
  },

  -- Emit contract: `live` = canonical keys promised each poll,
  -- `static` = host.set_* fields promised after driver_init.
  provides = {
    live   = { "battery.dc_W", "battery.SoC_nom_fract" },
    static = { "make", "model", "sn", "rated_w" },
  },
}

-- ────────────────────────────────────────────────────────────────────
-- driver_init — READ-ONLY identification.
--
-- Called once with the validated config map (manifest defaults already
-- applied). Read make/model/serial/rating off the bus and announce
-- them via host.set_* — make+sn anchor the persistent device_id. NO
-- BUS WRITES here; control arming lives in driver_command("init").
-- ────────────────────────────────────────────────────────────────────
function driver_init(config)
    host.set_make("Acme")
    host.set_model("Generic")
    host.set_sn("UNKNOWN")     -- read the real serial off the bus
    host.set_rated_w(0)        -- the device's actual AC rating
    -- host.set_warmup_s(5)    -- if the device needs a settle hold at 0 W
    return true                -- false → init failed, host logs and skips
end

-- ────────────────────────────────────────────────────────────────────
-- driver_poll — periodic state read. Called on the poll cadence.
-- Read the bus, decode, emit each live field once. No writes.
-- ────────────────────────────────────────────────────────────────────
function driver_poll()
    -- local regs, err = host.modbus_read(0x0258, 60, "holding")
    -- if not regs then return 1000 end
    -- local bat_w = host.decode_i16(regs[1] or 0)   -- +charge / −discharge
    --
    -- host.emit("battery", {
    --     dc_W          = bat_w,
    --     SoC_nom_fract = soc_pct / 100,   -- fraction 0..1, not percent
    --     temperature_C = temp_c,
    -- })
    -- host.emit_metric("cell_delta_mv", delta_mv)   -- ftw extra: TS DB
    return 5000   -- next-poll hint in ms (0 / nil = keep current cadence)
end

-- ────────────────────────────────────────────────────────────────────
-- driver_command — CONTROL. Never invoked for telemetry_only drivers.
-- All bus writes live here. Unknown actions return false (never crash
-- on a typo'd action).
--
--   "init"     value 0 — arm control mode (Remote Mode enable, device
--              watchdog). Sent once by the registry after driver_init.
--   "battery"  value = signed watts (site/Sourceful convention) — the
--              setpoint write. ftw also passes the full command table
--              as a third argument (cmd.action, cmd.power_w, …).
--   "deinit"   value 0 — safe revert: zero setpoint, disable Remote
--              Mode, clear anything that could re-arm autonomously.
--              Sent by the registry on clean stop, before
--              driver_default_mode.
--   ftw extras a control-capable driver may also receive:
--   "curtail" / "curtail_disable" (PV export limiting, W in cmd),
--   loadpoint verbs for EV chargers.
-- ────────────────────────────────────────────────────────────────────
function driver_command(action, value, cmd)
    if action == "init"   then return arm_control_mode()           end
    if action == "deinit" then return revert_to_self_consumption() end
    if action == "battery" then
        return write_battery_setpoint(value)
    end
    host.log("warn", "unknown action '" .. tostring(action) .. "'")
    return false
end

-- Best-effort safe revert. ftw calls this when the watchdog flips the
-- driver offline and on shutdown (after the deinit verb). Must leave
-- the device in autonomous self-consumption without further host
-- input, and must not assume anything about current control state.
function driver_default_mode()
    revert_to_self_consumption()
end

function driver_cleanup()
    -- Called on shutdown before the VM closes. Most drivers have
    -- nothing to do — capability connections are closed by the host.
end

-- ────────────────────────────────────────────────────────────────────
-- Driver-local helpers. NOT part of the contract — replace with the
-- real register sequences for your device.
-- ────────────────────────────────────────────────────────────────────

function arm_control_mode()
    -- e.g. enable Remote Mode register, arm the device-side watchdog.
    return true
end

function revert_to_self_consumption()
    -- e.g. zero the setpoint, disable Remote Mode, wipe stale schedule
    -- slots that would otherwise re-arm autonomously.
    return true
end

function write_battery_setpoint(power_w)
    -- Translate site-convention watts to the device's native units,
    -- clamp to safe bounds, write the register sequence.
    return true
end
