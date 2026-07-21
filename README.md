# Narvi

**Autonomous coding agents, in isolated sandboxes.**

> *Im Narvi hain echant* — "I, Narvi, made these." Narvi was the craftsman who forged the Doors of Durin. This project builds doors, well made: a clean ports-and-adapters core with swappable edges.

Narvi runs autonomous coding agents inside isolated cloud sandboxes. A person — or an automation — starts a session from the web, Slack, Linear, or GitHub; Narvi provisions a sandbox, runs the agent against the target repositories, streams the work back in real time, and opens a pull request attributed to the requester. Sessions are durable, recoverable, and multiplayer.

This project was started after running [OpenInspect](https://github.com/ColeMurray/background-agents) in production at [Fountain](https://github.com/onboardiq/background-agents). Narvi draws on that experience, but is an independent project — not a fork or a rewrite of OpenInspect, and shares no code with it.

## Architecture at a glance

- **Two Go services** — a control plane and an in-sandbox agent — packaged as containers that run on any Kubernetes cluster or plain Docker/VMs, with **Postgres as the single source of truth**. Host it wherever you like; no cloud lock-in.
- **Hexagonal (ports & adapters):** a pure domain core; sandbox providers, source-control, notifiers, and the agent runtime are interchangeable adapters behind interfaces.
- **One authoritative state machine per session**, with fencing and named persistent timers — consistency and liveness guaranteed by construction, not by convention.
- **A thin web UI** embedded in the control-plane binary (`go:embed`) — self-host is one binary plus a database.

## Documents

| Path | What it is |
|---|---|
| [`docs/TECHNICAL_PLAN.md`](docs/TECHNICAL_PLAN.md) | The full technical specification: architecture, domain model, ports, cross-cutting invariants, wire contracts, feature set, identity/RBAC, the product-prototyping workflow, release review, and the decision inbox. Self-contained. |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | The work broken into ~65 ordered Steps across 7 phases (0–6), each shippable as one pull request and referencing the technical-plan section that specifies it. |
| [`docs/design/mockups.html`](docs/design/mockups.html) | Visual specification — nine UI views with numbered design decisions. Open in a browser. |

## Building it

The specification is written to be executed by an AI coding agent, one Step at a time. See [`CLAUDE.md`](CLAUDE.md) for how to drive it.

## Development

```bash
make build   # go build ./...
make lint    # golangci-lint + the project's custom static-analysis checks
make test    # go test -race ./...
```

## License

Copyright (C) 2026 Benoît LELEVÉ.

Licensed under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0).
