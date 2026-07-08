// node --test web/settings/tabs/devices.test.mjs
//
// Behavioural tests for the Settings → Devices tab. devices.js is an
// IIFE onto window.FTWSettings; the repo ships no DOM polyfill (see
// web/setup.test.mjs), so we evaluate the source in a vm sandbox with a
// hand-rolled stub DOM that covers exactly the surface the after() fill
// pass touches. That is enough to execute the real async manifest-slot
// pipeline (catalog fetch → fillAllSlots → fillManifestSlot →
// myuplinkSetupHTML), which a node --check or a pure regex test can't:
// the H1 regression here was a *runtime* scope bug (`help` aliased only
// in render(), referenced from after()'s myuplinkSetupHTML) that
// crashed the whole tab for any config containing a MyUplink driver.

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import vm from "node:vm";

const __dirname = dirname(fileURLToPath(import.meta.url));
const MF_SRC = readFileSync(join(__dirname, "..", "manifest-form.js"), "utf8");
const DEVICES_SRC = readFileSync(join(__dirname, "devices.js"), "utf8");

// Minimal stub element: just the properties/methods the after() pass
// reads on slots, pickers, buttons and banners.
function stubEl(over) {
  return Object.assign({
    innerHTML: "",
    textContent: "",
    value: "",
    hidden: false,
    disabled: false,
    attrs: {},
    dataset: {},
    handlers: {},
    addEventListener(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); },
    getAttribute(n) { return Object.prototype.hasOwnProperty.call(this.attrs, n) ? this.attrs[n] : null; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    appendChild() {},
    closest() { return null; },
    classList: { calls: [], toggle(c, on) { this.calls.push([c, on]); }, add() {}, remove() {} },
  }, over || {});
}

// Boot a fresh sandbox with manifest-form.js + devices.js loaded and a
// document.getElementById backed by the given element map.
function boot(elements) {
  const sandbox = {
    window: {},
    document: {
      getElementById(id) { return (elements && elements[id]) || null; },
      createElement() { return stubEl(); },
    },
    location: { origin: "http://pi.local:8080" },
    console,
  };
  vm.createContext(sandbox);
  vm.runInContext(MF_SRC, sandbox, { filename: "manifest-form.js" });
  vm.runInContext(DEVICES_SRC, sandbox, { filename: "devices.js" });
  return sandbox;
}

