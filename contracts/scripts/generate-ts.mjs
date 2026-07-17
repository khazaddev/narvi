#!/usr/bin/env node
// Generates TypeScript types under contracts/gen/ts/ from the JSON Schemas
// under /contracts (technical plan §6.3: "Generate TS types for the UI from
// /contracts; /contracts is the single source — no hand-written response
// types"; §9.2: "TS codegen compiles against frontend usage").
//
// Run via `npm run generate` (see contracts/package.json). Output is
// regenerated from scratch every run — never hand-edit files under gen/ts/.
import { mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { compileFromFile } from 'json-schema-to-typescript';

const here = path.dirname(fileURLToPath(import.meta.url));
const contractsRoot = path.resolve(here, '..');
const outDir = path.join(contractsRoot, 'gen', 'ts');

// One entry per /contracts schema file (§6). `out` names mirror the Go
// package names go-jsonschema generates for the same schema
// (contracts/gen/go/*), so both codegen targets agree on which source
// schema a given generated name came from.
//
// `unreachableDefs: true` is required for client-ws and rest-dtos: those two
// schemas have no unifying top-level oneOf/$ref (§6.2, §6.3: "independent
// named payloads, not a discriminated union"), so none of their $defs are
// reachable from the schema root by $ref — without the flag they'd silently
// compile to an empty `{ [k: string]: unknown }` and nothing else. The other
// three schemas' $defs are already reachable via a root oneOf/$ref, so
// leaving the flag off for them avoids emitting a redundant duplicate
// (`SessionConfig` + `SessionConfig1`, etc.) of every type.
const schemas = [
  { in: 'sandbox-ws/v1/commands.schema.json', out: 'sandbox-ws-commands.ts', unreachableDefs: false },
  { in: 'sandbox-ws/v1/events.schema.json', out: 'sandbox-ws-events.ts', unreachableDefs: false },
  { in: 'client-ws/v1/protocol.schema.json', out: 'client-ws.ts', unreachableDefs: true },
  { in: 'session-config/v1/session-config.schema.json', out: 'session-config.ts', unreachableDefs: false },
  { in: 'rest/v1/dtos.schema.json', out: 'rest-dtos.ts', unreachableDefs: true },
];

const bannerComment = `/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * \`npm run generate\` instead.
 */
`;

async function main() {
  await rm(outDir, { recursive: true, force: true });
  await mkdir(outDir, { recursive: true });

  for (const schema of schemas) {
    const inputPath = path.join(contractsRoot, schema.in);
    const ts = await compileFromFile(inputPath, {
      cwd: path.dirname(inputPath),
      bannerComment,
      style: { singleQuote: true },
      unreachableDefinitions: schema.unreachableDefs,
    });
    const outputPath = path.join(outDir, schema.out);
    await writeFile(outputPath, ts);
    console.log(`generated gen/ts/${schema.out} from ${schema.in}`);
  }
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
