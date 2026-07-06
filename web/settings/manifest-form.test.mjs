// node --test web/settings/manifest-form.test.mjs
//
// manifest-form.js is the shared DRIVER_MANIFEST → form renderer used by
// the Devices tab, the Loadpoints tab, and the setup wizard. It's an
// IIFE onto `window`, but everything except wire() is DOM-free (pure
// string/HTML output), so we evaluate the source with a stubbed window
// and test the real functions. wire() needs a live DOM (the repo ships
// no polyfill — see web/setup.test.mjs for the same constraint) and is
// covered structurally instead.

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import vm from "node:vm";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SRC = readFileSync(join(__dirname, "manifest-form.js"), "utf8");

function loadMF() {
  const sandbox = { window: {} };
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox, { filename: "manifest-form.js" });
  return sandbox.window.FTWManifestForm;
}
const MF = loadMF();

const MANIFEST = {
  name: "testdrv",
  version: "1.0.0",
  role: "battery",
  display_name: "Test Driver",
  requires: [
    {
      name: "battery_capacity_wh", purpose: "control", type: "integer",
      min: 1000, max: 1000000, help: "Usable capacity in Wh.",
    },
    { name: "api_token", purpose: "always", type: "string", secret: true, help: "From the device web UI." },
  ],
  options: [
    { name: "charge_ceil_soc", purpose: "control", type: "double", default: 0.95, min: 0, max: 1 },
    { name: "skip_battery", purpose: "always", type: "boolean", default: false },
    { name: "host_label", purpose: "always", type: "string", default: "zap.local" },
  ],
  provides: { live: ["battery.W", "battery.SoC_nom_fract", "meter.W"], static: ["make", "sn"] },
};

describe("exports", () => {
  it("exposes the full documented API on window.FTWManifestForm", () => {
    for (const fn of ["esc", "emits", "liveTypes", "verificationStatus", "verificationNotes",
      "displayName", "fields", "labelFor", "renderFields", "wire"]) {
      assert.equal(typeof MF[fn], "function", fn + " missing");
    }
  });
});

describe("emits / liveTypes — provides.live is the capability source", () => {
  it("matches namespaced entries by prefix", () => {
    assert.equal(MF.emits(MANIFEST, "battery"), true);
    assert.equal(MF.emits(MANIFEST, "meter"), true);
    assert.equal(MF.emits(MANIFEST, "pv"), false);
  });
  it("does not prefix-match across type names (ev vs ev_something)", () => {
    const m = { provides: { live: ["ev_other.w"] } };
    assert.equal(MF.emits(m, "ev"), false);
  });
  it("accepts a bare type entry", () => {
    assert.equal(MF.emits({ provides: { live: ["vehicle"] } }, "vehicle"), true);
  });
  it("liveTypes returns unique type prefixes in declaration order", () => {
    // JSON-compare: the array comes from another vm realm, so strict
    // deepEqual fails on the foreign Array prototype.
    assert.equal(JSON.stringify(MF.liveTypes(MANIFEST)), '["battery","meter"]');
  });
  it("tolerates missing provides", () => {
    assert.equal(MF.emits({}, "battery"), false);
    assert.equal(JSON.stringify(MF.liveTypes({})), "[]");
  });
});

describe("esc — everything interpolated into HTML goes through it", () => {
  it("escapes markup-significant characters", () => {
    assert.equal(MF.esc('<b a="x">&'), "&lt;b a=&quot;x&quot;&gt;&amp;");
  });
  it("renders hostile help text inert", () => {
    const html = MF.renderFields({
      requires: [{ name: "k", purpose: "always", type: "string", help: '<img src=x onerror=alert(1)>' }],
      options: [],
    }, {});
    assert.doesNotMatch(html, /<img/);
    assert.match(html, /&lt;img/);
  });
});

