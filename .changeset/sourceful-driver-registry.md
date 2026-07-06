---
"forty-two-watts": minor
---

Drivers can now be fetched from the Sourceful driver registry via pinned
`driver: name@version` refs (mutually exclusive with `lua:`), cached
locally under `<state dir>/driver-cache` for deterministic, offline-safe
loads. New top-level `driver_registry` config section (`net`
devnet/testnet/mainnet, explicit `url` override, `cache_dir`;
`DRIVER_REGISTRY_URL` env beats both) and new `/api/registry` endpoints
(`GET /api/registry/drivers`, `GET /api/registry/drivers/{name}/versions`,
`POST /api/registry/refresh`) with a 5-minute TTL cache for the settings
UI.
