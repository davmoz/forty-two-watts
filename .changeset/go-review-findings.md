---
"forty-two-watts": patch
---

Driver-standard review fixes across the Go core.

Safety-critical: control arming is now lazy — a driver on an idle-mode
site never receives the `init` control verb (no Remote-Mode enable, no
device-watchdog arm) until the first real command dispatch, and
`deinit` on clean stop fires only for armed drivers. Warmup holds
(`host.set_warmup_s`) start at arm time and keep suppressing commands
while polls run.

Robustness + operator visibility: legacy drivers whose top-level code
fails in the manifest sandbox (e.g. `os.time()`) load again via the
warn-and-load path; registry fetches are validated (manifest-mandatory,
2 MB truncation detected) before anything lands in the offline cache,
with unique temp files for concurrent resolves; every driver Add
refusal now surfaces in driver health, and `/api/status` carries the
persistent `config_warning` alongside `last_error`.

Secrets: `driver:` registry-ref entries get their manifest-declared
secrets masked in `GET /api/config` (resolved cache-only, never the
network), and a name heuristic (`password|token|secret|api_key`) masks
and restores sensitive keys even when no manifest is resolvable
(legacy drivers, rotated `refresh_token`).

Smaller fixes: manifest parses are cached per (path, mtime, size);
manifest `string` fields accept unquoted YAML numeric ids; the new
`http_hosts` manifest field is parsed for UI consumption; the driver
registry defaults to mainnet; driver refs tolerate stray whitespace;
re-enabling a disabled driver now faces the config manifest gate; and
the watchdog no longer dispatches default-mode to drivers that never
started.
