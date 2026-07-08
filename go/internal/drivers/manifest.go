// DRIVER_MANIFEST reader + config validator.
//
// Every driver exposes a static global Lua table `DRIVER_MANIFEST` at
// file scope (blixt driver-standard contract). The host reads it BEFORE
// any driver function runs and uses it to:
//
//  1. validate the operator's per-driver `config:` map (type + inclusive
//     bounds, ALL errors reported at once) before driver_init,
//  2. apply option defaults,
//  3. describe the driver to the catalog / UI (display name, protocols,
//     connection defaults, verification provenance).
//
// Parsing happens in a sandboxed throwaway Lua VM: no `host` global, a
// minimal stdlib (base/table/string/math), an execution deadline, and
// panic recovery. A MALFORMED manifest is a load error — a typo'd
// manifest is more dangerous than no manifest. A MISSING manifest
// (ErrNoManifest) is tolerated by the registry with a loud warning so
// hand-written user drivers from before the manifest contract keep
// running across an upgrade (blixt's own legacy rule); such drivers get
// no config validation and don't appear in the catalog.
package drivers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// ErrNoManifest marks a driver whose top-level defines no
// DRIVER_MANIFEST table at all. Distinguished from a malformed manifest
// (any other error) so the registry can warn-and-load legacy drivers
// while still refusing typo'd ones. Test with errors.Is.
var ErrNoManifest = errors.New("missing DRIVER_MANIFEST table")

// ManifestField is one entry in a manifest's `requires` / `options`
// lists. Blixt-exact schema plus the ftw `secret` extension.
type ManifestField struct {
	Name    string   `json:"name"`
	Purpose string   `json:"purpose"` // "always" | "control"
	Type    string   `json:"type"`    // "integer" | "double" | "boolean" | "string"
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Default any      `json:"default,omitempty"`
	Help    string   `json:"help,omitempty"`
	// Secret marks the field as sensitive (API tokens, passwords). The
	// UI renders a password input and the config mask/restore cycle
	// treats it like the structured password fields. Replaces the old
	// DRIVER.config_secrets list.
	Secret bool `json:"secret,omitempty"`
}

// ManifestProvides declares the driver's emit contract: `live` topics
// promised from driver_poll, `static` host.set_* fields promised after
// driver_init. Documentation the host can check, not behavior.
type ManifestProvides struct {
	Live   []string `json:"live,omitempty"`
	Static []string `json:"static,omitempty"`
}

