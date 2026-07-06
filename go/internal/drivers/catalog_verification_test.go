package drivers

import (
	"testing"
)

// Verification status is what lets the UI distinguish "production-ready"
// drivers from "ported but unproven" ones. This test parses the real
// drivers/ dir and asserts the expected status labels for each driver
// we've manually annotated. Every other driver in the tree is expected
// to parse as "experimental" (the normalized default for missing /
// unknown values). Catalog IDs are file stems.
func TestCatalogVerificationStatus(t *testing.T) {
	entries, err := LoadCatalog("../../../drivers")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	byID := make(map[string]CatalogEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}

	cases := []struct {
		id     string
		status string
	}{
		{"ferroamp", "production"},
		{"sungrow", "production"},
		{"easee_cloud", "production"},
		{"ferroamp_modbus", "experimental"},
		{"zap", "beta"},
		{"deye", "experimental"},
		{"solis", "experimental"},
		{"solis_string", "experimental"},
		{"tibber", "experimental"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			e, ok := byID[tc.id]
			if !ok {
				t.Fatalf("driver %q missing from catalog (got %d entries)", tc.id, len(entries))
			}
			if e.Verification == nil {
				t.Fatalf("%s: no verification block after normalization", tc.id)
			}
			if e.Verification.Status != tc.status {
				t.Errorf("%s: verification.status=%q, want %q", tc.id, e.Verification.Status, tc.status)
			}
		})
	}
}

// Drivers at production status must also have a non-empty verified_by
// list — otherwise the label is hearsay. Beta is fuzzier; experimental
// needs nothing. This check runs against the real catalog so
// adding a new "production" annotation without also filling in
// verified_by fails loud at CI.
func TestCatalogProductionDriversHaveVerifier(t *testing.T) {
	entries, err := LoadCatalog("../../../drivers")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Verification == nil || e.Verification.Status != "production" {
			continue
		}
		if len(e.Verification.VerifiedBy) == 0 {
			t.Errorf("%s (%s): marked production but no verified_by entries — who tested it?",
				e.ID, e.Filename)
		}
		if e.Verification.VerifiedAt == "" {
			t.Errorf("%s (%s): marked production but no verified_at date", e.ID, e.Filename)
		}
	}
}

// Unknown / garbage values in the manifest must normalize to
// "experimental" rather than propagate an invalid label to the UI.
func TestNormalizeVerificationStatus(t *testing.T) {
	cases := map[string]string{
		"production":   "production",
		"PRODUCTION":   "production",
		"Beta":         "beta",
		"experimental": "experimental",
		"":             "experimental",
		"  ":           "experimental",
		"prod":         "experimental", // typo → safest default
		"alpha":        "experimental", // non-canonical → safest default
	}
	for in, want := range cases {
		if got := normalizeVerificationStatus(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCatalogSecretFields verifies the per-field `secret = true` markers
// surface end-to-end from a driver's manifest. Sonnen is the canonical
// user — its api_token has to land in SecretKeys so the Settings UI can
// render the password input and the API can mask it.
func TestCatalogSecretFields(t *testing.T) {
	entries, err := LoadCatalog("../../../drivers")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	byID := make(map[string]CatalogEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}

	sonnen, ok := byID["sonnen"]
	if !ok {
		t.Fatalf("sonnen driver missing from catalog (got %d entries)", len(entries))
	}
	if got, want := sonnen.SecretKeys(), []string{"api_token"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("sonnen SecretKeys = %v, want %v", got, want)
	}

	if got := byID["tibber"].SecretKeys(); len(got) != 1 || got[0] != "api_key" {
		t.Errorf("tibber SecretKeys = %v, want [api_key]", got)
	}
	if got := byID["easee_cloud"].SecretKeys(); len(got) != 1 || got[0] != "password" {
		t.Errorf("easee_cloud SecretKeys = %v, want [password]", got)
	}

	// Drivers without secret fields must come back empty — never a
	// phantom entry.
	if got := byID["pixii"].SecretKeys(); len(got) != 0 {
		t.Errorf("pixii unexpectedly has SecretKeys=%v", got)
	}
	if got := byID["ferroamp"].SecretKeys(); len(got) != 0 {
		t.Errorf("ferroamp unexpectedly has SecretKeys=%v", got)
	}
}
