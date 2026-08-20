#!/usr/bin/env node
// Enforces §12.1's own rule for the frontend: "TS types + typed API client
// + WS event handlers are codegen outputs; no hand-written response types
// anywhere." internal/ops (Go) already carries three checks in this same
// family (TestNoMetricDrift, TestNoGuideDrift, TestNoStepRefInSource) --
// this is that pattern's frontend sibling, over TypeScript source instead
// of Go.
//
// # What this checks, and why THIS shape specifically
//
// A structural check ("does this hand-written interface's FIELD SHAPE
// duplicate a generated DTO's") was considered and rejected: TypeScript
// interfaces are structurally typed by design, so any local view-model
// that happens to share a few field names with a generated DTO (e.g. a
// component prop bag with an `id: string` and a `status: string`) would
// false-positive constantly on pure coincidence, with no reliable way to
// tell "this is secretly re-modeling the wire shape" apart from "this is
// an unrelated local shape that merely has a similarly-named field" without
// real semantic analysis this script does not have. That is exactly the
// "fights its users" failure mode internal/ops/stepref.go's own doc
// comment names for an over-broad check.
//
// What IS cheap, mechanical, and low-noise: NAME collision. Every
// contracts/gen/ts/*.ts export is a name that already means one specific
// wire shape (Session, CreateSessionRequest, ...). A hand-written
// `interface`/`type` under web/src reusing that EXACT name is never an
// innocent coincidence in the way a shared field name can be -- it is
// either (a) a genuine re-declaration of the wire type (the bug §12.1
// exists to prevent) or (b) a local concept confusingly given the same
// name as an unrelated generated type (worth renaming regardless, so
// flagging it is still the right nudge). Both cases are worth failing CI
// over; neither is a plausible false positive. This is also just a
// mechanical reading of the task's own simpler alternative framing: "API
// response types are only ever imported from contracts/gen/ts/" -- if
// nothing under web/src declares a generated name locally, every use of
// that name necessarily imports it from contracts/gen/ts (TypeScript has
// no other source it could resolve to).
//
// Scans every generated export name out of contracts/gen/ts/*.ts, then
// every `interface`/`type` declaration (exported or not -- an unexported
// one still shadows the name within its own file) under web/src/**/*.{ts,tsx}
// (excluding routeTree.gen.ts, itself a generated file with no hand-written
// declarations to check), and fails if any declaration reuses a generated
// name.
import { readFile, readdir } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.resolve(here, '..')
const repoRoot = path.resolve(webRoot, '..')
const generatedDir = path.join(repoRoot, 'contracts', 'gen', 'ts')
const srcDir = path.join(webRoot, 'src')

const DECL_PATTERN = /^(?:export\s+)?(?:interface|type)\s+([A-Za-z_$][\w$]*)/gm

async function collectGeneratedNames() {
  const names = new Map() // name -> source file (relative to repo root)
  const entries = await readdir(generatedDir)
  for (const entry of entries) {
    if (!entry.endsWith('.ts')) continue
    const filePath = path.join(generatedDir, entry)
    const text = await readFile(filePath, 'utf8')
    for (const match of text.matchAll(DECL_PATTERN)) {
      names.set(match[1], path.relative(repoRoot, filePath))
    }
  }
  return names
}

async function walkTsFiles(dir) {
  const out = []
  const entries = await readdir(dir, { withFileTypes: true })
  for (const entry of entries) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...(await walkTsFiles(full)))
      continue
    }
    if (!/\.(ts|tsx)$/.test(entry.name)) continue
    if (entry.name === 'routeTree.gen.ts') continue
    out.push(full)
  }
  return out
}

async function main() {
  const generatedNames = await collectGeneratedNames()
  const files = await walkTsFiles(srcDir)

  const violations = []
  for (const filePath of files) {
    const text = await readFile(filePath, 'utf8')
    const lines = text.split('\n')
    for (const match of text.matchAll(DECL_PATTERN)) {
      const name = match[1]
      const generatedSource = generatedNames.get(name)
      if (!generatedSource) continue
      const upTo = text.slice(0, match.index).split('\n').length
      violations.push({
        file: path.relative(repoRoot, filePath),
        line: upTo,
        name,
        generatedSource,
        text: lines[upTo - 1]?.trim() ?? '',
      })
    }
  }

  if (violations.length === 0) {
    console.log(
      `check-no-dto-redeclaration: OK -- 0 hand-written declarations under web/src collide with a contracts/gen/ts export (${generatedNames.size} generated names checked against ${files.length} source files).`,
    )
    return
  }

  console.error('check-no-dto-redeclaration: found hand-written type(s) reusing a generated DTO name:\n')
  for (const v of violations) {
    console.error(`  ${v.file}:${v.line}: ${v.text}`)
    console.error(`    "${v.name}" is already generated by ${v.generatedSource} -- import it from there instead of redeclaring it.\n`)
  }
  process.exitCode = 1
}

main().catch((err) => {
  console.error('check-no-dto-redeclaration: unexpected error:', err)
  process.exitCode = 1
})
