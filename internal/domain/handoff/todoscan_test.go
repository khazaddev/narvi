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
