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
			"deleted file's +++ /dev/null header is excluded",
			"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			nil,
		},
		{
			"duplicate header lines dedupe",
			"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n" +
				"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -5 +5 @@\n-x\n+y\n",
			[]string{"a.go"},
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
