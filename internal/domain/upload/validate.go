package upload

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// MaxFilenameBytes and MaxContentTypeBytes cap the byte length of the two
// untrusted, free-text fields a mint request declares (§28.4's own
// {filename, contentType, sizeBytes}) -- enforced at mint
// (httpapi/uploadmint.go's own mintUploadCore, the shared core BOTH auth
// variants run through) before an artifact row is ever created. 255 is
// this package's own deliberate choice for both: no field in §28.4's spec
// text names a concrete limit, so this mirrors the familiar, well-known
// POSIX/most-filesystems own 255-byte filename convention -- a value every
// legitimate caller is already used to, and small enough that neither
// field can meaningfully bloat a rendered turn prompt (prompt.go's own
// RenderAttachmentBlock) even before sanitizeUntrustedField's own
// placeholder-token/delimiter neutralization runs.
const (
	MaxFilenameBytes    = 255
	MaxContentTypeBytes = 255
)

// ValidateUploadMetadata is mint's OWN first line of defense (layer 1 of
// two, defense in depth -- see prompt.go's own sanitizeUntrustedField for
// layer 2) against a hostile filename/contentType: a §28.5 finding proved
// that because sandbox-agent's own prompt-placeholder substitution
// (cmd/sandbox-agent/reviewverdicttoolprompt.go) runs strings.ReplaceAll
// over a turn's ENTIRE assembled prompt text, an attacker-controlled
// filename containing a newline could close prompt.go's own
// <upload_attachments> data fence early, and one containing a literal
// placeholder token (e.g. "{{UPLOAD_TOOL_BEARER}}") would be
// deterministically expanded into that turn's REAL, live sandbox
// bearer/gen -- the credential for every sandbox-bearer endpoint,
// including scm-credentials and provider-credentials.
//
// Rejects, in EITHER field:
//   - Any C0 or C1 control character (unicode.IsControl -- U+0000-U+001F,
//     U+007F, and U+0080-U+009F), which covers newline/CR/tab and every
//     other byte that could otherwise break prompt.go's own delimited
//     block onto a new "line" a naive text-scanning reader might treat as
//     a structural boundary. This is the primary control for the
//     fence-break vector; prompt.go's own render-time escaping of "<"/">"
//     is the second, independent layer that holds even for a value this
//     check never saw.
//   - Invalid UTF-8, which unicode.IsControl's own rune-by-rune iteration
//     cannot meaningfully classify one way or the other (Go's range over
//     a string silently substitutes utf8.RuneError for each invalid byte
//     sequence) -- rejected outright rather than silently ignored.
//   - A value longer than MaxFilenameBytes/MaxContentTypeBytes.
//
// This is a pure, in-process string check (§11: no I/O, no time.Now(), no
// randomness) -- safe to call from mintUploadCore's own shared core,
// exercised identically by BOTH the sandbox-bearer and browser auth
// variants (uploadmint.go), never duplicated divergently between them.
//
// Returns a plain error whose Error() text is safe to return verbatim in
// a 4xx body (it names only the field/limit violated, never echoes the
// offending value itself, which -- being hostile by definition here --
// this function deliberately never reflects back to the caller); nil
// when both fields pass.
func ValidateUploadMetadata(filename, contentType string) error {
	if err := validateMetadataField("filename", filename, MaxFilenameBytes); err != nil {
		return err
	}
	return validateMetadataField("contentType", contentType, MaxContentTypeBytes)
}

// validateMetadataField is ValidateUploadMetadata's own shared per-field
// check -- see that function's own doc comment for the full rationale.
func validateMetadataField(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds the maximum length of %d bytes", field, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}
