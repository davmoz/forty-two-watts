package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Mechanical enforcement of the driver-manifest audit invariants. These
// tests parse the real drivers/ directory, so adding a driver with a
// half-filled manifest fails CI instead of surfacing as a broken form
// in the Settings UI six weeks later.

const auditDriversDir = "../../../drivers"

var validRoles = map[string]bool{
	"battery": true, "meter": true, "pv": true,
	"ev": true, "heat-pump": true, "hybrid": true,
}

// provides.live entries are "type.key" (or "type.key[]" for arrays).
var validProvidesPrefix = map[string]bool{
	"battery": true, "meter": true, "pv": true,
	"ev": true, "vehicle": true, "inverter": true,
	"v2x_charger": true,
}

func auditCatalog(t *testing.T) []CatalogEntry {
	t.Helper()
	entries, err := LoadCatalog(auditDriversDir)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	return entries
}

// Every .lua file in drivers/ must parse into a catalog entry.
// LoadCatalog skips files whose manifest fails to parse (one broken
// driver must not blank the UI catalog), so without this test a
// malformed manifest would silently drop the driver from the catalog.
func TestAuditEveryDriverFileParses(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(auditDriversDir, "*.lua"))
	if err != nil {
		t.Fatal(err)
	}
	entries := auditCatalog(t)
	byFile := make(map[string]bool, len(entries))
	for _, e := range entries {
		byFile[e.Filename] = true
	}
	for _, f := range files {
		if !byFile[filepath.Base(f)] {
			t.Errorf("%s: DRIVER_MANIFEST failed to parse (missing from catalog)", filepath.Base(f))
		}
	}
}

// Core manifest hygiene: identity fields present, role valid, every
// declared config field carries operator-grade help, secrets are
// strings, no duplicate field names, defaults sit inside their own
// bounds, provides entries use the namespaced "type.key" shape, and
// poll_interval_ms (when set) is a sane cadence.
func TestAuditManifestBasics(t *testing.T) {
	for _, e := range auditCatalog(t) {
		e := e
		t.Run(e.ID, func(t *testing.T) {
			if e.DisplayName == "" {
				t.Error("display_name missing")
			}
			if e.Manufacturer == "" {
				t.Error("manufacturer missing")
			}
			if !validRoles[e.Role] {
				t.Errorf("role %q not in battery|meter|pv|ev|heat-pump|hybrid", e.Role)
			}
			if e.PollIntervalMS != 0 && (e.PollIntervalMS < 250 || e.PollIntervalMS > 3_600_000) {
				t.Errorf("poll_interval_ms %d outside 250..3600000", e.PollIntervalMS)
			}

			seen := map[string]bool{}
			for _, f := range append(append([]ManifestField{}, e.Requires...), e.Options...) {
				if f.Help == "" {
					t.Errorf("field %q has no help text — the UI form and validation errors surface it", f.Name)
				}
				if f.Secret && f.Type != "string" {
					t.Errorf("field %q: secret=true only makes sense on strings (got %s)", f.Name, f.Type)
				}
				if seen[f.Name] {
					t.Errorf("field %q declared twice", f.Name)
				}
				seen[f.Name] = true
				if f.Default != nil && (f.Min != nil || f.Max != nil) {
					if dv, ok := asFloat(f.Default); ok {
						if f.Min != nil && dv < *f.Min {
							t.Errorf("field %q: default %v below min %v", f.Name, dv, *f.Min)
						}
						if f.Max != nil && dv > *f.Max {
							t.Errorf("field %q: default %v above max %v", f.Name, dv, *f.Max)
						}
					}
				}
			}

			for _, p := range append(append([]string{}, e.Provides.Live...), e.Provides.Static...) {
				if strings.Contains(p, ".") {
					prefix := p[:strings.Index(p, ".")]
					if !validProvidesPrefix[prefix] {
						t.Errorf("provides entry %q has unknown event prefix %q", p, prefix)
					}
				}
			}
		})
	}
}

// Verified drivers (beta / production) must declare what they emit —
// the provides contract is what the registry UI and the first-poll
// checks build on. Experimental drivers get a pass.
func TestAuditVerifiedDriversDeclareProvides(t *testing.T) {
	for _, e := range auditCatalog(t) {
		if e.Verification == nil {
			continue
		}
		if reason, ok := metricsOnlyDrivers[e.ID]; ok {
			t.Logf("%s: provides.live empty by exception: %s", e.ID, reason)
			continue
		}
		switch e.Verification.Status {
		case "beta", "production":
			if len(e.Provides.Live) == 0 {
				t.Errorf("%s: verification=%s but provides.live is empty", e.ID, e.Verification.Status)
			}
		}
	}
}

// ---- config-key ↔ manifest cross-check --------------------------------

// stripManifestBlock removes the DRIVER_MANIFEST = { … } table (brace
// counting) so field names declared in the manifest don't satisfy the
// "body reads this key" check by accident.
func stripManifestBlock(src string) string {
	idx := strings.Index(src, "DRIVER_MANIFEST")
	if idx < 0 {
		return src
	}
	depth := 0
	started := false
	for i := idx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[:idx] + src[i+1:]
			}
		}
	}
	return src
}

