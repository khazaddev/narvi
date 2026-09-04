# Contributing to Narvi

Thanks for considering a contribution. Narvi is **source-available** under the
[Elastic License 2.0](LICENSE) and is under heavy active development — the
system is built by executing [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md)
one Step at a time, so the codebase moves fast and in a planned order.

## Before you start

- **Open an issue first** for anything larger than a small fix. A change that
  collides with a planned Step (or re-litigates a decision the technical plan
  already records) is unlikely to be merged, however good the code.
- **Read the spec.** [`docs/TECHNICAL_PLAN.md`](docs/TECHNICAL_PLAN.md) is the
  source of truth for behavior; the section (§) that covers your area explains
  the invariants your change must preserve.

## Contributor License Agreement

Your first pull request must carry a signed
[Individual Contributor License Agreement](CLA.md). An automated check will
prompt you on the PR — signing is a single comment:

> I have read the CLA Document and I hereby sign the CLA

The CLA is a license, not a copyright assignment: you keep ownership of your
work. It exists so the project can keep a single, coherent licensing story.

Signatures are recorded on the repository's `cla-signatures` branch, which
holds nothing else.

## Ground rules (CI-enforced)

- **Branch names**: `<type>/<short-kebab-description>` — e.g.
  `fix/webhook-dedupe-window`. Types: `feat`, `fix`, `docs`, `chore`,
  `refactor`, `test`, `perf`, `ci`, `build`.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):
  `<type>[(scope)]: <short, imperative description>` — scope is the primary
  package touched (`platform`, `postgres`, `contracts`, `sessionactor`, …).
- **Pull requests are squash-merged**; the PR title becomes the commit subject,
  so it follows the same convention. All required checks must pass and review
  threads must be resolved.

## Code conventions

- No I/O, `time.Now()`, or randomness in `/internal/domain` — inject
  `Clock`/`IDGen`.
- Every state transition goes through the relevant machine's transition table.
- Every timeout or interval lives in `platform/timeouts.go`.
- Table-driven tests; `go test -race` always.
- Concurrency uses `errgroup` + context; no naked goroutines.

## Developing

```bash
make build            # go build ./...
make lint             # golangci-lint + the project's custom analyzers (tools/lint/narvichecks)
make test             # go test -race ./...
make test-integration # integration suite (needs the local test database; runs packages serially)
```

CI runs `branch-name`, `commit-scope`, `checks` (lint + unit tests), four
`test-integration` groups, `web`, and `sandbox-image` on every PR.

## Security

Do not report vulnerabilities in public issues — see [SECURITY.md](SECURITY.md).
