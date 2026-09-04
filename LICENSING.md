# Licensing

**Narvi — this repository** is source-available under the
[Elastic License 2.0](LICENSE). Free to read, use, modify and self-host. You
may not provide it to third parties as a managed service, circumvent its
license-key functionality, or remove licensing notices.

Two companion projects are planned on top of it — **Narvi Gatekeeper**
(organization-scale review governance) and **Narvi Desktop** (an individual
client). Neither exists yet, and **their distribution terms are not decided**.
Nothing on this page should be read as an announcement about either.

What is decided, and will not change: the engine stays complete on its own.
RBAC, SSO, sandbox isolation, shadow mode and the entire per-repository review
pipeline live here, and **no security capability is ever moved out of this
repository or made conditional on an entitlement**. The technical seams that
let an optional module compose on top are specified in
`docs/TECHNICAL_PLAN.md` §34.

Contributions require a signed [CLA](CLA.md) — a license, not an assignment:
you keep ownership of your work. It exists so the project keeps a single,
coherent licensing story and the freedom to make those undecided choices
later.
