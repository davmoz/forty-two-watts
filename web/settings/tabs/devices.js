// Settings → Devices tab: driver catalog picker + per-driver editor.
// Owns its own add/remove/connect button wiring; the Settings shell
// stays driver-agnostic.
//
// Driver metadata comes from /api/drivers/catalog, which returns
// DRIVER_MANIFEST-shaped entries (display_name, protocols,
// requires/options field schemas, verification{status,notes}, provides).
// Per-driver "Device settings" forms are generated from that schema by
// web/settings/manifest-form.js — typed inputs, bounds, help text,
// password inputs for secret=true fields. Drivers can also be added
// from the Sourceful registry as pinned `driver: "name@version"` refs.
(function () {
  var S = (window.FTWSettings = window.FTWSettings || { tabs: {} });
  S.tabs = S.tabs || {};
  var MF = window.FTWManifestForm;

  // ownerFetch routes state-changing owner/CONTROL probes (EV-charger probe with
  // email+password, Tesla verify with IP+VIN, driver test with the full driver
  // config) over the STRICT P2P transport so those SENSITIVE bodies never traverse
  // the untrusted relay on the public home route. Wired in p2p.js to the shared
  // fail-closed strict function; falls back to plain fetch only where p2p.js never
  // loaded (genuine LAN / tests).
  function ownerFetch(path, opts) {
    if (typeof window.ownerFetch === "function") return window.ownerFetch(path, opts);
    return fetch(path, opts);
  }

  function catalogEntryForLua(lua) {
    return lua ? (S.catalogByLua || {})[lua] : null;
  }

  // manifestForDriver resolves the manifest for one config driver:
  // `lua:` paths through the catalog cache (sync), `driver:` registry
  // refs through the client-side manifest cache (populated async by
  // fetchRefManifests in after()).
  function manifestForDriver(d) {
    if (d.lua) return catalogEntryForLua(d.lua);
    if (d.driver) return (S.manifestByRef || {})[d.driver] || null;
    return null;
  }

  function driverEmits(d, type) {
    var m = manifestForDriver(d);
    return !!(m && MF && MF.emits(m, type));
  }

  function manifestFieldNames(m) {
    if (!MF || !m) return [];
    return MF.fields(m).map(function (item) { return item.field.name; });
  }

  // sanitizeName mirrors the wizard's driver-name derivation.
  function defaultDriverName(base, config) {
    var nameBase = String(base || "device").toLowerCase()
      .replace(/[^a-z0-9]/g, "-").replace(/-+/g, "-").replace(/^-|-$/g, "") || "device";
    var name = nameBase;
    var n = 2;
    var taken = function (nm) {
      return (config.drivers || []).some(function (d) { return d.name === nm; });
    };
    while (taken(name)) { name = nameBase + "-" + n; n++; }
    return name;
  }

  // driverFromManifest builds a fresh config.drivers[] entry from a
  // manifest: capability blocks from `protocols` prefilled with
  // `connection_defaults`, config seeded with a declared local host.
  // sourceRef (name@version) selects the pinned-registry path; absent
  // means bundled (`lua: path`).
  function driverFromManifest(name, manifest, luaPath, sourceRef) {
    var driver = { name: name };
    if (sourceRef) driver.driver = sourceRef;
    else driver.lua = luaPath;
    driver.capabilities = {};
    var protos = (manifest && manifest.protocols) || [];
    var cd = (manifest && manifest.connection_defaults) || {};
    if (protos.indexOf("mqtt") >= 0) {
      driver.capabilities.mqtt = {
        host: typeof cd.host === "string" ? cd.host : "",
        port: typeof cd.port === "number" ? cd.port : 1883,
        username: typeof cd.username === "string" ? cd.username : "",
        password: typeof cd.password === "string" ? cd.password : "",
      };
    }
    if (protos.indexOf("modbus") >= 0) {
      driver.capabilities.modbus = {
        host: typeof cd.host === "string" ? cd.host : "",
        port: typeof cd.port === "number" ? cd.port : 502,
        unit_id: typeof cd.unit_id === "number" ? cd.unit_id : 1,
      };
    }
    // Cloud drivers declare their fixed hosts via `http_hosts`; seed the
    // allowlist so the driver works out of the box instead of relying on
    // the empty-list allow-any behaviour.
    if (protos.indexOf("http") >= 0) driver.capabilities.http = { allowed_hosts: manifestHTTPHosts(manifest) };
    if (protos.indexOf("websocket") >= 0) driver.capabilities.websocket = { allowed_hosts: [] };
    if (protos.indexOf("tcp") >= 0) driver.capabilities.tcp = { allowed_hosts: [] };
    // A manifest with no protocols still needs ≥1 capability to pass
    // config validation; HTTP with an empty allowlist is the loosest.
    if (Object.keys(driver.capabilities).length === 0) {
      driver.capabilities.http = { allowed_hosts: [] };
    }
    driver.config = {};
    // connection_defaults.host declared (even empty) = the driver takes
    // a user-configurable local endpoint; seed config.host so the
    // manifest form shows it prefilled.
    if (Object.prototype.hasOwnProperty.call(cd, "host") && !driver.capabilities.mqtt && !driver.capabilities.modbus) {
      driver.config.host = typeof cd.host === "string" ? cd.host : "";
    }
    return driver;
  }

  // manifestHTTPHosts: fixed outbound cloud hosts a manifest declares
  // via the optional `http_hosts` extension (e.g. ["api.myuplink.com"]).
  // Tolerates the field being absent or malformed → [] (the operator
  // can still narrow the allowlist by hand).
  function manifestHTTPHosts(manifest) {
    var hosts = manifest && manifest.http_hosts;
    if (!Array.isArray(hosts)) return [];
    return hosts.filter(function (h) { return typeof h === "string" && h !== ""; });
  }

  function verificationBadge(status) {
    return status === "production" ? "🟢 " : status === "beta" ? "🟡 " : "🔴 ";
  }

  S.tabs.devices = {
    render: function (ctx) {
      var help = ctx.help, escHtml = ctx.escHtml, config = ctx.config;
      if (!config.drivers) config.drivers = [];
      var html = '<fieldset><legend>Add device</legend>' +
        '<div class="mf-source-toggle" role="tablist">' +
        '<button type="button" class="mf-source-btn active" data-source="bundled">Bundled</button>' +
        '<button type="button" class="mf-source-btn" data-source="registry">Sourceful registry</button>' +
        '</div>' +
        '<div id="driver-add-bundled">' +
        '<div class="field-row"><div>' +
        '<label>Driver <span class="help" data-help="Pick a Lua driver from the drivers/ directory. Each driver declares its protocols + supported hardware in its DRIVER_MANIFEST.">?</span></label>' +
        '<select id="driver-catalog-picker"><option value="">Loading catalog…</option></select>' +
        '</div><div>' +
        '<label>Friendly name</label><input type="text" id="driver-catalog-name" placeholder="e.g. ferroamp-house">' +
        '</div></div>' +
        '<button class="btn-add" id="driver-catalog-add">+ Add selected</button>' +
        '<p style="color:var(--fg-muted);font-size:0.75rem;margin:8px 0 0">' +
        '🟢 production — verified on real hardware at ≥1 site · ' +
        '🟡 beta — working on a single site, awaiting a second · ' +
        '🔴 experimental — ported from reference, not yet proven against live hardware. ' +
        'Hover a driver for site + date notes.' +
        '</p>' +
        '</div>' +
        '<div id="driver-add-registry" hidden>' +
        '<div class="field-row"><div>' +
        '<label>Driver ' + help('Drivers published to the Sourceful registry. Installs are pinned to an exact version (name@version) — updates are an explicit version change, never implicit.') + '</label>' +
        '<select id="registry-driver-picker"><option value="">Loading registry…</option></select>' +
        '</div><div>' +
        '<label>Version</label><select id="registry-version-picker"><option value="">—</option></select>' +
        '</div></div>' +
        '<div class="field-row"><div>' +
        '<label>Friendly name</label><input type="text" id="registry-driver-name" placeholder="e.g. deye-garage">' +
        '</div><div></div></div>' +
        '<button class="btn-add" id="registry-driver-add">+ Add pinned</button> ' +
        '<button class="btn-add" id="registry-refresh" type="button" title="Flush the registry cache and re-fetch the driver list">Refresh</button>' +
        '<span class="mf-registry-status" id="registry-status"></span>' +
        '</div>' +
        '</fieldset>';
      html += '<div class="mf-banner-error drv-save-banner" hidden></div>';
      html += '<div class="devices-list">';
      config.drivers.forEach(function (d, idx) {
        var cap = d.capabilities || {};
        var mqtt = cap.mqtt || d.mqtt;
        var modbus = cap.modbus || d.modbus;
        var protocol = mqtt ? "mqtt" : (modbus ? "modbus" : (cap.http ? "http" : (cap.websocket ? "ws" : (cap.tcp ? "tcp" : "?"))));
        var sourceLabel = d.driver
          ? "registry · " + escHtml(d.driver)
          : "lua · " + protocol + " · " + escHtml(d.lua || "(none)");
        var supportsBattery = driverEmits(d, "battery");
        html += '<div class="device-item">' +
          '<div class="device-item-header">' +
          '<strong>' + escHtml(d.name) + '</strong>' +
          '<span class="device-meta">' + sourceLabel + '</span>' +
          '<button class="btn-remove" data-remove-idx="' + idx + '">Remove</button>' +
          '</div>' +
          '<div class="field-row device-core-row' + (supportsBattery ? '' : ' field-row-single') + '"><div>';
        if (d.driver) {
          html += '<label>Registry driver ' + help('Pinned Sourceful-registry reference, name@version. Change the version here to upgrade/downgrade explicitly.') + '</label>' +
            '<input type="text" data-path="drivers.' + idx + '.driver" value="' + escHtml(d.driver) + '">';
        } else {
          html += '<label>Driver file ' + help('Path to the .lua driver. Absolute or relative to the config file directory.') + '</label>' +
            '<input type="text" data-path="drivers.' + idx + '.lua" value="' + escHtml(d.lua || "(none)") + '">';
        }
        html += '</div><div class="driver-battery-capacity" data-driver-idx="' + idx + '"' + (supportsBattery ? '' : ' hidden') + '>' +
          '<label>Battery capacity (kWh) ' + help('Nameplate storage capacity in kilowatt-hours. Stored internally as Wh.') + '</label>' +
          '<input type="number" step="0.1" data-path="drivers.' + idx + '.battery_capacity_wh" data-unit-scale="1000" value="' + ((d.battery_capacity_wh || 0) / 1000) + '">' +
          '</div></div>' +
          '<label><input type="checkbox" data-checkbox-path="drivers.' + idx + '.is_site_meter"' + (d.is_site_meter ? ' checked' : '') + '> Site meter ' + help('Exactly one driver should be the site meter — its grid reading defines the point-of-measurement the PI loop balances.') + '</label>' +
          '<label><input type="checkbox" class="drv-telemetry-only" data-driver-idx="' + idx + '" data-checkbox-path="drivers.' + idx + '.telemetry_only"' + (d.telemetry_only ? ' checked' : '') + '> Read-only (telemetry only) ' + help('Run this driver read-only: no control commands are sent and control-purpose settings become optional.') + '</label>';
        if (mqtt) {
          html += '<fieldset><legend>MQTT</legend>' +
            '<div class="field-row"><div>' +
            '<label>Host ' + help('IP or hostname of the MQTT broker exposing the device data (e.g. the Ferroamp EnergyHub).') + '</label>' +
            '<input type="text" data-path="drivers.' + idx + '.capabilities.mqtt.host" value="' + escHtml(mqtt.host) + '">' +
            '</div><div>' +
            '<label>Port</label><input type="number" data-path="drivers.' + idx + '.capabilities.mqtt.port" value="' + (mqtt.port || 1883) + '">' +
            '</div></div>' +
            '<div class="field-row"><div>' +
            '<label>Username</label><input type="text" data-path="drivers.' + idx + '.capabilities.mqtt.username" value="' + escHtml(mqtt.username || "") + '">' +
            '</div><div>' +
            '<label>Password</label><input type="password" data-path="drivers.' + idx + '.capabilities.mqtt.password" value="' + escHtml(mqtt.password || "") + '">' +
            '</div></div></fieldset>';
        }
        if (modbus) {
          html += '<fieldset><legend>Modbus TCP</legend>' +
            '<div class="field-row"><div>' +
            '<label>Host ' + help('IP of the Modbus-TCP device (e.g. Sungrow inverter LAN port).') + '</label>' +
            '<input type="text" data-path="drivers.' + idx + '.capabilities.modbus.host" value="' + escHtml(modbus.host) + '">' +
            '</div><div>' +
            '<label>Port</label><input type="number" data-path="drivers.' + idx + '.capabilities.modbus.port" value="' + (modbus.port || 502) + '">' +
            '</div></div>' +
            '<label>Unit ID ' + help('Slave address. Usually 1 for a single-device setup.') + '</label>' +
            '<input type="number" data-path="drivers.' + idx + '.capabilities.modbus.unit_id" value="' + (modbus.unit_id || 1) + '">' +
            '</fieldset>';
        }
        if (cap.http) {
          // Local-HTTPS devices with self-signed certs (e.g. the NIBE
          // Local REST API) pin the leaf cert by SHA-256 fingerprint —
          // blanket insecure-skip-verify is deliberately not offered.
          html += '<fieldset><legend>HTTPS</legend>' +
            '<label>Certificate fingerprint (SHA-256) ' + help('Pin the device\'s self-signed HTTPS certificate by its SHA-256 fingerprint (the "fingeravtryck" in the myUplink app, or from "openssl x509 -fingerprint -sha256"). 64 hex chars; colons and case are ignored. Leave empty for normal certificate verification.') + '</label>' +
            '<input type="text" autocomplete="off" data-path="drivers.' + idx + '.capabilities.http.tls_pin_sha256" value="' + escHtml((cap.http && cap.http.tls_pin_sha256) || '') + '" placeholder="(empty = normal verification)" style="font-family:var(--mono);font-size:0.78rem">' +
            '</fieldset>';
        }
        // Device settings — generated from the driver's DRIVER_MANIFEST
        // requires/options schema. Filled by the after() pass once the
        // catalog (or the registry manifest for pinned refs) resolves.
        // Special affordances (Tesla verify, EV cloud connect, MyUplink
        // OAuth connect) are layered onto the manifest form by
        // fillManifestSlot.
        html += '<div class="drv-mf-slot" data-driver-idx="' + idx + '"></div>';
        html += '<div class="driver-test-panel">' +
          '<button class="btn-add driver-test-btn" type="button" data-driver-idx="' + idx + '">Test connection</button>' +
          '<span class="driver-test-status" data-driver-idx="' + idx + '"></span>' +
          '<div class="driver-test-output" data-driver-idx="' + idx + '" hidden></div>' +
          '</div>';
        html += '</div>';
      });
      html += '</div>' +
        '<a href="/setup?step=3" class="btn-add" style="display:block;text-align:center;text-decoration:none">Add new device&hellip;</a>' +
        '<button class="btn-add" id="add-mqtt">+ Add MQTT device</button>' +
        '<button class="btn-add" id="add-modbus">+ Add Modbus device</button>';
      return html;
    },

    // validate: pre-save gate the shell calls while this tab is open.
    // Required manifest fields empty (outside read-only mode) block the
    // save; the fields are decorated inline by the form controller.
    validate: function () {
      var forms = S._deviceForms || {};
      var count = 0;
      Object.keys(forms).forEach(function (idx) {
        count += forms[idx].validate().length;
      });
      if (count > 0) {
        return "Fix " + count + " invalid device setting" + (count === 1 ? "" : "s") + " below";
      }
      return null;
    },

    // onSaveError: map backend manifest-validation messages
    // (`driver "x": field "y" …`) onto the offending fields; anything
    // unmatched lands in the banner above the device list.
    //
    // `body` is the parsed 400 payload (settings.js attaches it to the
    // thrown Error). Prefer its `manifest_errors` array — one intact
    // message per failed field — over splitting the joined `error`
    // string on "; ", which mangles help texts that contain "; ".
    onSaveError: function (message, ctx, body) {
      var config = ctx.config;
      var forms = S._deviceForms || {};
      var banner = ctx.bodyEl.querySelector(".drv-save-banner");
      var unmatched = [];
      var msgs = body && Array.isArray(body.manifest_errors)
        ? body.manifest_errors
        : String(message || "").replace(/^validation:\s*/i, "").split("; ");
      msgs.forEach(function (frag) {
        if (!/field "/.test(frag)) return; // non-manifest failure — status line covers it
        var m = /driver "([^"]+)"/.exec(frag);
        var idx = -1;
        if (m) {
          (config.drivers || []).forEach(function (d, i) { if (d.name === m[1]) idx = i; });
        }
        var form = idx >= 0 ? forms[idx] : null;
        var rest = form ? form.showServerErrors([frag.replace(/^driver "[^"]+":\s*/, "")]) : [frag];
        unmatched = unmatched.concat(rest.map(function () { return frag; }));
      });
      if (banner && unmatched.length) {
        banner.textContent = unmatched.join(" · ");
        banner.hidden = false;
      }
    },

    after: function (ctx) {
      var config = ctx.config;
      var bodyEl = ctx.bodyEl;
      var escHtml = ctx.escHtml;
      // help must be aliased here too, not just in render(): the async
      // fill pass (fillManifestSlot → myuplinkSetupHTML) builds HTML with
      // help() long after render() returned. Without this alias any
      // config with a MyUplink driver crashed the whole Devices tab with
      // a ReferenceError.
      var help = ctx.help;
      S._deviceForms = {};
      S.manifestByRef = S.manifestByRef || {};

      function fmtW(v) {
        if (!Number.isFinite(v)) return "—";
        return Math.abs(v) >= 1000 ? (v / 1000).toFixed(2) + " kW" : v.toFixed(0) + " W";
      }

      function fmtNum(v) {
        if (!Number.isFinite(v)) return "—";
        return Math.abs(v) >= 100 ? v.toFixed(0) : v.toFixed(2);
      }

      function fmtAge(ms) {
        if (!Number.isFinite(ms) || ms < 0) return "—";
        var s = Math.floor(ms / 1000);
        return s < 60 ? s + "s ago" : Math.floor(s / 60) + "m ago";
      }

      function renderProbeOutput(res) {
        var readings = res.readings || res.Readings || [];
        var metrics = res.metrics || res.Metrics || [];
        var health = res.health || res.Health || {};
        var identity = res.identity || res.Identity || {};
        var html = '<div class="driver-test-kv">';
        html += '<span>status</span><strong>' + escHtml(res.ok ? "connected" : "failed") + '</strong>';
        html += '<span>elapsed</span><strong>' + escHtml(String(res.elapsed_ms || res.ElapsedMs || 0)) + ' ms</strong>';
        if (health.TickCount != null) {
          html += '<span>ticks</span><strong>' + escHtml(String(health.TickCount)) + '</strong>';
        }
        if (identity.make || identity.sn || identity.endpoint) {
          html += '<span>identity</span><strong>' + escHtml([identity.make, identity.sn, identity.endpoint].filter(Boolean).join(" · ")) + '</strong>';
        }
        html += '</div>';
        if (res.error) {
          html += '<div class="driver-test-error">' + escHtml(res.error) + '</div>';
        }
        if (readings.length) {
          html += '<div class="driver-test-values">';
          readings.forEach(function (r) {
            var soc = r.soc != null ? " · SoC " + (r.soc * 100).toFixed(1) + "%" : "";
            var age = r.updated_at_ms ? " · " + fmtAge(Date.now() - r.updated_at_ms) : "";
            html += '<div><span>' + escHtml(r.type) + '</span><strong>' + escHtml(fmtW(r.smoothed_w)) + '</strong><small>raw ' + escHtml(fmtW(r.raw_w)) + soc + age + '</small></div>';
          });
          html += '</div>';
        }
        if (metrics.length) {
          html += '<div class="driver-test-metrics">';
          metrics.slice(0, 12).forEach(function (m) {
            html += '<span>' + escHtml(m.name) + '</span><strong>' + escHtml(fmtNum(m.value)) + '</strong>';
          });
          if (metrics.length > 12) {
            html += '<span>more</span><strong>' + escHtml(String(metrics.length - 12)) + '</strong>';
          }
          html += '</div>';
        }
        if (!readings.length && !metrics.length && !res.error) {
          html += '<div class="driver-test-empty">No values returned.</div>';
        }
        return html;
      }

      // ---- Device settings section (manifest-driven form) ----

      function secretSavedFn(d) {
        return function (name) {
          var v = d.config ? d.config[name] : null;
          if (typeof v === "string" && v !== "") return true;
          return name === "password" && d.has_password === true;
        };
      }

      // fillManifestSlot renders + wires the "Device settings" section
      // for one driver from its manifest. Falls back to a raw key/value
      // editor when no manifest is resolvable.
      function fillManifestSlot(slot, d, idx, manifest) {
        if (!manifest || !MF) {
          renderRawConfigFallback(slot, d, idx);
          return;
        }
        var names = manifestFieldNames(manifest);
        var isVehicle = MF.emits(manifest, "vehicle");
        var isCloudEV = names.indexOf("email") >= 0 && names.indexOf("password") >= 0;
        // OAuth authorization-code drivers (e.g. MyUplink) declare
        // client_id + client_secret; the refresh_token is managed by the
        // Connect flow (persisted server-side), never a manifest field.
        var isOAuth = names.indexOf("client_id") >= 0 && names.indexOf("client_secret") >= 0;
        var html = "";
        var fieldsHTML = MF.renderFields(manifest, d.config || {}, {
          telemetryOnly: !!d.telemetry_only,
          secretSaved: secretSavedFn(d),
        });
        if (isOAuth) {
          html += myuplinkSetupHTML(d);
        }
        if (fieldsHTML) {
          html += '<div class="mf-eyebrow">Device settings</div>' + fieldsHTML;
        }
        if (isOAuth) {
          html += myuplinkConnectHTML(d, idx);
        }
        if (isVehicle) {
          html += '<div style="margin-top:8px;display:flex;gap:10px;align-items:center">' +
            '<button class="btn-add tesla-verify-btn" type="button" data-driver-idx="' + idx + '">Verify connection</button>' +
            '<span class="tesla-verify-status" data-driver-idx="' + idx + '" style="font-size:0.82rem;color:var(--fg-muted)"></span>' +
            '</div>';
        }
        if (isCloudEV) {
          html += '<div style="margin-top:8px;display:flex;gap:10px;align-items:center">' +
            '<button class="btn-add ev-connect-btn" type="button" data-driver-idx="' + idx + '">Connect</button>' +
            '<span id="ev-connect-status-' + idx + '" style="font-size:0.8rem;color:var(--fg-muted)"></span>' +
            '</div>';
        }
        slot.innerHTML = html;
        if (!fieldsHTML && !isVehicle && !isCloudEV && !isOAuth) return;

        if (!d.config) d.config = {};
        S._deviceForms[idx] = MF.wire(slot, manifest, d.config, {
          telemetryOnly: function () { return d.telemetry_only === true; },
          secretSaved: secretSavedFn(d),
        });

        if (isVehicle) wireVehicleAffordance(slot, d, idx);
        if (isCloudEV) wireCloudConnect(slot, d, idx);
        if (isOAuth) wireMyUplinkConnect(slot, d, idx);
      }

      // OAuth authorization-code setup (MyUplink): numbered steps + the
      // exact Callback URL above the manifest fields (client_id /
      // client_secret render from the manifest with the portal's labels
      // via their help text).
      function myuplinkSetupHTML(d) {
        var callbackURL = location.origin + '/api/oauth/myuplink/callback';
        return '<div class="mf-eyebrow">MyUplink connection</div>' +
          '<ol style="color:var(--fg-muted);font-size:0.78rem;line-height:1.6;margin:0 0 12px;padding-left:1.2em">' +
          '<li>Open the <a href="https://dev.myuplink.com/apps" target="_blank" rel="noopener" style="color:var(--accent-e)">MyUplink developer portal</a> → <b>Apps</b> → <b>Create new app</b>.</li>' +
          '<li>Set <b>Callback Url</b> to the address shown below (copy it exactly).</li>' +
          '<li>Copy the app\'s <b>Client Identifier</b> and <b>Client Secret</b> into the matching fields below.</li>' +
          '<li><b>Save</b> these settings, then click <b>Connect to MyUplink</b> and sign in.</li>' +
          '</ol>' +
          '<div class="mf-field"><label>Callback URL ' + help('Paste this exact string into the "Callback Url" field of your MyUplink app. It must match the address you use to reach 42-watts.') + '</label>' +
          '<input type="text" class="myuplink-callback-url" value="' + escHtml(callbackURL) + '" readonly onclick="this.select()" style="font-family:var(--mono);font-size:0.8rem"></div>';
      }

      // Connect button + connected badge + manual-URL fallback, rendered
      // below the manifest fields. refresh_token is written server-side by
      // the consent flow and masked on GET /api/config by the server-side
      // name heuristic (any key matching password|token|secret|api_key),
      // so a masked non-empty value round-tripping here = connected.
      function myuplinkConnectHTML(d, idx) {
        var acfg = d.config || {};
        var connected = typeof acfg.refresh_token === 'string' && acfg.refresh_token !== '';
        var connBadge = connected
          ? '<span class="creds-badge creds-saved">✓ Connected</span>'
          : '<span class="creds-badge creds-missing">⚠ Not connected</span>';
        return '<div style="margin-top:12px;display:flex;gap:10px;align-items:center;flex-wrap:wrap">' +
          '<button class="btn-add myuplink-connect-btn" type="button" data-driver-idx="' + idx + '" data-driver-name="' + escHtml(d.name || '') + '">Connect to MyUplink</button>' +
          connBadge +
          '<span class="myuplink-connect-status" data-driver-idx="' + idx + '" style="font-size:0.82rem;color:var(--fg-muted)"></span>' +
          '</div>' +
          // Manual fallback: when the automatic redirect can't reach this
          // device (remote/relay access, or the portal rejected an http LAN
          // callback), the operator copies the redirected URL from the
          // address bar and pastes it here. The Pi exchanges the code over
          // its own outbound HTTPS, so no inbound callback is needed.
          '<details style="margin-top:10px">' +
          '<summary style="cursor:pointer;font-size:0.8rem;color:var(--fg-muted)">Not redirected back? Paste the URL instead</summary>' +
          '<p style="color:var(--fg-muted);font-size:0.72rem;margin:6px 0">After signing in at MyUplink, copy the full address from your browser\'s address bar and paste it here. Use this for remote/relay access or if the page didn\'t return to 42-watts.</p>' +
          '<input type="text" class="myuplink-manual-url" data-driver-idx="' + idx + '" placeholder=".../api/oauth/myuplink/callback?code=...&amp;state=..." style="font-family:var(--mono);font-size:0.78rem">' +
          '<button class="btn-add myuplink-manual-btn" type="button" data-driver-idx="' + idx + '" style="margin-top:6px">Complete connection</button>' +
          '</details>';
      }

      // Wire the Connect + manual-exchange buttons for one driver's slot.
      // Slot-scoped (not a bodyEl sweep): manifest slots fill async after
      // the tab's synchronous handler pass has already run.
      function wireMyUplinkConnect(slot, d, idx) {
        var cbtn = slot.querySelector(".myuplink-connect-btn");
        var statusEl = slot.querySelector(".myuplink-connect-status");
        function setStatus(msg, color) {
          if (statusEl) { statusEl.textContent = msg; statusEl.style.color = color || "var(--fg-muted)"; }
        }
        if (cbtn) cbtn.addEventListener("click", function () {
          var name = cbtn.dataset.driverName || "";
          if (!name) { setStatus("Save the driver name first", "var(--red-e)"); return; }
          setStatus("Opening MyUplink…");
          cbtn.disabled = true;
          var redirectURI = location.origin + "/api/oauth/myuplink/callback";
          var qs = "?driver=" + encodeURIComponent(name) +
            "&redirect_uri=" + encodeURIComponent(redirectURI);
          ownerFetch("/api/oauth/myuplink/start" + qs)
            .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, body: j }; }); })
            .then(function (res) {
              if (!res.ok || !res.body || !res.body.authorize_url) {
                setStatus("✗ " + ((res.body && res.body.error) || "could not start consent — save Client ID + Secret first"), "var(--red-e)");
                return;
              }
              window.open(res.body.authorize_url, "_blank");
              setStatus("Complete the consent in the new tab. If it returns here, reload; if not, paste the URL below.", "var(--green-e)");
            })
            .catch(function (e) { setStatus("✗ " + e.message, "var(--red-e)"); })
            .finally(function () { cbtn.disabled = false; });
        });
        var mbtn = slot.querySelector(".myuplink-manual-btn");
        if (mbtn) mbtn.addEventListener("click", function () {
          var input = slot.querySelector(".myuplink-manual-url");
          var url = input ? input.value.trim() : "";
          if (!url) { setStatus("Paste the redirect URL first", "var(--red-e)"); return; }
          setStatus("Completing…");
          mbtn.disabled = true;
          ownerFetch("/api/oauth/myuplink/exchange", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ redirect_url: url }),
          })
            .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, body: j }; }); })
            .then(function (res) {
              if (res.ok && res.body && res.body.status === "connected") {
                setStatus("✓ Connected — reload to refresh the badge.", "var(--green-e)");
              } else {
                setStatus("✗ " + ((res.body && res.body.error) || "exchange failed"), "var(--red-e)");
              }
            })
            .catch(function (e) { setStatus("✗ " + e.message, "var(--red-e)"); })
            .finally(function () { mbtn.disabled = false; });
        });
      }

      // Raw key/value fallback for drivers with an unparseable or
      // missing manifest: plain inputs bound through the shell's
      // data-path capture, typed by the current value's JS type.
      function renderRawConfigFallback(slot, d, idx) {
        var dcfg = d.config || {};
        var keys = Object.keys(dcfg);
        if (keys.length === 0) return;
        var html = '<div class="mf-eyebrow">Device settings</div>' +
          '<div class="mf-hint">No manifest available for this driver — editing raw config values.</div>' +
          '<div class="mf-fields">';
        keys.forEach(function (key) {
          var v = dcfg[key];
          var label = escHtml(MF ? MF.labelFor(key) : key);
          var path = 'drivers.' + idx + '.config.' + escHtml(key);
          html += '<div class="mf-field">';
          if (typeof v === "boolean") {
            html += '<label class="mf-check"><input type="checkbox" data-checkbox-path="' + path + '"' + (v ? ' checked' : '') + '> ' + label + '</label>';
          } else if (typeof v === "number") {
            html += '<label>' + label + '</label><input type="number" step="any" data-path="' + path + '" value="' + escHtml(String(v)) + '">';
          } else if (/password|token|secret|api_key/i.test(key)) {
            html += '<label>' + label + '</label><input type="password" autocomplete="off" data-path="' + path + '" value="" placeholder="•••••••• (leave empty to keep)">';
          } else {
            html += '<label>' + label + '</label><input type="text" data-path="' + path + '" value="' + escHtml(v == null ? "" : String(v)) + '">';
          }
          html += '</div>';
        });
        html += '</div>';
        slot.innerHTML = html;
      }

      // Tesla/vehicle drivers: verify button + the ip → allowed_hosts
      // autosync. Without the sync a fresh driver keeps allowed_hosts=[]
      // from catalog-add — that's allow-any for HTTP, but a configured
      // list must track the proxy IP the operator typed.
      function wireVehicleAffordance(slot, d, idx) {
        var ipInput = slot.querySelector('.mf-field[data-mf-name="ip"] [data-mf-input]');
        if (ipInput) {
          var syncAllowedHosts = function () {
            if (!d.capabilities) return;
            if (!d.capabilities.http) d.capabilities.http = { allowed_hosts: [] };
            var bare = (ipInput.value || "").trim().split(":")[0];
            d.capabilities.http.allowed_hosts = bare ? [bare] : [];
          };
          ipInput.addEventListener("input", syncAllowedHosts);
          ipInput.addEventListener("blur", syncAllowedHosts);
        }
        var vbtn = slot.querySelector(".tesla-verify-btn");
        if (!vbtn) return;
        vbtn.addEventListener("click", function () {
          var statusEl = slot.querySelector('.tesla-verify-status');
          var vinInput = slot.querySelector('.mf-field[data-mf-name="vin"] [data-mf-input]');
          var ip = ipInput ? ipInput.value.trim() : ((d.config && d.config.ip) || "");
          var vin = vinInput ? vinInput.value.trim() : ((d.config && d.config.vin) || "");
          if (!ip || !vin) {
            if (statusEl) statusEl.textContent = "Enter Proxy IP + VIN first";
            return;
          }
          if (statusEl) { statusEl.textContent = "Verifying…"; statusEl.style.color = "var(--fg-muted)"; }
          vbtn.disabled = true;
          ownerFetch("/api/drivers/verify_tesla", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ ip: ip, vin: vin }),
          }).then(function (r) {
            return r.json().then(function (j) { return { ok: r.ok, status: r.status, body: j }; });
          }).then(function (res) {
            if (!statusEl) return;
            if (res.ok && res.body && res.body.ok) {
              var soc = res.body.soc_pct != null ? Math.round(res.body.soc_pct) + "%" : "?";
              var lim = res.body.charge_limit_pct != null ? Math.round(res.body.charge_limit_pct) + "%" : "?";
              var st = res.body.charging_state || "";
              statusEl.style.color = "var(--green-e)";
              statusEl.textContent = "✓ SoC " + soc + " · limit " + lim + (st ? " · " + st : "");
            } else {
              statusEl.style.color = "var(--red-e)";
              statusEl.textContent = "✗ " + ((res.body && res.body.error) || "verification failed");
            }
          }).catch(function (e) {
            if (statusEl) {
              statusEl.style.color = "var(--red-e)";
              statusEl.textContent = "✗ " + e.message;
            }
          }).finally(function () {
            vbtn.disabled = false;
          });
        });
      }

      // Cloud EV drivers (manifest declares email+password): Connect
      // button lists the account's chargers and fills the serial field.
      function wireCloudConnect(slot, d, idx) {
        var connectBtn = slot.querySelector(".ev-connect-btn");
        if (!connectBtn) return;
        connectBtn.addEventListener("click", function () {
          var statusEl = document.getElementById("ev-connect-status-" + idx);
          var emailInput = slot.querySelector('.mf-field[data-mf-name="email"] [data-mf-input]');
          var pwInput = slot.querySelector('.mf-field[data-mf-name="password"] [data-mf-input]');
          var email = emailInput ? emailInput.value : ((d.config && d.config.email) || "");
          var pw = pwInput ? pwInput.value : "";
          if (!email) { if (statusEl) statusEl.textContent = "Enter email first"; return; }
          if (statusEl) statusEl.textContent = "Connecting...";
          connectBtn.disabled = true;
          var provider = "easee";
          if (typeof d.lua === "string" && d.lua !== "") {
            provider = d.lua.replace(/^.*[\\/]/, "").replace(/\.lua$/i, "").replace(/_cloud$/i, "") || "easee";
          } else if (typeof d.driver === "string" && d.driver !== "") {
            provider = d.driver.split("@")[0].replace(/[-_]cloud$/i, "") || "easee";
          }
          ownerFetch("/api/ev/chargers", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ provider: provider, email: email, password: pw }),
          }).then(function (r) {
            if (!r.ok) return r.json().then(function (j) { throw new Error(j.error || "HTTP " + r.status); });
            return r.json();
          }).then(function (chargers) {
            if (!Array.isArray(chargers) || chargers.length === 0) {
              if (statusEl) statusEl.textContent = "No chargers found";
              return;
            }
            var serialField = slot.querySelector('.mf-field[data-mf-name="serial"]');
            var serialInput = serialField && serialField.querySelector("[data-mf-input]");
            var current = (d.config && d.config.serial) || "";
            var applySerial = function (id) {
              if (serialInput) serialInput.value = id;
              if (!d.config) d.config = {};
              d.config.serial = id;
            };
            if (serialField && chargers.length > 1) {
              // More than one charger — inject a picker under the field.
              var sel = serialField.querySelector("select.ev-charger-pick");
              if (!sel) {
                sel = document.createElement("select");
                sel.className = "ev-charger-pick";
                serialField.appendChild(sel);
              }
              sel.innerHTML = "";
              chargers.forEach(function (ch) {
                var opt = document.createElement("option");
                opt.value = ch.id;
                opt.textContent = ch.id + (ch.name ? "  —  " + ch.name : "");
                if (ch.id === current) opt.selected = true;
                sel.appendChild(opt);
              });
              sel.onchange = function () { applySerial(sel.value); };
              applySerial(sel.value);
            } else {
              applySerial(chargers[0].id);
            }
            if (config.ev_charger) config.ev_charger.serial = d.config.serial;
            if (statusEl) statusEl.textContent = chargers.length + " charger(s) found";
          }).catch(function (e) {
            if (statusEl) statusEl.textContent = "Error: " + e.message;
          }).finally(function () {
            connectBtn.disabled = false;
          });
        });
      }

      function fillAllSlots() {
        bodyEl.querySelectorAll(".drv-mf-slot").forEach(function (slot) {
          var dIdx = parseInt(slot.getAttribute("data-driver-idx"), 10);
          var d = config.drivers[dIdx];
          if (!d) return;
          fillManifestSlot(slot, d, dIdx, manifestForDriver(d));
        });
        // Battery-capacity reveal now that manifests are resolvable.
        bodyEl.querySelectorAll(".driver-battery-capacity").forEach(function (wrap) {
          var dIdx = parseInt(wrap.getAttribute("data-driver-idx"), 10);
          var d = config.drivers[dIdx];
          var row = wrap.closest(".device-core-row");
          var show = d ? driverEmits(d, "battery") : false;
          wrap.hidden = !show;
          if (row) row.classList.toggle("field-row-single", !show);
        });
      }

      // Registry-pinned drivers: fetch each distinct ref's manifest once
      // (server caches for 5 min; we also cache client-side per session).
      function fetchRefManifests() {
        var refs = {};
        (config.drivers || []).forEach(function (d) {
          if (d.driver && !S.manifestByRef[d.driver]) refs[d.driver] = true;
        });
        var pending = Object.keys(refs);
        if (pending.length === 0) return;
        pending.forEach(function (ref) {
          var at = ref.indexOf("@");
          if (at <= 0) return;
          var name = ref.slice(0, at), version = ref.slice(at + 1);
          ownerFetch("/api/registry/drivers/" + encodeURIComponent(name) + "/" + encodeURIComponent(version) + "/manifest")
            .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
            .then(function (man) {
              S.manifestByRef[ref] = man;
              fillAllSlots();
            })
            .catch(function () { /* raw fallback stays */ });
        });
      }

      // ---- Driver catalog picker (bundled) ----
      ownerFetch("/api/drivers/catalog").then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      }).then(function (data) {
        var entries = (data && data.entries) || [];
        var byLua = {};
        entries.forEach(function (e) { if (e && e.path) byLua[e.path] = e; });
        // Cache by-lua so synchronous render passes (and the Loadpoints
        // tab) can resolve manifests without re-fetching.
        S.catalogByLua = byLua;
        fillAllSlots();
        var sel = document.getElementById("driver-catalog-picker");
        if (!sel) return;
        sel.innerHTML = "";
        if (entries.length === 0) {
          sel.innerHTML = "<option value=''>(no drivers found in drivers/)</option>";
          return;
        }
        entries.forEach(function (e) {
          var opt = document.createElement("option");
          opt.value = e.path;
          var protoLabel = (e.protocols || []).join("+");
          var status = MF ? MF.verificationStatus(e) : "experimental";
          var label = (MF && MF.displayName(e)) || e.filename;
          opt.textContent = verificationBadge(status) + label + "  —  " + (e.manufacturer || "?") + "  [" + protoLabel + "]" + (e.version ? "  v" + e.version : "");
          opt.dataset.id = e.id || "";
          var notes = MF ? MF.verificationNotes(e) : "";
          if (notes) opt.title = notes;
          sel.appendChild(opt);
        });
      }).catch(function (e) {
        // Catalog unreachable (offline Pi, server hiccup): the tab must
        // still render every configured driver. fillAllSlots with no
        // catalog resolves lua drivers to a null manifest → raw
        // key/value fallback editor; the battery-capacity reveal pass
        // runs too. The picker gets a real message instead of a stuck
        // "Loading catalog…".
        fillAllSlots();
        var sel = document.getElementById("driver-catalog-picker");
        if (sel) sel.innerHTML = "<option value=''>(catalog unavailable — " + escHtml(e.message) + ")</option>";
      });

      fetchRefManifests();

      var btn = document.getElementById("driver-catalog-add");
      if (btn) btn.addEventListener("click", function () {
        var sel = document.getElementById("driver-catalog-picker");
        var nameEl = document.getElementById("driver-catalog-name");
        if (!sel || !sel.value) return;
        ctx.captureCurrentTab();
        var entry = (S.catalogByLua || {})[sel.value];
        var chosen = sel.options[sel.selectedIndex];
        var name = (nameEl.value || "").trim() || chosen.dataset.id || defaultDriverName((entry && entry.name) || "driver", config);
        config.drivers.push(driverFromManifest(name, entry, sel.value, null));
        ctx.renderTab("devices");
      });

      // ---- Source toggle + Sourceful registry flow ----
      var bundledPane = document.getElementById("driver-add-bundled");
      var registryPane = document.getElementById("driver-add-registry");
      var registryLoaded = false;
      bodyEl.querySelectorAll(".mf-source-btn").forEach(function (srcBtn) {
        srcBtn.addEventListener("click", function () {
          bodyEl.querySelectorAll(".mf-source-btn").forEach(function (b) {
            b.classList.toggle("active", b === srcBtn);
          });
          var src = srcBtn.getAttribute("data-source");
          if (bundledPane) bundledPane.hidden = src !== "bundled";
          if (registryPane) registryPane.hidden = src !== "registry";
          if (src === "registry" && !registryLoaded) {
            registryLoaded = true;
            loadRegistryList();
          }
        });
      });

      function registryStatus(msg, isError) {
        var el = document.getElementById("registry-status");
        if (!el) return;
        el.textContent = msg || "";
        el.className = "mf-registry-status" + (isError ? " error" : "");
      }

      function loadRegistryList() {
        var sel = document.getElementById("registry-driver-picker");
        if (!sel) return;
        sel.innerHTML = "<option value=''>Loading registry…</option>";
        registryStatus("");
        ownerFetch("/api/registry/drivers")
          .then(function (r) {
            if (!r.ok) return r.json().catch(function () { return {}; }).then(function (j) {
              throw new Error(j.error || ("HTTP " + r.status));
            });
            return r.json();
          })
          .then(function (data) {
            var list = (data && data.drivers) || [];
            sel.innerHTML = "";
            if (list.length === 0) {
              sel.innerHTML = "<option value=''>(registry is empty)</option>";
              return;
            }
            list.forEach(function (rd) {
              var opt = document.createElement("option");
              opt.value = rd.name;
              var label = rd.display_name || rd.name;
              if (rd.tier) label += "  ·  " + rd.tier;
              if (rd.latest_version) label += "  ·  latest " + rd.latest_version;
              opt.textContent = label;
              opt.dataset.latestVersion = rd.latest_version || "";
              if (rd.description) opt.title = rd.description;
              sel.appendChild(opt);
            });
            loadRegistryVersions(sel.value);
          })
          .catch(function (e) {
            sel.innerHTML = "<option value=''>(registry unavailable)</option>";
            registryStatus("Registry unreachable — bundled drivers still work offline. " + e.message, true);
          });
      }

      function loadRegistryVersions(name) {
        var vsel = document.getElementById("registry-version-picker");
        if (!vsel) return;
        if (!name) { vsel.innerHTML = "<option value=''>—</option>"; return; }
        vsel.innerHTML = "<option value=''>Loading…</option>";
        ownerFetch("/api/registry/drivers/" + encodeURIComponent(name) + "/versions")
          .then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
          })
          .then(function (data) {
            var versions = (data && data.versions) || [];
            vsel.innerHTML = "";
            if (versions.length === 0) {
              vsel.innerHTML = "<option value=''>(no versions)</option>";
              return;
            }
            // Registry delivers newest-first; default = latest.
            versions.forEach(function (v, i) {
              var opt = document.createElement("option");
              opt.value = v.version;
              opt.textContent = v.version + (i === 0 ? "  (latest)" : "");
              if (v.changelog) opt.title = v.changelog;
              vsel.appendChild(opt);
            });
          })
          .catch(function (e) {
            vsel.innerHTML = "<option value=''>(unavailable)</option>";
            registryStatus("Failed to load versions: " + e.message, true);
          });
      }

      var regPicker = document.getElementById("registry-driver-picker");
      if (regPicker) regPicker.addEventListener("change", function () {
        loadRegistryVersions(regPicker.value);
      });

      var regRefresh = document.getElementById("registry-refresh");
      if (regRefresh) regRefresh.addEventListener("click", function () {
        regRefresh.disabled = true;
        registryStatus("Refreshing…");
        ownerFetch("/api/registry/refresh", { method: "POST" })
          .then(function () {
            S.manifestByRef = {};
            loadRegistryList();
          })
          .catch(function (e) { registryStatus("Refresh failed: " + e.message, true); })
          .finally(function () { regRefresh.disabled = false; });
      });

      var regAdd = document.getElementById("registry-driver-add");
      if (regAdd) regAdd.addEventListener("click", function () {
        var vsel = document.getElementById("registry-version-picker");
        var nameEl = document.getElementById("registry-driver-name");
        var drvName = regPicker && regPicker.value;
        var version = vsel && vsel.value;
        if (!drvName || !version) {
          registryStatus("Pick a driver and version first", true);
          return;
        }
        var ref = drvName + "@" + version;
        regAdd.disabled = true;
        registryStatus("Fetching manifest…");
        ownerFetch("/api/registry/drivers/" + encodeURIComponent(drvName) + "/" + encodeURIComponent(version) + "/manifest")
          .then(function (r) {
            if (!r.ok) return r.json().catch(function () { return {}; }).then(function (j) {
              throw new Error(j.error || ("HTTP " + r.status));
            });
            return r.json();
          })
          .then(function (man) {
            S.manifestByRef[ref] = man;
            ctx.captureCurrentTab();
            var name = (nameEl && nameEl.value.trim()) || defaultDriverName(drvName, config);
            config.drivers.push(driverFromManifest(name, man, null, ref));
            ctx.renderTab("devices");
          })
          .catch(function (e) {
            registryStatus("Failed to fetch manifest: " + e.message, true);
          })
          .finally(function () { regAdd.disabled = false; });
      });

      // ---- Read-only toggle → live form de-emphasis ----
      bodyEl.querySelectorAll(".drv-telemetry-only").forEach(function (cb) {
        cb.addEventListener("change", function () {
          var dIdx = parseInt(cb.getAttribute("data-driver-idx"), 10);
          var form = S._deviceForms[dIdx];
          if (form) form.setTelemetryOnly(cb.checked);
        });
      });

      // MyUplink OAuth Connect + manual-exchange handlers are wired
      // per-slot in wireMyUplinkConnect (manifest slots fill async, after
      // this synchronous handler pass).

      // Generic driver probe. Runs the current row's unsaved config through a
      // short-lived backend driver instance and dumps live readings/metrics
      // inline so the operator can verify host, credentials, and protocol.
      bodyEl.querySelectorAll(".driver-test-btn").forEach(function (testBtn) {
        testBtn.addEventListener("click", function () {
          var dIdx = testBtn.dataset.driverIdx;
          var statusEl = bodyEl.querySelector('.driver-test-status[data-driver-idx="' + dIdx + '"]');
          var outputEl = bodyEl.querySelector('.driver-test-output[data-driver-idx="' + dIdx + '"]');
          ctx.captureCurrentTab();
          var driver = config.drivers && config.drivers[dIdx];
          if (!driver) return;
          if (statusEl) {
            statusEl.textContent = "Testing...";
            statusEl.className = "driver-test-status";
          }
          if (outputEl) {
            outputEl.hidden = false;
            outputEl.innerHTML = '<div class="driver-test-empty">Waiting for live values...</div>';
          }
          testBtn.disabled = true;
          ownerFetch("/api/drivers/test", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(driver),
          }).then(function (r) {
            return r.json().then(function (j) { return { ok: r.ok, status: r.status, body: j }; });
          }).then(function (res) {
            var body = res.body || {};
            if (!res.ok) {
              body = { ok: false, error: body.error || ("HTTP " + res.status) };
            }
            if (statusEl) {
              statusEl.textContent = body.ok ? "Connected" : "Failed";
              statusEl.className = "driver-test-status " + (body.ok ? "ok" : "error");
            }
            if (outputEl) {
              outputEl.hidden = false;
              outputEl.innerHTML = renderProbeOutput(body);
            }
          }).catch(function (e) {
            if (statusEl) {
              statusEl.textContent = "Failed";
              statusEl.className = "driver-test-status error";
            }
            if (outputEl) {
              outputEl.hidden = false;
              outputEl.innerHTML = '<div class="driver-test-error">' + escHtml(e.message) + '</div>';
            }
          }).finally(function () {
            testBtn.disabled = false;
          });
        });
      });

      // Add/remove-device buttons.
      var addMqtt = document.getElementById("add-mqtt");
      var addModbus = document.getElementById("add-modbus");
      if (addMqtt) addMqtt.addEventListener("click", function () {
        ctx.captureCurrentTab();
        config.drivers.push({
          name: "new-device-" + (config.drivers.length + 1),
          lua: "drivers/new.lua",
          is_site_meter: false,
          battery_capacity_wh: 0,
          mqtt: { host: "", port: 1883, username: "", password: "" },
        });
        ctx.renderTab("devices");
      });
      if (addModbus) addModbus.addEventListener("click", function () {
        ctx.captureCurrentTab();
        config.drivers.push({
          name: "new-device-" + (config.drivers.length + 1),
          lua: "drivers/new.lua",
          is_site_meter: false,
          battery_capacity_wh: 0,
          modbus: { host: "", port: 502, unit_id: 1 },
        });
        ctx.renderTab("devices");
      });
      bodyEl.querySelectorAll("[data-remove-idx]").forEach(function (rmBtn) {
        rmBtn.addEventListener("click", function () {
          var idx = parseInt(rmBtn.dataset.removeIdx);
          ctx.captureCurrentTab();
          config.drivers.splice(idx, 1);
          ctx.renderTab("devices");
        });
      });
    },
  };
})();
