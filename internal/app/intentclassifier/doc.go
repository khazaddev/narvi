// Package intentclassifier is the impure orchestration layer for the
// unified intent classifier (§8.3, §18) -- mirrors internal/app/
// imagebuild's/internal/app/outboxworker's own "app-level service
// composing ports+domain" shape, NOT internal/domain/intent, since
// calling an LLM is I/O.
//
// Service.Classify is the real ports.IntentClassifier implementation:
// assembles the prompt from the DB-backed template (via TemplateFetcher)
// + the confidence rubric constant (internal/domain/intent.
// ConfidenceRubric, placed at the confidence field's own JSON-schema
// description level), calls ports.LLM.Complete, maps its typed
// *ports.LLMError.Code onto the SAME FallbackReason taxonomy 1:1 (never
// string-matched), runs deterministic Target corroboration for callers
// that supply one (internal/domain/intent.CorroborateTarget), and returns
// a ports.IntentDecision -- NEVER a caller-fatal error, per §18.1's
// never-throw contract.
//
// Two capabilities beyond the ports.IntentClassifier interface itself
// (which only takes an input, with no session id, and so cannot persist
// anything) live on the concrete *Service:
//
//   - IsActive(surface) implements §18.5's permanent shadow-vs-active
//     gate: a surface not explicitly listed in the configured active set
//     defaults to shadow, forever -- never silently flipped active.
//   - RecordDecision persists an intent.IntentDecisionRecord write-once
//     (§18.4's guarded UPDATE, internal/adapters/outbound/postgres.
//     SessionStore.UpdateIntentDecisionIfNull) -- first decision wins, no
//     application-level lock needed.
//
// TemplateFetcher and DecisionStore are narrow, LOCAL interfaces (not
// *postgres.PromptTemplateStore/*postgres.SessionStore directly) so this
// package's own tests substitute in-memory fakes without a real Postgres
// connection; postgres.PromptTemplateStore/SessionStore both already
// satisfy them structurally (no import needed in either direction).
package intentclassifier
