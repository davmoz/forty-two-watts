# Sourceful Driver Registry

forty-two-watts can fetch Lua drivers from the Sourceful Novacore
driver registry instead of (or alongside) the bundled `drivers/*.lua`
set. A registry driver is referenced by a **pinned** `name@version`
ref, fetched once over HTTPS, cached locally, and loaded through the
exact same driver host as a local file.

Implementation: `go/internal/driverregistry` (client + cache),
`go/internal/drivers/registry.go` (`Registry.ResolveDriverRef` hook),
`go/internal/api/api_registry.go` (UI proxy endpoints).

## Ref format — pinning is mandatory

```
name@version        e.g.  deye@3.1.1
```

The `@version` is **required**; there is no `latest` at the runtime
layer, ever. Registry versions are immutable once published, so a
pinned ref plus a warm cache is fully deterministic: the same config
loads byte-identical driver code on every boot, with or without WAN.
Updating a driver is an explicit act — change the pinned ref in
config, the registry hot-reload restarts the driver on the new
version.

Refs with a missing `@`, an empty name, or an empty version are
rejected at config validation time.

## Using a registry driver

```yaml
drivers:
  - name: deye
    driver: deye@3.1.1        # registry ref
    is_site_meter: true
    capabilities:
      modbus:
        host: 192.168.1.42

driver_registry:              # optional — mainnet defaults when absent
  net: mainnet                # devnet | testnet | mainnet
  url: ""                     # explicit base URL, beats net
  cache_dir: ""               # default <state dir>/driver-cache
```

`driver:` and `lua:` are **mutually exclusive** — exactly one of the
two must be set per driver. Everything else about the entry (name,
capabilities, `config:` map, battery capacity, …) is identical to a
local-file driver. `driver:` refs are not filesystem paths: the
driver-path resolution that rewrites `lua:` entries never touches
them.

## Registry selection

Base URL precedence, highest first:

1. `DRIVER_REGISTRY_URL` environment variable (dev / self-hosted).
2. `driver_registry.url` (explicit override).
3. `driver_registry.net` → `https://novacore-{net}.sourceful.dev/device-support/drivers`
   for `devnet` | `testnet` | `mainnet`. Default net: `mainnet`.

The registry read path is public — no auth token is needed to list or
fetch drivers.

## Cache + offline behavior

Fetched sources are cached as `{cache_dir}/{name}-{version}.lua`
(default `<state dir>/driver-cache`, i.e. next to `state.db`). The
resolve path is cache-first:

- **Cache hit** → zero network. An offline Pi with a warm cache boots
  and runs registry drivers indefinitely.
- **Cache miss** → `GET {base}/{name}/{version}` (raw Lua body, 10 s
  timeout), validated BEFORE caching — registry drivers are
  manifest-mandatory, so the body must parse as a driver with a valid
  `DRIVER_MANIFEST`. Empty, oversized (> 2 MB), or garbage bodies (an
  HTML error page served with a 200) are refused and nothing is
  cached; the next resolve retries. Valid bodies are written
  atomically (unique temp file + rename) so a crashed fetch never
  leaves a truncated driver behind.
- **Cache miss + fetch failure** → the driver is *refused* with a
  clear error in the log and in driver health; every other driver
  starts normally. Fix the WAN (or the ref) and restart / touch
  config.yaml to retry.

Bundled drivers still ship in `drivers/` — a fresh Pi with no WAN
boots exactly as before; the registry is purely additive.

The cache is content-addressed by name+version and versions are
immutable, so it never needs invalidation. Deleting files from the
cache dir is always safe (they re-fetch on next resolve).

## Registry HTTP surface (upstream)

- `GET {base}` — JSON index `{count, drivers:[{id, name,
  display_name, tier, is_active, author, latest_version, …}]}`.
- `GET {base}/{name}/versions` — published versions of one driver.
- `GET {base}/{name}/{version}` — raw Lua source (text/plain).

## API endpoints (this service)

Proxied through the local API for the settings UI, with a 5-minute
in-process TTL cache (the registry changes only when someone publishes
a driver):

| Endpoint | Purpose |
|---|---|
| `GET /api/registry/drivers` | Registry index for the add-driver picker. |
| `GET /api/registry/drivers/{name}/versions` | Version list for pinning. |
| `POST /api/registry/refresh` | Flush the TTL cache ("I just published X"). |

`GET /api/registry/drivers/{name}/{version}/manifest` (fetch source →
`drivers.ParseManifest` → manifest JSON for manifest-driven forms) is
wired once the driver-manifest core (`drivers.ParseManifest`) lands —
see `go/internal/api/api_registry.go` for the integration note.

All endpoints return `503` when the registry client is not configured
and `502` (uncached, retried on next request) when the upstream fetch
fails.

## Troubleshooting

- **"resolve registry ref … : status 404"** — the name@version doesn't
  exist on the selected net. Check `GET /api/registry/drivers/{name}/versions`
  and remember each net (devnet/testnet/mainnet) has its own catalog.
- **Driver refused offline** — the ref was never fetched on this host
  (cold cache). Bring WAN up once, or pre-seed
  `{cache_dir}/{name}-{version}.lua` by hand.
- **Switched nets but still seeing old list in the UI** —
  `POST /api/registry/refresh`, and note that `driver_registry.*`
  changes need a service restart (client is built at startup).
