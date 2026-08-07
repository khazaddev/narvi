// Package shadowcompare is Step 59's own "shadow-comparison tooling for
// review" deliverable (IMPLEMENTATION_PLAN.md Step 59 row), reusing
// §9.4/§18.5's own shadow-mode discipline ("the same mechanism is used
// again for every future model swap, prompt change, or new surface, not
// just the first activation").
//
// # Structural decision (named here, since §29 has no dedicated
// subsection for this piece)
//
// This is a deliberately minimal, from-scratch interpretation: a
// READ-ONLY, side-effect-free comparison of two ALREADY-COMPLETED turns
// (e.g. the same PR/prompt dispatched once on the active model/effort and
// once on a shadow/candidate one, or a session's own two differently-
// configured re-runs), never a re-execution orchestrator of its own.
// "Shadow" here means "never affects either compared turn or its
// session" -- the same never-act-only-observe posture §18.5 requires stay
// PERMANENT (never a one-time launch gate), applied to model/effort
// evaluation rather than classifier routing. It is genuinely useful for
// exactly the case this Step exists to enable: deciding whether to widen
// a Codex/Gemini/effort rollout by comparing real, already-run turns
// side by side, without inventing new turn-dispatch machinery (a
// materially larger, riskier undertaking §29 never asks for).
//
// This package is pure (§11: no I/O, time.Now(), or randomness) --
// internal/app/shadowcompare (or the httpapi handler directly) loads the
// real turn/session rows and converts them into TurnSnapshot values;
// Compare only ever computes over already-fetched data.
package shadowcompare
