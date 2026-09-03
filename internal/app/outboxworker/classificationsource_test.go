package outboxworker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/narvidev/narvi/internal/app/ports"
)

// TestClassification_CoversEveryKindDeclaredInSource reads the notification
// kinds out of ports/notifier.go itself, instead of comparing the
// classification table against a second list maintained by hand beside it.
//
// The hand-maintained list could not do what its own comment claimed. It
// said a new kind added to notifier.go without a matching entry here would
// fail -- but a kind added to notifier.go ALONE leaves both this list and
// the classification map untouched, so they still agree and the test stays
// green. Two copies of the same fact cannot check each other; only the
// source can.
//
// Go cannot enumerate a named string type's constants at runtime, so this
// parses the declaring file. That is the same technique internal/ops uses
// for the repo's other structural checks, and it fails for the event that
// actually happens: someone declares a twentieth kind.
func TestClassification_CoversEveryKindDeclaredInSource(t *testing.T) {
	declared := notificationKindsDeclaredInSource(t)
	if len(declared) < 19 {
		t.Fatalf("found only %d NotificationKind constants in the source; the parse is broken, not the table", len(declared))
	}

	for _, name := range declared {
		kind := ports.NotificationKind(name.value)
		if _, ok := notificationKindClassification[kind]; !ok {
			t.Errorf("%s (%q) is declared in internal/app/ports/notifier.go but has no egress classification.\n"+
				"    Every kind must be classified as one that reaches a customer or one that does not.\n"+
				"    Add it to notificationKindClassification and say which it is, and why, in its comment.",
				name.constName, name.value)
		}
	}

	// And the reverse: a classification for a kind nobody declares is dead
	// weight that outlives the thing it described.
	declaredValues := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredValues[d.value] = true
	}
	for kind := range notificationKindClassification {
		if !declaredValues[string(kind)] {
			t.Errorf("the classification table carries %q, which no NotificationKind constant declares any more", kind)
		}
	}
}

type declaredKind struct {
	constName string
	value     string
}

func notificationKindsDeclaredInSource(t *testing.T) []declaredKind {
	t.Helper()

	path := notifierSourcePath(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []declaredKind
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "NotificationKind" {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			out = append(out, declaredKind{constName: name.Name, value: lit.Value[1 : len(lit.Value)-1]})
		}
		return true
	})
	return out
}

func notifierSourcePath(t *testing.T) string {
	t.Helper()
	// Walk up to the module root, which is where internal/ lives.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "internal", "app", "ports", "notifier.go")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find internal/app/ports/notifier.go from the working directory")
		}
		dir = parent
	}
}