// ManifestVerification records who has actually run this driver against
// real hardware. Surfaced as a badge in the catalog picker.
type ManifestVerification struct {
	Status     string   `json:"status"` // experimental | beta | production
	VerifiedBy []string `json:"verified_by,omitempty"`
	VerifiedAt string   `json:"verified_at,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

// Manifest is the parsed DRIVER_MANIFEST table. Blixt-core fields first,
// ftw catalog extensions after (all optional — a blixt manifest without
// them still loads).
type Manifest struct {
	Name           string           `json:"name"`
	Version        string           `json:"version"`
	Role           string           `json:"role"`                       // battery|meter|pv|ev|heat-pump|hybrid
	PollIntervalMS int              `json:"poll_interval_ms,omitempty"` // 0 = host default
	Requires       []ManifestField  `json:"requires"`
	Options        []ManifestField  `json:"options"`
	Provides       ManifestProvides `json:"provides"`

	// ftw extensions
	DisplayName        string                `json:"display_name,omitempty"`
	Manufacturer       string                `json:"manufacturer,omitempty"`
	Protocols          []string              `json:"protocols,omitempty"` // mqtt|modbus|http|websocket|tcp
	ConnectionDefaults map[string]any        `json:"connection_defaults,omitempty"`
	Verification       *ManifestVerification `json:"verification,omitempty"`
	TestedModels       []string              `json:"tested_models,omitempty"`
	// HTTPHosts lists the fixed outbound HTTP hosts a cloud driver
	// talks to (e.g. {"api.myuplink.com"}). The UI seeds
	// capabilities.http.allowed_hosts from it so the operator doesn't
	// have to know the vendor's API hostname.
	HTTPHosts []string `json:"http_hosts,omitempty"`
}

// SecretKeys returns the names of all requires/options fields marked
// secret=true. Used by the API's config mask/restore cycle.
func (m Manifest) SecretKeys() []string {
	var out []string
	for _, f := range m.Requires {
		if f.Secret {
			out = append(out, f.Name)
		}
	}
	for _, f := range m.Options {
		if f.Secret {
			out = append(out, f.Name)
		}
	}
	return out
}

// LoadManifest reads the .lua file at path and parses its manifest.
func LoadManifest(path string) (*Manifest, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	m, err := ParseManifest(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return m, nil
}

// ParseManifest executes src in a sandboxed Lua VM and parses the
// DRIVER_MANIFEST global. The VM has no `host` global — a driver's
// top-level code must be the manifest, local state, and function
// definitions only. A missing DRIVER_MANIFEST table is an error.
func ParseManifest(src string) (m *Manifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, err = nil, fmt.Errorf("manifest parse panic: %v", r)
		}
	}()

	L := lua.NewState(lua.Options{
		SkipOpenLibs:  true,
		CallStackSize: 120,
		RegistrySize:  1024 * 20,
	})
	defer L.Close()

	// Minimal stdlib: enough for literal tables + pure top-level
	// computation. No io, no os, no package loader.
	for _, lib := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		if cErr := L.CallByParam(lua.P{Fn: L.NewFunction(lib.fn), NRet: 0, Protect: true},
			lua.LString(lib.name)); cErr != nil {
			return nil, fmt.Errorf("open lib %s: %w", lib.name, cErr)
		}
	}
	// OpenBase registers file loaders; a manifest has no business with them.
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("loadfile", lua.LNil)

	// Deadline so a buggy top-level loop can't hang the host.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	L.SetContext(ctx)

	// A top-level exec error is NOT fatal by itself: legacy drivers may
	// run top-level code the sandbox can't (os.time(), host.*, …). What
	// matters is whether DRIVER_MANIFEST got defined before the failure
	// — if it did, honour it (schema errors stay fatal); if not, this is
	// a manifest-less legacy driver (ErrNoManifest carries the exec
	// error so the registry's warn-and-load log shows why).
	execErr := L.DoString(src)

	tblVal := L.GetGlobal("DRIVER_MANIFEST")
	tbl, ok := tblVal.(*lua.LTable)
	if !ok {
		if execErr != nil {
			return nil, fmt.Errorf("%w (driver top-level failed in manifest sandbox: %v)", ErrNoManifest, execErr)
		}
		return nil, fmt.Errorf("%w (found %s)", ErrNoManifest, tblVal.Type())
	}
	return parseManifestTable(tbl)
}

func parseManifestTable(tbl *lua.LTable) (*Manifest, error) {
	m := &Manifest{}
	var err error
	if m.Name, err = requiredString(tbl, "name"); err != nil {
		return nil, err
	}
	if m.Version, err = requiredString(tbl, "version"); err != nil {
		return nil, err
	}
	if m.Role, err = requiredString(tbl, "role"); err != nil {
		return nil, err
	}

	switch v := tbl.RawGetString("poll_interval_ms").(type) {
	case *lua.LNilType:
	case lua.LNumber:
		if v > 0 {
			m.PollIntervalMS = int(v)
		}
	default:
		return nil, fmt.Errorf("manifest.poll_interval_ms: expected number, got %s", v.Type())
	}

	if m.Requires, err = parseFieldList(tbl, "requires"); err != nil {
		return nil, err
	}
	if m.Options, err = parseFieldList(tbl, "options"); err != nil {
		return nil, err
	}
	if err = parseProvides(tbl, &m.Provides); err != nil {
		return nil, err
	}

	// ftw extensions — all optional.
	m.DisplayName = optionalString(tbl, "display_name")
	m.Manufacturer = optionalString(tbl, "manufacturer")
	m.Protocols = optionalStringList(tbl, "protocols")
	m.TestedModels = optionalStringList(tbl, "tested_models")
	m.HTTPHosts = optionalStringList(tbl, "http_hosts")
	if cd, ok := tbl.RawGetString("connection_defaults").(*lua.LTable); ok {
		if obj, ok := luaToGo(cd).(map[string]any); ok {
			m.ConnectionDefaults = obj
		}
	}
	if v, ok := tbl.RawGetString("verification").(*lua.LTable); ok {
		m.Verification = &ManifestVerification{
			Status:     optionalString(v, "status"),
			VerifiedBy: optionalStringList(v, "verified_by"),
			VerifiedAt: optionalString(v, "verified_at"),
			Notes:      optionalString(v, "notes"),
		}
	}
	return m, nil
}

func requiredString(tbl *lua.LTable, key string) (string, error) {
	s, ok := tbl.RawGetString(key).(lua.LString)
	if !ok || string(s) == "" {
		return "", fmt.Errorf("manifest.%s: required non-empty string", key)
	}
	return string(s), nil
}

func optionalString(tbl *lua.LTable, key string) string {
	if s, ok := tbl.RawGetString(key).(lua.LString); ok {
		return string(s)
	}
	return ""
}

func optionalStringList(tbl *lua.LTable, key string) []string {
	list, ok := tbl.RawGetString(key).(*lua.LTable)
	if !ok {
		return nil
	}
	var out []string
	for i := 1; i <= list.Len(); i++ {
		if s, ok := list.RawGetInt(i).(lua.LString); ok {
			out = append(out, string(s))
		}
	}
	return out
}

func parseFieldList(tbl *lua.LTable, key string) ([]ManifestField, error) {
	v := tbl.RawGetString(key)
	if v == lua.LNil {
		return nil, nil
	}
	list, ok := v.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("manifest.%s: expected list, got %s", key, v.Type())
	}
	var out []ManifestField
	for i := 1; i <= list.Len(); i++ {
		entry, ok := list.RawGetInt(i).(*lua.LTable)
		if !ok {
			return nil, fmt.Errorf("manifest.%s[%d]: expected table", key, i)
		}
		f, err := parseField(entry)
		if err != nil {
			return nil, fmt.Errorf("manifest.%s[%d]: %w", key, i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

func parseField(tbl *lua.LTable) (ManifestField, error) {
	var f ManifestField
	var err error
	if f.Name, err = requiredString(tbl, "name"); err != nil {
		return f, err
	}
	if f.Purpose, err = requiredString(tbl, "purpose"); err != nil {
		return f, fmt.Errorf("field %q: %w", f.Name, err)
	}
	if f.Purpose != "always" && f.Purpose != "control" {
		return f, fmt.Errorf("field %q: unknown purpose %q (expected \"always\" or \"control\")", f.Name, f.Purpose)
	}
	if f.Type, err = requiredString(tbl, "type"); err != nil {
		return f, fmt.Errorf("field %q: %w", f.Name, err)
	}
	switch f.Type {
	case "integer", "double", "boolean", "string":
	default:
		return f, fmt.Errorf("field %q: unknown type %q (expected \"integer\", \"double\", \"boolean\", or \"string\")", f.Name, f.Type)
	}

	if f.Min, err = optionalNumber(tbl, "min"); err != nil {
		return f, fmt.Errorf("field %q: %w", f.Name, err)
	}
	if f.Max, err = optionalNumber(tbl, "max"); err != nil {
		return f, fmt.Errorf("field %q: %w", f.Name, err)
	}
	if (f.Type == "boolean" || f.Type == "string") && (f.Min != nil || f.Max != nil) {
		return f, fmt.Errorf("field %q: min/max are only meaningful for integer/double types", f.Name)
	}
	if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
		return f, fmt.Errorf("field %q: min (%v) > max (%v)", f.Name, *f.Min, *f.Max)
	}

	if f.Default, err = parseDefault(tbl, f.Name, f.Type); err != nil {
		return f, err
	}
	f.Help = optionalString(tbl, "help")
	if b, ok := tbl.RawGetString("secret").(lua.LBool); ok {
		f.Secret = bool(b)
	}
	return f, nil
}

func optionalNumber(tbl *lua.LTable, key string) (*float64, error) {
	switch v := tbl.RawGetString(key).(type) {
	case *lua.LNilType:
		return nil, nil
	case lua.LNumber:
		n := float64(v)
		return &n, nil
	default:
		return nil, fmt.Errorf("`%s`: expected number, got %s", key, v.Type())
	}
}

// parseDefault type-checks the field's literal default against its
// declared type. `default = 1.5` on an integer field is a manifest bug
// caught at load, not at 3 AM when a device restarts.
func parseDefault(tbl *lua.LTable, fieldName, fieldType string) (any, error) {
	v := tbl.RawGetString("default")
	if v == lua.LNil {
		return nil, nil
	}
	mismatch := func() (any, error) {
		return nil, fmt.Errorf("field %q: default does not match type %q (got %s)", fieldName, fieldType, v.Type())
	}
	switch fieldType {
	case "integer":
		n, ok := v.(lua.LNumber)
		if !ok || float64(n) != math.Trunc(float64(n)) {
			return mismatch()
		}
		return int64(n), nil
	case "double":
		n, ok := v.(lua.LNumber)
		if !ok {
			return mismatch()
		}
		return float64(n), nil
	case "boolean":
		b, ok := v.(lua.LBool)
		if !ok {
			return mismatch()
		}
		return bool(b), nil
	case "string":
		s, ok := v.(lua.LString)
		if !ok {
			return mismatch()
		}
		return string(s), nil
	}
	return mismatch()
}

func parseProvides(tbl *lua.LTable, out *ManifestProvides) error {
	v := tbl.RawGetString("provides")
	if v == lua.LNil {
		return nil
	}
	p, ok := v.(*lua.LTable)
	if !ok {
		return fmt.Errorf("manifest.provides: expected table, got %s", v.Type())
	}
	out.Live = optionalStringList(p, "live")
	out.Static = optionalStringList(p, "static")
	return nil
}

// ValidateConfig checks an operator config map against the manifest.
// Returns one error string per failure — required field missing, wrong
// type, out-of-bounds numeric — never fail-fast: when an operator types
// five wrong fields, surfacing all five at once saves five round trips.
//
// telemetryOnly skips purpose="control" fields entirely, so a freshly
// wired device can run read-only without install-time control fields.
func (m *Manifest) ValidateConfig(cfg map[string]any, telemetryOnly bool) []string {
	var errs []string
	for _, f := range m.Requires {
		if telemetryOnly && f.Purpose == "control" {
			continue
		}
		v, ok := cfg[f.Name]
		if !ok {
			msg := fmt.Sprintf("required field %q is missing", f.Name)
			if f.Help != "" {
				msg += " — " + f.Help
			}
			errs = append(errs, msg)
			continue
		}
		checkFieldValue(f, v, &errs)
	}
	for _, f := range m.Options {
		if telemetryOnly && f.Purpose == "control" {
			continue
		}
		if v, ok := cfg[f.Name]; ok {
			checkFieldValue(f, v, &errs)
		}
	}
	return errs
}

// checkFieldValue type-checks + bounds-checks one config value against
// one manifest field, appending human-readable errors.
func checkFieldValue(f ManifestField, v any, errs *[]string) {
	var n float64
	switch f.Type {
	case "boolean":
		if _, ok := v.(bool); !ok {
			*errs = append(*errs, fmt.Sprintf("field %q must be boolean (got %v)", f.Name, v))
		}
		return
	case "string":
		if _, ok := v.(string); ok {
			return
		}
		// Leniency: YAML leaves unquoted numeric ids (param_power_id:
		// 10013) as numbers, and the driver reads them through Lua where
		// number→string coercion is native. Rejecting would force every
		// operator to quote ids that have always worked unquoted.
		if _, ok := asFloat(v); ok {
			return
		}
		*errs = append(*errs, fmt.Sprintf("field %q must be a string (got %v)", f.Name, v))
		return
	case "integer":
		fv, ok := asFloat(v)
		if !ok || fv != math.Trunc(fv) {
			*errs = append(*errs, fmt.Sprintf("field %q must be an integer (got %v)", f.Name, v))
			return
		}
		n = fv
	case "double":
		fv, ok := asFloat(v)
		if !ok {
			*errs = append(*errs, fmt.Sprintf("field %q must be a number (got %v)", f.Name, v))
			return
		}
		n = fv
	}
	if f.Min != nil && n < *f.Min {
		*errs = append(*errs, boundErr(f, n, "below min", *f.Min))
	}
	if f.Max != nil && n > *f.Max {
		*errs = append(*errs, boundErr(f, n, "above max", *f.Max))
	}
}

func boundErr(f ManifestField, n float64, dir string, bound float64) string {
	help := f.Help
	if help == "" {
		help = "no help text"
	}
	return fmt.Sprintf("field %q = %v %s %v (%s)", f.Name, trimFloat(n), dir, trimFloat(bound), help)
}

func trimFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

// asFloat coerces YAML/JSON-decoded numerics. YAML gives int / float64,
// JSON gives float64; both may appear depending on the config path.
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

// ApplyDefaults returns a copy of cfg with every absent option that
// declares a default filled in. Non-mutating; safe to call with nil.
func (m *Manifest) ApplyDefaults(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg)+len(m.Options))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range m.Options {
		if f.Default == nil {
			continue
		}
		if _, ok := out[f.Name]; !ok {
			out[f.Name] = f.Default
		}
	}
	return out
}