// stripLuaComments removes `--` line comments. Limitation (documented,
// accepted): a literal "--" inside a string constant would truncate
// that line, and multi-line --[[ ]] comments are handled only because
// their body lines also start with content after "--". Bundled drivers
// use neither pattern in a way that affects the config.<key> scan; if
// one ever does, tighten this helper rather than weakening the test.
func stripLuaComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if p := strings.Index(l, "--"); p >= 0 {
			lines[i] = l[:p]
		}
	}
	return strings.Join(lines, "\n")
}

var (
	configDotRe   = regexp.MustCompile(`\bconfig\.([A-Za-z_][A-Za-z0-9_]*)`)
	configIndexRe = regexp.MustCompile(`\bconfig\[\s*"([^"]+)"\s*\]`)
)

// configKeysReadBy scans a driver body (manifest stripped, comments
// stripped) for literal `config.<key>` / `config["<key>"]` accesses.
// Limitation: keys read through a helper (`local v = config[key]` with
// key passed as a variable) are invisible to this scan — the reverse
// check below covers those via a per-driver exception list.
func configKeysReadBy(src string) map[string]bool {
	body := stripLuaComments(stripManifestBlock(src))
	keys := map[string]bool{}
	for _, m := range configDotRe.FindAllStringSubmatch(body, -1) {
		keys[m[1]] = true
	}
	for _, m := range configIndexRe.FindAllStringSubmatch(body, -1) {
		keys[m[1]] = true
	}
	return keys
}

// Keys a driver reads that are intentionally NOT declarable in the
// manifest, with the reason. Keep this list painful to grow.
var undeclaredKeyExceptions = map[string]map[string]string{
	"ferroamp": {
		"eso_capacity_kwh": "map of ESO id → kWh; the manifest field schema has no table type yet",
	},
	"myuplink": {
		"refresh_token":  "written by the OAuth connect flow (and rotated via host.persist_secret) — never operator-entered, so no form field",
		"base_url":       "test-only override; production is pinned to api.myuplink.com",
		"setup_retry_ms": "test-only override for the retry backoff",
	},
	"nibe_local": {
		"base_url":        "test-only override; production builds https://host:port",
		"setup_retry_ms":  "test-only override for the retry backoff",
		"full_refresh_ms": "test-only override for the full register sweep cadence",
	},
}

// metricsOnlyDrivers emit host.emit_metric diagnostics exclusively (no
// structured DER telemetry), so an empty provides.live is truthful even
// at beta/production verification.
var metricsOnlyDrivers = map[string]string{
	"nibe_local": "read-only heat-pump register map via emit_metric",
	"myuplink":   "read-only heat-pump headline metrics via emit_metric",
}

// Every literal config key a driver body reads must be declared in its
// manifest (requires or options) — otherwise the Settings form can
// never offer it and validation never sees it. Host-injected keys
// (leading underscore, e.g. _troubleshooting_mode) are exempt.
func TestAuditEveryConfigReadIsDeclared(t *testing.T) {
	for _, e := range auditCatalog(t) {
		e := e
		t.Run(e.ID, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(auditDriversDir, e.Filename))
			if err != nil {
				t.Fatal(err)
			}
			declared := map[string]bool{}
			for _, f := range append(append([]ManifestField{}, e.Requires...), e.Options...) {
				declared[f.Name] = true
			}
			for key := range configKeysReadBy(string(src)) {
				if strings.HasPrefix(key, "_") {
					continue // host-injected (e.g. _troubleshooting_mode)
				}
				if declared[key] {
					continue
				}
				if reason, ok := undeclaredKeyExceptions[e.ID][key]; ok {
					t.Logf("%s: config.%s undeclared by exception: %s", e.ID, key, reason)
					continue
				}
				t.Errorf("driver reads config.%s but the manifest doesn't declare it", key)
			}
		})
	}
}

// Reverse direction: every manifest-declared field must actually be
// read by the driver body — a declared-but-dead key is a lie the UI
// renders as a working setting. Fields read through helpers with the
// key as a string literal (pixii's config_bool) are found by the
// quoted-name fallback.
func TestAuditEveryDeclaredKeyIsRead(t *testing.T) {
	for _, e := range auditCatalog(t) {
		e := e
		if e.ID == "skeleton" {
			continue // template: placeholder fields, placeholder body
		}
		t.Run(e.ID, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(auditDriversDir, e.Filename))
			if err != nil {
				t.Fatal(err)
			}
			body := stripLuaComments(stripManifestBlock(string(raw)))
			read := configKeysReadBy(string(raw))
			for _, f := range append(append([]ManifestField{}, e.Requires...), e.Options...) {
				if read[f.Name] {
					continue
				}
				// Helper-mediated access: config_bool(config, "debug") etc.
				if strings.Contains(body, fmt.Sprintf("%q", f.Name)) {
					continue
				}
				t.Errorf("manifest declares %q but the driver body never reads it", f.Name)
			}
		})
	}
}
