package handoff_test

import (
	"reflect"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/handoff"
)

func TestScanTODOs_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []handoff.TODOFinding
	}{
		{
			name: "empty diff",
			diff: "",
			want: nil,
		},
		{
			name: "whitespace-only diff",
			diff: "   \n\t\n",
			want: nil,
		},
		{
			name: "single added TODO line",
			diff: "diff --git a/apps/web/src/api.ts b/apps/web/src/api.ts\n" +
				"index 111..222 100644\n" +
				"--- a/apps/web/src/api.ts\n" +
				"+++ b/apps/web/src/api.ts\n" +
				"@@ -1,3 +1,4 @@\n" +
				" import x from 'x'\n" +
				"+// TODO: replace mock with real backend once available\n" +
				" const a = 1\n" +
				" const b = 2\n",
			want: []handoff.TODOFinding{
				{FilePath: "apps/web/src/api.ts", Line: 2, Text: "// TODO: replace mock with real backend once available"},
			},
		},
		{
			name: "FIXME marker also matches, case-insensitively",
			diff: "diff --git a/foo.ts b/foo.ts\n" +
				"--- a/foo.ts\n" +
				"+++ b/foo.ts\n" +
				"@@ -5,2 +5,3 @@\n" +
				" line5\n" +
				"+// fixme: wire this up\n" +
				" line6\n",
			want: []handoff.TODOFinding{
				{FilePath: "foo.ts", Line: 6, Text: "// fixme: wire this up"},
			},
		},
		{
			name: "removed TODO line is never reported",
			diff: "diff --git a/foo.ts b/foo.ts\n" +
				"--- a/foo.ts\n" +
				"+++ b/foo.ts\n" +
				"@@ -1,2 +1,1 @@\n" +
				"-// TODO: old marker\n" +
				" line1\n",
			want: nil,
		},
		{
			name: "context line containing TODO is never reported (unchanged)",
			diff: "diff --git a/foo.ts b/foo.ts\n" +
				"--- a/foo.ts\n" +
				"+++ b/foo.ts\n" +
				"@@ -1,2 +1,3 @@\n" +
				" // TODO: pre-existing, untouched\n" +
				"+const x = 1\n" +
				" line2\n",
			want: nil,
		},
		{
			name: "identifier substring TODOList is not a marker",
			diff: "diff --git a/foo.ts b/foo.ts\n" +
				"--- a/foo.ts\n" +
				"+++ b/foo.ts\n" +
				"@@ -1,1 +1,2 @@\n" +
				" line1\n" +
				"+const TODOList = []\n",
			want: nil,
		},
		{
			name: "deleted file contributes nothing",
			diff: "diff --git a/foo.ts b/dev/null\n" +
				"--- a/foo.ts\n" +
				"+++ /dev/null\n" +
				"@@ -1,2 +0,0 @@\n" +
				"-// TODO: gone\n" +
				"-line2\n",
			want: nil,
		},
		{
			name: "two files in one diff, second file's line numbers reset",
			diff: "diff --git a/a.ts b/a.ts\n" +
				"--- a/a.ts\n" +
				"+++ b/a.ts\n" +
				"@@ -1,1 +1,2 @@\n" +
				" line1\n" +
				"+// TODO: in file a\n" +
				"diff --git a/b.ts b/b.ts\n" +
				"--- a/b.ts\n" +
				"+++ b/b.ts\n" +
				"@@ -10,1 +10,2 @@\n" +
				" line10\n" +
				"+// TODO: in file b\n",
			want: []handoff.TODOFinding{
				{FilePath: "a.ts", Line: 2, Text: "// TODO: in file a"},
				{FilePath: "b.ts", Line: 11, Text: "// TODO: in file b"},
			},
		},
		{
			name: "multiple hunks in the same file track line numbers independently",
			diff: "diff --git a/a.ts b/a.ts\n" +
				"--- a/a.ts\n" +
				"+++ b/a.ts\n" +
				"@@ -1,1 +1,1 @@\n" +
				" line1\n" +
				"@@ -50,1 +50,2 @@\n" +
				" line50\n" +
				"+// TODO: later in the file\n",
			want: []handoff.TODOFinding{
				{FilePath: "a.ts", Line: 51, Text: "// TODO: later in the file"},
			},
		},
		{
			// doc.go's own design call #4: no content filtering -- the marker
			// is reported regardless of WHERE in the added line it appears,
			// never requiring it to sit inside a "//"/"#" comment specifically.
			name: "TODO inside a string literal still reported",
			diff: "diff --git a/foo.ts b/foo.ts\n" +
				"--- a/foo.ts\n" +
				"+++ b/foo.ts\n" +
				"@@ -1,1 +1,2 @@\n" +
				" line1\n" +
				"+const msg = \"TODO: fix this string before shipping\"\n",
			want: []handoff.TODOFinding{
				{FilePath: "foo.ts", Line: 2, Text: "const msg = \"TODO: fix this string before shipping\""},
			},
		},
		{
			// doc.go's own design call #4: no path filtering -- a test file
			// is scanned exactly like any other file, never excluded by its
			// own name/extension.
			name: "TODO inside a test file still reported",
			diff: "diff --git a/foo_test.go b/foo_test.go\n" +
				"--- a/foo_test.go\n" +
				"+++ b/foo_test.go\n" +
				"@@ -1,1 +1,2 @@\n" +
				" line1\n" +
				"+// TODO: assert the error case too\n",
			want: []handoff.TODOFinding{
				{FilePath: "foo_test.go", Line: 2, Text: "// TODO: assert the error case too"},
			},
		},
		{
			// The bug this test guards against: an ADDED line whose own
			// source text starts with "++ " becomes, once the diff's own
			// leading "+" is prepended, a raw diff line starting with
			// "+++ " -- colliding with the diff FILE header's own prefix.
			// A parser that treats "+++ " as a header ANYWHERE (not just
			// in the per-file header block, before the first "@@") would
			// misread this hunk-body line as a bogus new file header,
			// losing the TODO marker it carries and corrupting
			// currentFile for every line after it. This also asserts a
			// later, ordinary added line ("+// TODO: still attributed to
			// notes.md") is still correctly attributed to notes.md
			// afterward, proving the file/line state was never
			// corrupted.
			name: "added line whose source starts with '++ ' is not misread as a file header",
			diff: "diff --git a/notes.md b/notes.md\n" +
				"--- a/notes.md\n" +
				"+++ b/notes.md\n" +
				"@@ -1,1 +1,4 @@\n" +
				" # Notes\n" +
				"+++ TODO: revisit this bullet\n" +
				"+another line\n" +
				"+// TODO: still attributed to notes.md\n",
			want: []handoff.TODOFinding{
				{FilePath: "notes.md", Line: 2, Text: "++ TODO: revisit this bullet"},
				{FilePath: "notes.md", Line: 4, Text: "// TODO: still attributed to notes.md"},
			},
		},
		{
			// The mirror case for a REMOVED line whose own source starts
			// with "-- " (becomes a raw diff line starting with "--- ",
			// colliding with the diff file header's own old-file prefix).
			// It must still be treated as an ordinary removed line (never
			// reported, new-side line counter untouched), and a
			// subsequent added TODO in the same file must still be
			// attributed correctly.
			name: "removed line whose source starts with '-- ' is not misread as a file header",
			diff: "diff --git a/notes.md b/notes.md\n" +
				"--- a/notes.md\n" +
				"+++ b/notes.md\n" +
				"@@ -1,2 +1,2 @@\n" +
				" # Notes\n" +
				"--- old separator, not a header\n" +
				"+// TODO: still attributed to notes.md\n",
			want: []handoff.TODOFinding{
				{FilePath: "notes.md", Line: 2, Text: "// TODO: still attributed to notes.md"},
			},
		},
		{
			// A second file's REAL "--- "/"+++ " header must still be
			// recognized after the first file's hunk body has already
			// turned inHeaderBlock off -- guards against overcorrecting
			// the fix above into never treating "+++ " as a header again.
			name: "second file's real header is still recognized after the first file's hunk body",
			diff: "diff --git a/a.ts b/a.ts\n" +
				"--- a/a.ts\n" +
				"+++ b/a.ts\n" +
				"@@ -1,1 +1,1 @@\n" +
				" line1\n" +
				"diff --git a/b.ts b/b.ts\n" +
				"--- a/b.ts\n" +
				"+++ b/b.ts\n" +
				"@@ -1,1 +1,2 @@\n" +
				" line1\n" +
				"+// TODO: in b, correctly attributed\n",
			want: []handoff.TODOFinding{
				{FilePath: "b.ts", Line: 2, Text: "// TODO: in b, correctly attributed"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handoff.ScanTODOs(tt.diff)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ScanTODOs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