function makeCtx(sandbox, config, bodyEl) {
  const helpCalls = [];
  return {
    helpCalls,
    ctx: {
      config,
      bodyEl,
      escHtml: (s) => String(s == null ? "" : s)
        .replace(/&/g, "&amp;").replace(/</g, "&lt;")
        .replace(/>/g, "&gt;").replace(/"/g, "&quot;"),
      help: (t) => { helpCalls.push(t); return '<span class="help" data-help="' + t + '">?</span>'; },
      captureCurrentTab() {},
      renderTab() {},
      ownerFetch: sandbox.window.ownerFetch,
    },
  };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

// A MyUplink-shaped catalog entry: OAuth authorization-code driver
// (client_id + client_secret) with the optional http_hosts extension.
const MYUPLINK_ENTRY = {
  path: "drivers/myuplink.lua",
  name: "myuplink",
  display_name: "MyUplink",
  manufacturer: "myUplink",
  protocols: ["http"],
  http_hosts: ["api.myuplink.com"],
  requires: [
    { name: "client_id", purpose: "always", type: "string", help: "Client Identifier from the developer portal." },
    { name: "client_secret", purpose: "always", type: "string", secret: true },
  ],
  options: [],
  provides: { live: ["battery.soc", "meter.w"] },
  verification: { status: "beta" },
};

describe("devices tab — MyUplink slot fills without a scope crash (H1)", () => {
  it("after() renders the OAuth setup block (help() must be in after()'s scope)", async () => {
    const slot = stubEl({ attrs: { "data-driver-idx": "0" } });
    const bodyEl = stubEl({
      querySelectorAll: (q) => (q === ".drv-mf-slot" ? [slot] : []),
    });
    const config = {
      drivers: [{
        name: "myuplink-house",
        lua: "drivers/myuplink.lua",
        capabilities: { http: { allowed_hosts: [] } },
        config: {},
      }],
    };
    const sandbox = boot(null);
    sandbox.window.ownerFetch = (path) =>
      path === "/api/drivers/catalog"
        ? Promise.resolve({ ok: true, json: async () => ({ entries: [MYUPLINK_ENTRY] }) })
        : Promise.reject(new Error("unexpected fetch " + path));

    const { ctx, helpCalls } = makeCtx(sandbox, config, bodyEl);
    sandbox.window.FTWSettings.tabs.devices.after(ctx);
    await tick();

    // With the scope bug, the ReferenceError aborted the fill pass and
    // the slot stayed empty — assert the actual rendered output.
    assert.match(slot.innerHTML, /MyUplink connection/,
      "the OAuth setup block must render for a MyUplink driver");
    assert.match(slot.innerHTML, /\/api\/oauth\/myuplink\/callback/,
      "the callback URL must be shown");
    assert.match(slot.innerHTML, /data-mf-name="client_id"/,
      "manifest fields must render alongside the OAuth block");
    assert.ok(helpCalls.length > 0,
      "myuplinkSetupHTML must reach ctx.help through the after()-scope alias");
  });
});

describe("devices tab — catalog fetch failure degrades gracefully (M1)", () => {
  it("fills slots with the raw fallback and unsticks the picker", async () => {
    const slot = stubEl({ attrs: { "data-driver-idx": "0" } });
    const row = stubEl();
    const wrap = stubEl({ attrs: { "data-driver-idx": "0" }, closest: () => row });
    const picker = stubEl();
    const bodyEl = stubEl({
      querySelectorAll: (q) =>
        q === ".drv-mf-slot" ? [slot] :
        q === ".driver-battery-capacity" ? [wrap] : [],
    });
    const config = {
      drivers: [{
        name: "legacy-box",
        lua: "drivers/legacy.lua",
        config: { host: "192.168.1.50", api_token: "shh" },
      }],
    };
    const sandbox = boot({ "driver-catalog-picker": picker });
    sandbox.window.ownerFetch = () => Promise.reject(new Error("network down"));

    const { ctx } = makeCtx(sandbox, config, bodyEl);
    sandbox.window.FTWSettings.tabs.devices.after(ctx);
    await tick();

    assert.match(slot.innerHTML, /No manifest available/,
      "raw config fallback must still render when the catalog is unreachable");
    assert.match(picker.innerHTML, /catalog unavailable/,
      "the picker must not stay stuck on 'Loading catalog…'");
    assert.equal(wrap.hidden, true,
      "the battery-capacity reveal pass must still run (no manifest → hidden)");
    assert.deepEqual(row.classList.calls, [["field-row-single", true]]);
  });
});

describe("devices tab — driverFromManifest seeds allowed_hosts from http_hosts (M3)", () => {
  it("catalog-add produces capabilities.http.allowed_hosts = manifest http_hosts", async () => {
    const picker = stubEl({
      value: "drivers/myuplink.lua",
      selectedIndex: 0,
      options: [{ dataset: { id: "myuplink" } }],
    });
    const addBtn = stubEl();
    const nameEl = stubEl({ value: "" });
    const bodyEl = stubEl();
    const config = { drivers: [] };
    const sandbox = boot({
      "driver-catalog-picker": picker,
      "driver-catalog-add": addBtn,
      "driver-catalog-name": nameEl,
    });
    sandbox.window.ownerFetch = () =>
      Promise.resolve({ ok: true, json: async () => ({ entries: [MYUPLINK_ENTRY] }) });

    const { ctx } = makeCtx(sandbox, config, bodyEl);
    sandbox.window.FTWSettings.tabs.devices.after(ctx);
    await tick();

    assert.equal(addBtn.handlers.click.length, 1);
    addBtn.handlers.click[0]();
    assert.equal(config.drivers.length, 1);
    const d = config.drivers[0];
    assert.equal(d.name, "myuplink");
    assert.equal(d.lua, "drivers/myuplink.lua");
    assert.equal(JSON.stringify(d.capabilities.http.allowed_hosts), '["api.myuplink.com"]',
      "http_hosts from the manifest must seed the HTTP allowlist");
  });

  it("tolerates the http_hosts field being absent (empty allowlist)", async () => {
    const entry = { ...MYUPLINK_ENTRY };
    delete entry.http_hosts;
    const picker = stubEl({
      value: "drivers/myuplink.lua",
      selectedIndex: 0,
      options: [{ dataset: { id: "myuplink" } }],
    });
    const addBtn = stubEl();
    const nameEl = stubEl({ value: "" });
    const config = { drivers: [] };
    const sandbox = boot({
      "driver-catalog-picker": picker,
      "driver-catalog-add": addBtn,
      "driver-catalog-name": nameEl,
    });
    sandbox.window.ownerFetch = () =>
      Promise.resolve({ ok: true, json: async () => ({ entries: [entry] }) });

    const { ctx } = makeCtx(sandbox, config, stubEl());
    sandbox.window.FTWSettings.tabs.devices.after(ctx);
    await tick();

    addBtn.handlers.click[0]();
    assert.equal(JSON.stringify(config.drivers[0].capabilities.http.allowed_hosts), "[]");
  });
});

describe("devices tab — onSaveError prefers manifest_errors[] (L1)", () => {
  function run(message, body) {
    const banner = stubEl();
    const bodyEl = stubEl({
      querySelector: (q) => (q === ".drv-save-banner" ? banner : null),
    });
    const sandbox = boot(null);
    const { ctx } = makeCtx(sandbox, { drivers: [] }, bodyEl);
    sandbox.window.FTWSettings.tabs.devices.onSaveError(message, ctx, body);
    return banner;
  }

  it("keeps a message whose help text contains '; ' intact", () => {
    const msg = 'driver "myuplink-house": required field "client_id" is missing — create an app; copy the Client Identifier';
    const banner = run("validation: " + msg, { manifest_errors: [msg] });
    assert.equal(banner.hidden, false);
    assert.match(banner.textContent, /create an app; copy the Client Identifier/,
      "the array path must not split messages on '; '");
  });

  it("falls back to splitting the joined error string when no body is available", () => {
    const banner = run(
      'validation: driver "a": required field "x" is missing; driver "b": required field "y" is missing',
      undefined
    );
    assert.equal(banner.hidden, false);
    assert.match(banner.textContent, /driver "a"/);
    assert.match(banner.textContent, /driver "b"/);
  });
});
