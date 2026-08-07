// Package contractstest holds the round-trip contract tests required by
// technical plan §9.2 ("Round-trip tests for every /contracts schema: Go
// marshal -> validate -> unmarshal") for every schema under /contracts:
// sandbox-ws v1 (commands + events), client-ws v1, session-config v1, and
// rest v1. Each schema is compiled straight from contracts.FS (the same
// embedded copy the running binary would use) with santhosh-tekuri/jsonschema
// so validation exercises the actual shipped schema text, not a hand-copied
// stand-in.
package contractstest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/khazaddev/narvi/contracts"
)

// testSessionID is a syntactically valid UUID (format: uuid is asserted, see
// newCompiler) reused across fixtures that need a sessionId.
const testSessionID = "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a"

// testSandboxID is a syntactically valid UUID, distinct from testSessionID,
// reused across fixtures that need a sandboxId (SessionConfig's own
// "format": "uuid" constraint validates the VALUE, not just key presence --
// an empty string fails format validation even though the required-key
// check alone would pass).
const testSandboxID = "9b1a6b1a-4b1a-9b1a-6b1a-5b1c1e2e6b1a"

// testUploadID is a syntactically valid UUID, distinct from both above,
// reused across fixtures that need an upload artifact id (Step 58, §28.4/
// §28.5's own MintUploadResponse.uploadId / CreateTurnRequest.attachmentIds).
const testUploadID = "6b1a4b1a-9b1a-5b1c-1e2e-6b1a9b1a4b1a"

// newCompiler returns a compiler with format assertions turned on
// (uuid/date-time/email/uri), so "format" isn't just documentation here — a
// malformed value in one of those fields actually fails validation.
func newCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	return c
}

// compileSchema loads the schema file at path (relative to the root of
// contracts.FS, e.g. "sandbox-ws/v1/commands.schema.json"), adds it as a
// resource keyed by its own "$id", and compiles the schema reachable at
// id+fragment. An empty fragment compiles the document's own root schema
// (whatever top-level "oneOf"/"$ref" it declares); a fragment such as
// "#/$defs/Session" compiles one named sub-schema directly, for the schemas
// in this repo that deliberately have no unifying top-level oneOf (§6.2,
// §6.3: "independent named payloads, not a discriminated union").
func compileSchema(t testing.TB, path, fragment string) *jsonschema.Schema {
	t.Helper()

	data, err := contracts.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}

	var probe struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("probe $id in %s: %v", path, err)
	}
	if probe.ID == "" {
		t.Fatalf("schema %s has no $id", path)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode schema %s: %v", path, err)
	}

	c := newCompiler()
	if err := c.AddResource(probe.ID, doc); err != nil {
		t.Fatalf("add schema resource %s: %v", path, err)
	}

	sch, err := c.Compile(probe.ID + fragment)
	if err != nil {
		t.Fatalf("compile schema %s%s: %v", path, fragment, err)
	}
	return sch
}

// validateJSON decodes raw JSON the way jsonschema.Schema.Validate requires
// (numbers preserved distinctly from other types, so an object-shaped vs.
// number-shaped field is judged correctly — see santhosh-tekuri/jsonschema's
// own UnmarshalJSON contract) and validates it against sch.
func validateJSON(t testing.TB, sch *jsonschema.Schema, data []byte) error {
	t.Helper()

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode instance for validation: %v", err)
	}
	return sch.Validate(inst)
}

// roundTrip performs the §9.2 contract-test sequence for one payload: Go
// marshal -> JSON Schema validate -> Go unmarshal -> compare against the
// original value. It fails the test at whichever step breaks first.
func roundTrip[T any](t *testing.T, sch *jsonschema.Schema, v T) {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := validateJSON(t, sch, data); err != nil {
		t.Fatalf("schema validation failed: %v\npayload: %s", err, data)
	}

	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(v, out) {
		t.Fatalf("round trip mismatch:\n  in:  %#v\n  out: %#v", v, out)
	}
}
