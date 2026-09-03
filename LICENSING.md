# Licensing

Narvi uses one source-available license and two proprietary tiers:

- **Narvi (this repository)** — [Elastic License 2.0](LICENSE). Free to read,
  use, modify and self-host. You may not provide it to third parties as a
  managed service, circumvent its license-key functionality, or remove
  licensing notices.
- **Narvi Gatekeeper** (organization-scale review governance; planned) —
  proprietary, compiled into a separate enterprise build, activated by a
  license key.
- **Narvi Desktop** (individual client; planned) — proprietary and paid; talks
  to the engine only through its versioned public API.

Contributions to this repository require a signed [CLA](CLA.md) — a license,
not an assignment: you keep ownership of your work. The engine stays complete
on its own: RBAC, SSO (OIDC), sandbox isolation and the entire per-repository
review pipeline live here, and no security feature sits behind a paywall.
