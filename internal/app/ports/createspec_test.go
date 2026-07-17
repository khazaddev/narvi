package ports

import (
	"errors"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
)

func TestCreateSpec_Validate(t *testing.T) {
	t.Run("Gen matches SessionConfig.Gen", func(t *testing.T) {
		spec := CreateSpec{Gen: 3, SessionConfig: sessionconfig.SessionConfig{Gen: 3}}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("Gen diverges from SessionConfig.Gen", func(t *testing.T) {
		spec := CreateSpec{Gen: 3, SessionConfig: sessionconfig.SessionConfig{Gen: 1}}
		err := spec.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want a *GenMismatchError")
		}
		var target *GenMismatchError
		if !errors.As(err, &target) {
			t.Fatalf("Validate() error = %v, want *GenMismatchError", err)
		}
		if target.Gen != 3 || target.SessionConfigGen != 1 {
			t.Errorf("GenMismatchError = %+v, want Gen=3 SessionConfigGen=1", target)
		}
		for _, want := range []string{"3", "1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
			}
		}
	})
}
