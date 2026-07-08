// Package driverref parses pinned Sourceful driver references
// ("name@version"). It is a dependency-free leaf so both the config
// validator and the driverregistry client (which imports the drivers
// package for manifest validation) can share one parser without an
// import cycle.
package driverref

import (
	"fmt"
	"strings"
)

// Parse splits a pinned driver reference "name@version" into its
// parts. Surrounding whitespace (a hand-edited config's stray space)
// is trimmed from the ref and from each side of the "@". The "@" is
// mandatory and both sides must be non-empty — we want explicit
// pinning, never an implicit "latest". Path-hostile characters are
// rejected because name/version become a cache file name.
func Parse(ref string) (name, version string, err error) {
	name, version, ok := strings.Cut(strings.TrimSpace(ref), "@")
	name, version = strings.TrimSpace(name), strings.TrimSpace(version)
	if !ok || name == "" || version == "" {
		return "", "", fmt.Errorf("driver ref %q must be 'name@version' (e.g. 'deye@3.1.1')", ref)
	}
	for _, part := range []string{name, version} {
		if strings.ContainsAny(part, `/\`) || strings.Contains(part, "..") {
			return "", "", fmt.Errorf("driver ref %q contains path characters", ref)
		}
	}
	return name, version, nil
}
