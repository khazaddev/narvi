/**
 * Hand-written type-level fixture (not generated, unlike everything under
 * gen/ts/). Pins the §6.1 / §9.2 regression this PR exists to catch on the
 * TypeScript side, mirroring contracts/contractstest's Go version:
 * StepFinish.cost.tokens is an OBJECT, never a bare number. `npm run
 * typecheck` fails if either assignment below stops behaving as annotated —
 * that's the point.
 */
import type { StepFinish } from '../gen/ts/sandbox-ws-events';

// Object-shaped cost.tokens (the correct, contract-mandated shape) must
// typecheck with no error.
const valid: StepFinish = {
  type: 'step_finish',
  messageId: 'm1',
  sessionId: '5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a',
  gen: 1,
  stepId: 'step-1',
  cost: {
    tokens: { input: 100, output: 50 },
  },
};
void valid;

// Number-shaped cost.tokens (the regression §6.1 warns about: "a
// number-vs-object mismatch here silently zeroes cost tracking downstream")
// must NOT typecheck. If this stops being a type error, StepFinish's
// generated shape has regressed back to accepting a bare number.
const invalid: StepFinish = {
  type: 'step_finish',
  messageId: 'm2',
  sessionId: '5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a',
  gen: 1,
  stepId: 'step-1',
  cost: {
    // @ts-expect-error tokens must be {input, output, cached?}, never a number
    tokens: 100,
  },
};
void invalid;