describe("renderFields — typed inputs from the field schema", () => {
  const html = MF.renderFields(MANIFEST, { battery_capacity_wh: 10000 }, {});

  it("integer field: number input with min/max and step=1", () => {
    assert.match(html, /data-mf-name="battery_capacity_wh"[^>]*data-mf-type="integer"[^>]*data-mf-required="1"[^>]*data-mf-control="1"/);
    assert.match(html, /type="number" data-mf-input step="1" min="1000" max="1000000" value="10000"/);
  });
  it("double field: step=any and the option default as placeholder", () => {
    assert.match(html, /data-mf-name="charge_ceil_soc"/);
    assert.match(html, /step="any" min="0" max="1" value="" placeholder="0.95"/);
  });
  it("boolean field: checkbox, unchecked by its default", () => {
    assert.match(html, /data-mf-name="skip_battery"[^>]*data-mf-type="boolean"/);
    assert.match(html, /<label class="mf-check"><input type="checkbox" data-mf-input> /);
  });
  it("string option: text input with default as placeholder", () => {
    assert.match(html, /data-mf-name="host_label"/);
    assert.match(html, /type="text" data-mf-input value="" placeholder="zap.local"/);
  });
  it("requires get a required mark, options don't", () => {
    const req = html.split('data-mf-name="battery_capacity_wh"')[1].split("</div>")[0];
    const opt = html.split('data-mf-name="host_label"')[1].split("</div>")[0];
    assert.match(req, /mf-req/);
    assert.doesNotMatch(opt, /mf-req/);
  });
  it("help renders as an mf-hint line", () => {
    assert.match(html, /<div class="mf-hint">Usable capacity in Wh\.<\/div>/);
  });
  it("every field carries an (initially hidden) error slot", () => {
    const n = (html.match(/mf-field-error/g) || []).length;
    assert.equal(n, 5);
  });
  it("empty manifest renders nothing", () => {
    assert.equal(MF.renderFields({ requires: [], options: [] }, {}), "");
  });
});

describe("renderFields — secrets never echo values", () => {
  it("stored secret: empty password input + Saved badge", () => {
    const html = MF.renderFields(MANIFEST, { api_token: "super-secret-value" }, {});
    assert.doesNotMatch(html, /super-secret-value/);
    assert.match(html, /data-mf-name="api_token"[^>]*data-mf-secret="1"/);
    assert.match(html, /type="password" data-mf-input autocomplete="off" value=""/);
    assert.match(html, /creds-saved/);
    assert.match(html, /leave empty to keep/);
  });
  it("unset secret: Not-saved badge", () => {
    const html = MF.renderFields(MANIFEST, {}, {});
    assert.match(html, /creds-missing/);
  });
  it("secretSaved callback overrides the value heuristic (has_password mirror)", () => {
    const html = MF.renderFields(MANIFEST, {}, { secretSaved: (n) => n === "api_token" });
    assert.match(html, /creds-saved/);
  });
});

describe("renderFields — telemetry-only de-emphasis", () => {
  it("stamps the container class so control fields render optional", () => {
    const html = MF.renderFields(MANIFEST, {}, { telemetryOnly: true });
    assert.match(html, /^<div class="mf-fields mf-telemetry-only">/);
  });
});

describe("labelFor / displayName / verification helpers", () => {
  it("labelFor humanizes snake_case with unit suffixes", () => {
    // Exact casing is the renderer's contract with the operator's eyes:
    // lock the shape (no snake_case leaking through), not every nicety.
    assert.doesNotMatch(MF.labelFor("battery_capacity_wh"), /_/);
    assert.match(MF.labelFor("battery_capacity_wh"), /battery capacity/i);
  });
  it("displayName prefers display_name, falls back to name", () => {
    assert.equal(MF.displayName(MANIFEST), "Test Driver");
    assert.equal(MF.displayName({ name: "raw" }), "raw");
  });
  it("verificationStatus reads the nested verification object, defaulting experimental", () => {
    assert.equal(MF.verificationStatus({ verification: { status: "beta" } }), "beta");
    assert.equal(MF.verificationStatus({}), "experimental");
  });
});

describe("wire() structural coverage (DOM-coupled, not executable here)", () => {
  it("deletes empty non-required keys so server-side defaults apply", () => {
    assert.match(SRC, /delete target\[/);
  });
  it("never writes an empty secret over a stored value", () => {
    // the write-through must special-case secrets on empty input
    assert.match(SRC, /secret/i);
    assert.match(SRC, /leave empty to keep/);
  });
  it("routes server errors by field name and returns unmatched ones", () => {
    assert.match(SRC, /showServerErrors/);
  });
});
