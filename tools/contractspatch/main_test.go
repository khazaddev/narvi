package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPatchFile pins the classification rule in the package comment: a
// defined pointer type is rewritten to an alias unless its pointee is a
// predeclared basic type, which can never carry methods of its own and so
// is already decoded correctly by encoding/json.
func TestPatchFile(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        string
		wantPatched int
	}{
		{
			name:        "NullableDateTimeIsAliased",
			in:          "package p\n\nimport \"time\"\n\ntype Foo *time.Time\n",
			want:        "package p\n\nimport \"time\"\n\ntype Foo = *time.Time\n",
			wantPatched: 1,
		},
		{
			// The pointee has its own validating UnmarshalJSON, which a
			// defined pointer type would silently bypass -- the same
			// method-set rule, with a worse failure mode than a decode
			// error (skipped validation).
			name:        "NullableGeneratedStructIsAliased",
			in:          "package p\n\ntype Bar struct{}\n\ntype Foo *Bar\n",
			want:        "package p\n\ntype Bar struct{}\n\ntype Foo = *Bar\n",
			wantPatched: 1,
		},
		{
			name:        "BasicPointeesAreLeftAlone",
			in:          "package p\n\ntype A *string\n\ntype B *int\n\ntype C *bool\n\ntype D *float64\n",
			want:        "package p\n\ntype A *string\n\ntype B *int\n\ntype C *bool\n\ntype D *float64\n",
			wantPatched: 0,
		},
		{
			// Idempotency is what keeps `make contracts-check`'s
			// regenerate-and-diff free of spurious drift.
			name:        "AlreadyAliasedIsANoOp",
			in:          "package p\n\nimport \"time\"\n\ntype Foo = *time.Time\n",
			want:        "package p\n\nimport \"time\"\n\ntype Foo = *time.Time\n",
			wantPatched: 0,
		},
		{
			name:        "NonPointerDeclsAreUntouched",
			in:          "package p\n\nimport \"time\"\n\ntype Foo time.Time\n\ntype Bar string\n\ntype Baz []string\n",
			want:        "package p\n\nimport \"time\"\n\ntype Foo time.Time\n\ntype Bar string\n\ntype Baz []string\n",
			wantPatched: 0,
		},
		{
			// go-jsonschema emits one decl per type, but a grouped block
			// must not shift the offsets of its own later specs.
			name:        "GroupedDeclPatchesEverySpec",
			in:          "package p\n\nimport \"time\"\n\ntype (\n\tA *time.Time\n\tB *string\n\tC *time.Time\n)\n",
			want:        "package p\n\nimport \"time\"\n\ntype (\n\tA = *time.Time\n\tB *string\n\tC = *time.Time\n)\n",
			wantPatched: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gen.go")
			if err := os.WriteFile(path, []byte(tt.in), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			patched, err := patchFile(path)
			if err != nil {
				t.Fatalf("patchFile: %v", err)
			}
			if patched != tt.wantPatched {
				t.Errorf("patched = %d, want %d", patched, tt.wantPatched)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("output mismatch:\n got: %q\nwant: %q", got, tt.want)
			}

			// Running again must change nothing further.
			again, err := patchFile(path)
			if err != nil {
				t.Fatalf("patchFile (second run): %v", err)
			}
			if again != 0 {
				t.Errorf("second run patched %d decl(s), want 0 (not idempotent)", again)
			}
		})
	}
}
