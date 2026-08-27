package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoSectionRefInOperatorCopy fails when a technical-plan section number
// reaches the text the web UI renders. Comments may cite §§ freely and are
// stripped before the scan; what remains is what an operator can read, and
// an operator has no access to the technical plan.
//
// Twelve of these shipped across four screens during the UI phase. None was
// caught by a check -- they were found by opening the running app and
// reading it, which is not a repeatable defence.
func TestNoSectionRefInOperatorCopy(t *testing.T) {
	root := repoRoot(t)

	refs, err := CheckOperatorCopy(root, []string{"web/src"})
	if err != nil {
		t.Fatalf("CheckOperatorCopy: %v", err)
	}
	for _, r := range refs {
		t.Errorf("%s:%d renders a technical-plan section number to an operator: %s\n"+
			"    Operator-facing copy names what the system is doing, never which section specifies it.\n"+
			"    Move the citation into a comment, where it belongs and where this check ignores it.",
			r.File, r.Line, r.Text)
	}
}

// TestCheckOperatorCopy_CatchesAViolationAndSparesComments feeds the checker
// input it MUST reject and input it MUST accept. Without it, the check above
// passing would prove only that the tree is currently clean -- not that the
// checker can tell the two apart, which is the entire thing it exists to do.
func TestCheckOperatorCopy_CatchesAViolationAndSparesComments(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "web", "src")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name string
		file string
		body string
		want int
	}{
		{
			name: "a citation in a JSX text node is reported",
			file: "Rendered.tsx",
			body: "export const A = () => <p>Fails closed, matching every other §27.3 surface.</p>\n",
			want: 1,
		},
		{
			name: "a citation in a returned string is reported -- copy is often assembled, not inline",
			file: "Copy.ts",
			body: "export const NOTICE = 'None of §15.3\\'s composition criteria were met.'\n",
			want: 1,
		},
		{
			name: "a citation in a line comment is NOT reported",
			file: "Commented.tsx",
			body: "// Implements §25.15's own cost rule.\nexport const B = () => <p>Cost so far</p>\n",
			want: 0,
		},
		{
			name: "a citation in a block comment is NOT reported",
			file: "Blocked.tsx",
			body: "/**\n * §12.5 says configured is structurally always true.\n */\nexport const C = () => <p>Last delivery</p>\n",
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(src, tc.file)
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			defer func() { _ = os.Remove(path) }()

			refs, err := CheckOperatorCopy(dir, []string{"web/src"})
			if err != nil {
				t.Fatalf("CheckOperatorCopy: %v", err)
			}
			if len(refs) != tc.want {
				t.Errorf("got %d findings, want %d: %+v", len(refs), tc.want, refs)
			}
		})
	}
}
