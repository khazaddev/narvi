package ops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuideCommand_Validate(t *testing.T) {
	tests := []struct {
		name    string
		c       GuideCommand
		wantErr bool
	}{
		{"valid route", GuideCommand{Name: "Create a session", Route: "POST /api/sessions"}, false},
		{"valid classifier", GuideCommand{Name: "PR mention", Classifier: &ClassifierRef{Surface: "github", Target: "review"}}, false},
		{"missing name", GuideCommand{Route: "POST /api/sessions"}, true},
		{"neither route nor classifier", GuideCommand{Name: "x"}, true},
		{"both route and classifier", GuideCommand{Name: "x", Route: "POST /api/sessions", Classifier: &ClassifierRef{Surface: "web"}}, true},
		{"malformed route: no method", GuideCommand{Name: "x", Route: "/api/sessions"}, true},
		{"malformed route: lowercase method", GuideCommand{Name: "x", Route: "post /api/sessions"}, true},
		{"malformed route: unknown method", GuideCommand{Name: "x", Route: "FETCH /api/sessions"}, true},
		{"malformed route: path missing leading slash", GuideCommand{Name: "x", Route: "GET api/sessions"}, true},
		{"classifier missing surface", GuideCommand{Name: "x", Classifier: &ClassifierRef{Target: "review"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadGuides(t *testing.T) {
	t.Run("valid guide with route and classifier commands", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\nSome prose.\n\n```json narvi-command\n{\"name\": \"Create a session\", \"route\": \"POST /api/sessions\"}\n```\n\nMore prose.\n\n```json narvi-command\n{\"name\": \"PR mention\", \"classifier\": {\"surface\": \"github\", \"target\": \"review\"}}\n```\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		// README.md must be ignored entirely, even if malformed.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a guide, no heading, no commands"), 0o644); err != nil {
			t.Fatalf("write README.md: %v", err)
		}

		guides, err := LoadGuides(dir)
		if err != nil {
			t.Fatalf("LoadGuides: %v", err)
		}
		if len(guides) != 1 {
			t.Fatalf("len(guides) = %d, want 1 (README.md must be excluded)", len(guides))
		}
		g := guides[0]
		if g.Surface != "web" {
			t.Errorf("Surface = %q, want \"web\"", g.Surface)
		}
		if g.Title != "Web Guide" {
			t.Errorf("Title = %q, want \"Web Guide\"", g.Title)
		}
		if len(g.Commands) != 2 {
			t.Fatalf("len(Commands) = %d, want 2", len(g.Commands))
		}
		if g.Commands[0].Route != "POST /api/sessions" {
			t.Errorf("Commands[0].Route = %q", g.Commands[0].Route)
		}
		if g.Commands[1].Classifier == nil || g.Commands[1].Classifier.Surface != "github" {
			t.Errorf("Commands[1].Classifier = %+v", g.Commands[1].Classifier)
		}
	})

	t.Run("missing heading fails closed", func(t *testing.T) {
		dir := t.TempDir()
		content := "No heading here.\n\n```json narvi-command\n{\"name\": \"x\", \"route\": \"GET /health\"}\n```\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for a file with no top-level heading")
		}
	})

	t.Run("zero command blocks fails closed", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\nNo command blocks at all.\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for a file with zero narvi-command blocks")
		}
	})

	t.Run("unterminated fence fails closed, never silently skipped", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\n```json narvi-command\n{\"name\": \"x\", \"route\": \"GET /health\"}\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for an unterminated fence")
		}
	})

	t.Run("malformed JSON inside a fence fails closed", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\n```json narvi-command\n{not valid json at all\n```\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for malformed embedded JSON")
		}
	})

	t.Run("command failing Validate fails closed", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\n```json narvi-command\n{\"name\": \"\", \"route\": \"GET /health\"}\n```\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for a command missing its required name")
		}
	})

	t.Run("unknown JSON field fails closed", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Web Guide\n\n```json narvi-command\n{\"name\": \"x\", \"route\": \"GET /health\", \"typo\": true}\n```\n"
		if err := os.WriteFile(filepath.Join(dir, "web.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write web.md: %v", err)
		}
		if _, err := LoadGuides(dir); err == nil {
			t.Error("LoadGuides() error = nil, want an error for an unrecognized field in a narvi-command block")
		}
	})
}
