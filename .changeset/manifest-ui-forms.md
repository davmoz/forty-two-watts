---
"forty-two-watts": minor
---

Driver settings forms are now generated from each driver's DRIVER_MANIFEST: typed fields (integer/double/boolean/string) with min/max bounds, help text under every field, and password inputs for secret fields — in both the Settings → Devices tab and the setup wizard. Drivers can be installed straight from the Sourceful registry in the UI, pinned to an exact version (`driver: "name@version"`), with a graceful offline fallback to bundled drivers. A new "Read-only (telemetry only)" checkbox runs a driver without control commands and relaxes control-purpose settings. POST /api/config now validates driver configs against their manifests synchronously, and the UI surfaces those errors on the exact offending field.
