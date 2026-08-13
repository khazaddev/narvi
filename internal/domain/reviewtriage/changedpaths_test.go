package reviewtriage_test

import (
	"reflect"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

func TestExtractChangedPaths(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []string
	}{
		{"empty diff", "", nil},
		{"no diff --git headers at all", "just some text\nwith no diff shape\n", nil},
		{
			"single file",
			"diff --git a/foo.go b/foo.go\n" +
				"index 111..222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-old\n" +
				"+new\n",
			[]string{"foo.go"},
		},
		{
			"multiple files, order preserved",
			"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n" +
				"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+y\n",
			[]string{"a.go", "b.go"},
		},
		{
			// Adversarial-review fix (D3): a deleted file's own
			// "+++ /dev/null" header used to contribute NO path entry at
			// all -- this pins the fix instead: the paired "--- a/<path>"
			// side (this SAME file section's own pre-change path) is now
			// harvested.
			"deleted file's own pre-change path is harvested via +++ /dev/null pairing",
			"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			[]string{"gone.go"},
		},
		{
			"duplicate header lines dedupe",
			"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n" +
				"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -5 +5 @@\n-x\n+y\n",
			[]string{"a.go"},
		},
		{
			// Adversarial-review fix (D3): a 100%-similarity rename (no
			// content change at all) emits neither a "---" nor a "+++"
			// line -- the ONLY place either path appears is "rename
			// from"/"rename to". Both the vacated old location and the
			// new one are real changes to the tree.
			"pure rename (100% similarity, no content change) harvests both paths",
			"diff --git a/old/widget.go b/new/widget.go\nsimilarity index 100%\nrename from old/widget.go\nrename to new/widget.go\n",
			[]string{"old/widget.go", "new/widget.go"},
		},
		{
			// A rename that ALSO changes content -- git still emits
			// rename from/to, AND a "---"/"+++" pair for the actual diff
			// hunks. Both paths still come through exactly once each,
			// deduped.
			"rename with content change harvests both paths, deduped",
			"diff --git a/old/widget.go b/new/widget.go\nsimilarity index 90%\nrename from old/widget.go\nrename to new/widget.go\nindex 111..222 100644\n--- a/old/widget.go\n+++ b/new/widget.go\n@@ -1 +1 @@\n-x\n+y\n",
			[]string{"old/widget.go", "new/widget.go"},
		},
		{
			// A deletion followed by an addition in the SAME diff must
			// never let the deletion's own pendingRemoved path leak
			// forward onto the addition -- the addition's own
			// "--- /dev/null" line must reset it first.
			"deletion immediately followed by an unrelated addition never cross-contaminates",
			"diff --git a/deleted.go b/deleted.go\n--- a/deleted.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n" +
				"diff --git a/added.go b/added.go\n--- /dev/null\n+++ b/added.go\n@@ -0,0 +1 @@\n+x\n",
			[]string{"deleted.go", "added.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.ExtractChangedPaths(tt.diff)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractChangedPaths() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
