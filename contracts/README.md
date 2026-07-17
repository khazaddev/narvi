# contracts

Versioned JSON Schemas for the wire contracts this system must preserve
byte-for-byte: the sandbox WS protocol, the client WS protocol,
`SESSION_CONFIG`, and REST DTOs (§6). Go types are generated from these
schemas for the control plane and the sandbox agent; TS types are generated
for the frontend. `/contracts` is the single source of wire truth — no
hand-written response types anywhere else in the codebase.

This directory is populated in **PR-05** (§6): JSON Schemas, Go + TS codegen,
and round-trip contract tests (including the dedicated `tokens`
object-not-number regression test called out in §6.1).
