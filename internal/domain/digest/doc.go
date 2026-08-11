// Package digest renders §21.3's own deterministic daily digest --
// "entirely deterministic, never LLM-narrated ... renders from the same
// read model above via a template, not a model call". Pure per
// CLAUDE.md/§11: no I/O, no time.Now(), no randomness, and (a stricter
// bar than most domain packages in this codebase) no branching on
// anything OTHER than the already-computed RollupData this package is
// handed -- there is no LLM call, no prompt, and nothing here ever
// imports internal/app/ports.LLM or any adapter. Render is a plain string
// template, table-driven-tested exactly like any other pure function in
// this codebase, which is the whole point: "a fixed rendering is easier
// to trust and to test than a fresh narration every day."
//
// Two providers, ONE shared RollupData input, formatted differently only
// where the two channels' own text dialects genuinely differ (Slack's
// single-asterisk mrkdwn *bold* vs Linear's plain CommonMark **bold**) --
// never two independently-maintained templates that could silently drift
// in CONTENT, only in the one narrow surface (Format) both call through.
package digest
