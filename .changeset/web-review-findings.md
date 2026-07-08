---
"forty-two-watts": patch
---

Web review fixes: Devices-tab scope crash, catalog-failure fallback, wizard host sync.

- The Settings → Devices tab no longer crashes with a ReferenceError when the config contains a MyUplink (OAuth) driver — the async manifest-slot fill pass now has the `help` renderer in scope.
- A failed `/api/drivers/catalog` fetch no longer leaves the Devices tab stuck on "Loading catalog…": configured drivers fall back to the raw config editor, the battery-capacity reveal still runs, and the picker reports the failure.
- Setup wizard step 5 hides the duplicate manifest `host` field; the wizard's IP input is the single source of truth and is synced into `config.host` on save, so it can no longer diverge from the capability endpoint.
- Drivers added from the catalog or the wizard seed `capabilities.http.allowed_hosts` from the manifest's declared `http_hosts` cloud endpoints when present.
- The Loadpoints tab self-primes registry-pinned (`driver:` ref) manifests, so registry-installed EV chargers appear in the dropdown without visiting Devices first.
- Manifest-validation save errors are routed to fields from the structured `manifest_errors` array instead of splitting the joined error string, so help texts containing "; " are no longer mangled.
