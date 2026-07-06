// manifest-form.js — shared DRIVER_MANIFEST → HTML form renderer.
//
// Consumed by the Settings Devices tab (web/settings/tabs/devices.js),
// the Loadpoints tab, and the setup wizard (web/setup.js). Renders the
// typed requires/options field schema a driver declares in its
// DRIVER_MANIFEST as real inputs (number with min/max/step, checkbox,
// text, password for secret=true), keeps a target config object in
// sync with CORRECT JSON types (number / boolean / string — never
// stringly-typed numbers), and validates required fields + bounds
// client-side before a save round-trip.
//
// Dependency-free vanilla JS, same IIFE-onto-window style as the
// settings tab modules. No frameworks, no DOM libraries.
(function () {
  "use strict";

  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // emits(entry, type): does this manifest promise live telemetry of the
  // given DER type? provides.live entries are "<type>.<key>" (or a bare
  // "<type>"). Replaces the old catalog `capabilities` array — the UI's
  // battery / meter / pv / ev / vehicle checks all route through here.
  function emits(entry, type) {
    var live = (entry && entry.provides && entry.provides.live) || [];
    for (var i = 0; i < live.length; i++) {
      var t = String(live[i]);
      if (t === type || t.indexOf(type + ".") === 0) return true;
    }
    return false;
  }

  // liveTypes(entry): unique DER-type prefixes from provides.live, e.g.
  // ["meter", "pv"]. Used for human-readable driver labels.
  function liveTypes(entry) {
    var live = (entry && entry.provides && entry.provides.live) || [];
    var seen = {};
    var out = [];
    live.forEach(function (t) {
      var p = String(t).split(".")[0];
      if (p && !seen[p]) { seen[p] = true; out.push(p); }
    });
    return out;
  }

  function verificationStatus(entry) {
    return (entry && entry.verification && entry.verification.status) || "experimental";
  }

  function verificationNotes(entry) {
    return (entry && entry.verification && entry.verification.notes) || "";
  }

  function displayName(entry) {
    return (entry && (entry.display_name || entry.name)) || "";
  }

  // fields(manifest): requires (required=true) then options
  // (required=false), in declaration order.
  function fields(manifest) {
    var out = [];
    ((manifest && manifest.requires) || []).forEach(function (f) {
      out.push({ field: f, required: true });
    });
    ((manifest && manifest.options) || []).forEach(function (f) {
      out.push({ field: f, required: false });
    });
    return out;
  }

  function labelFor(name) {
    return String(name).replace(/_/g, " ").replace(/\b\w/g, function (c) {
      return c.toUpperCase();
    });
  }

  function isNumeric(type) {
    return type === "integer" || type === "double";
  }

  // renderFields(manifest, values, opts) → HTML string.
  //
  //   values          current driver config map (typed) or null
  //   opts.telemetryOnly  initial read-only state (control fields de-emphasized)
  //   opts.secretSaved(name) → bool  "a value is stored server-side"
  //
  // Callers innerHTML the result into a container and then call
  // wire(container, manifest, target, opts) to get a controller.
  function renderFields(manifest, values, opts) {
    opts = opts || {};
    values = values || {};
    var list = fields(manifest);
    if (list.length === 0) return "";
    var html = '<div class="mf-fields' + (opts.telemetryOnly ? " mf-telemetry-only" : "") + '">';
    list.forEach(function (item) {
      var f = item.field;
      var attrs = ' data-mf-name="' + esc(f.name) + '"' +
        ' data-mf-type="' + esc(f.type) + '"' +
        (item.required ? ' data-mf-required="1"' : "") +
        (f.purpose === "control" ? ' data-mf-control="1"' : "") +
        (f.secret ? ' data-mf-secret="1"' : "");
      var label = esc(labelFor(f.name));
      var reqMark = item.required ? ' <span class="mf-req" title="Required">*</span>' : "";
      var val = Object.prototype.hasOwnProperty.call(values, f.name) ? values[f.name] : null;

      html += '<div class="mf-field"' + attrs + ">";
      if (f.type === "boolean") {
        var checked = val != null ? !!val : f.default === true;
        html += '<label class="mf-check"><input type="checkbox" data-mf-input' +
          (checked ? " checked" : "") + "> " + label + reqMark + "</label>";
      } else if (f.secret) {
        var saved = opts.secretSaved ? !!opts.secretSaved(f.name) : (typeof val === "string" && val !== "");
        var badge = saved
          ? '<span class="creds-badge creds-saved">✓ Saved</span>'
          : '<span class="creds-badge creds-missing">⚠ Not saved</span>';
        var ph = saved ? "•••••••• (leave empty to keep)" : "enter value";
        // Never echo the stored value (it's the masked placeholder from
        // the API anyway) — empty input + badge, like the cloud-password
        // pattern.
        html += "<label>" + label + reqMark + " " + badge + "</label>" +
          '<input type="password" data-mf-input autocomplete="off" value="" placeholder="' + esc(ph) + '">';
      } else if (isNumeric(f.type)) {
        var step = f.type === "integer" ? "1" : "any";
        html += "<label>" + label + reqMark + "</label>" +
          '<input type="number" data-mf-input step="' + step + '"' +
          (f.min != null ? ' min="' + esc(String(f.min)) + '"' : "") +
          (f.max != null ? ' max="' + esc(String(f.max)) + '"' : "") +
          ' value="' + (val != null && isFinite(val) ? esc(String(val)) : "") + '"' +
          (f.default != null ? ' placeholder="' + esc(String(f.default)) + '"' : "") + ">";
      } else {
        html += "<label>" + label + reqMark + "</label>" +
          '<input type="text" data-mf-input value="' + (val != null ? esc(String(val)) : "") + '"' +
          (f.default != null ? ' placeholder="' + esc(String(f.default)) + '"' : "") + ">";
      }
      if (f.help) html += '<div class="mf-hint">' + esc(f.help) + "</div>";
      html += '<div class="mf-field-error" hidden></div>';
      html += "</div>";
    });
    html += "</div>";
    return html;
  }

  // wire(container, manifest, target, opts) → controller.
  //
  // Attaches typed write-through listeners: every input change lands in
  // `target` (the driver's config map) as number / boolean / string.
  // Empty non-required values DELETE the key so server-side option
  // defaults apply; empty secret inputs never clobber a stored value.
  //
  //   opts.telemetryOnly() → bool   live getter, consulted on validate
  //
  // Controller API:
  //   validate()            → [{name, message}] (also decorates fields)
  //   setTelemetryOnly(on)  → toggle control-field de-emphasis
  //   showServerErrors(msgs)→ route `field "x"` messages to fields;
  //                           returns the messages that matched nothing
  //   clearErrors()
  function wire(container, manifest, target, opts) {
    opts = opts || {};
    var fieldByName = {};
    fields(manifest).forEach(function (item) {
      fieldByName[item.field.name] = item;
    });

    function fieldEls() {
      return Array.prototype.slice.call(container.querySelectorAll(".mf-field[data-mf-name]"));
    }

    function setErr(el, msg) {
      var errEl = el.querySelector(".mf-field-error");
      if (errEl) {
        errEl.textContent = msg || "";
        errEl.hidden = !msg;
      }
      el.classList.toggle("mf-invalid", !!msg);
    }

    function writeThrough(el) {
      var name = el.getAttribute("data-mf-name");
      var item = fieldByName[name];
      if (!item) return;
      var f = item.field;
      var input = el.querySelector("[data-mf-input]");
      if (!input) return;
      if (f.type === "boolean") {
        target[name] = !!input.checked;
        return;
      }
      var raw = String(input.value == null ? "" : input.value).trim();
      if (f.secret) {
        // Empty secret input = keep whatever is stored server-side.
        if (raw !== "") target[name] = raw;
        return;
      }
      if (raw === "") {
        delete target[name]; // absent option → server applies the default
        return;
      }
      target[name] = isNumeric(f.type) ? Number(raw) : raw;
    }

    fieldEls().forEach(function (el) {
      var input = el.querySelector("[data-mf-input]");
      if (!input) return;
      var ev = input.type === "checkbox" ? "change" : "input";
      input.addEventListener(ev, function () {
        writeThrough(el);
        setErr(el, ""); // typing clears the stale error
      });
      input.addEventListener("blur", function () { writeThrough(el); });
    });

    function telemetryOnly() {
      if (typeof opts.telemetryOnly === "function") return !!opts.telemetryOnly();
      return !!opts.telemetryOnly;
    }

    function validate() {
      var errs = [];
      var tOnly = telemetryOnly();
      fieldEls().forEach(function (el) {
        var name = el.getAttribute("data-mf-name");
        var item = fieldByName[name];
        if (!item) return;
        var f = item.field;
        setErr(el, "");
        if (tOnly && f.purpose === "control") return; // optional in read-only mode
        var input = el.querySelector("[data-mf-input]");
        if (!input) return;
        var msg = "";
        if (f.type === "boolean") return; // checkbox is always valid
        var raw = String(input.value == null ? "" : input.value).trim();
        if (raw === "") {
          if (item.required && !(f.secret && opts.secretSaved && opts.secretSaved(name))) {
            msg = "Required";
          }
        } else if (isNumeric(f.type)) {
          var n = Number(raw);
          if (!isFinite(n)) {
            msg = "Must be a number";
          } else if (f.type === "integer" && n !== Math.trunc(n)) {
            msg = "Must be a whole number";
          } else if (f.min != null && n < f.min) {
            msg = "Must be ≥ " + f.min;
          } else if (f.max != null && n > f.max) {
            msg = "Must be ≤ " + f.max;
          }
        }
        if (msg) {
          setErr(el, msg);
          errs.push({ name: name, message: msg });
        }
      });
      return errs;
    }

    function setTelemetryOnly(on) {
      var wrap = container.querySelector(".mf-fields");
      if (wrap) wrap.classList.toggle("mf-telemetry-only", !!on);
    }

    // showServerErrors: match `field "<name>"` in each backend message
    // (the manifest validator's stable phrasing) against rendered
    // fields. Unmatched messages are returned for a caller banner.
    function showServerErrors(messages) {
      var unmatched = [];
      (messages || []).forEach(function (msg) {
        var m = /field "([^"]+)"/.exec(String(msg));
        var el = m && container.querySelector('.mf-field[data-mf-name="' + m[1].replace(/"/g, "") + '"]');
        if (el) {
          setErr(el, String(msg).replace(/^.*?field "[^"]+" ?/, "") || String(msg));
        } else {
          unmatched.push(msg);
        }
      });
      return unmatched;
    }

    function clearErrors() {
      fieldEls().forEach(function (el) { setErr(el, ""); });
    }

    return {
      validate: validate,
      setTelemetryOnly: setTelemetryOnly,
      showServerErrors: showServerErrors,
      clearErrors: clearErrors,
    };
  }

  window.FTWManifestForm = {
    esc: esc,
    emits: emits,
    liveTypes: liveTypes,
    verificationStatus: verificationStatus,
    verificationNotes: verificationNotes,
    displayName: displayName,
    fields: fields,
    labelFor: labelFor,
    renderFields: renderFields,
    wire: wire,
  };
})();
