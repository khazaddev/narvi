package sandboxboot

import "fmt"

// BootMode identifies which of the four sandbox boot modes (§6.4) a
// sandbox-agent process is running under -- delivered to the sandbox as
// the raw string value of the NARVI_BOOT_MODE env var (§6.4;
// contracts/gen/go/sessionconfig's generated SessionConfigBootMode enum
// happens to carry the same four string values, but this package defines
// its own type rather than importing that generated package -- see
// doc.go).
type BootMode string

// The four boot modes §6.4 defines. Values are wire values -- they are
// exactly what NARVI_BOOT_MODE will literally contain -- so they must
// never be renamed independently of the plan.
const (
	// BootModeBuild is image-build time: setup.sh runs and a failure is
	// fatal; start.sh does not run at all.
	BootModeBuild BootMode = "build"
	// BootModeFresh is a brand-new sandbox with no prior image/snapshot to
	// build on: both setup.sh (non-fatal) and start.sh (primary-fatal,
	// secondary-warn) run.
	BootModeFresh BootMode = "fresh"
	// BootModeRepoImage is a sandbox booting from a prebuilt repo image:
	// setup.sh already ran at build time and does not run again; start.sh
	// runs (primary-fatal, secondary-warn).
	BootModeRepoImage BootMode = "repo_image"
	// BootModeSnapshotRestore is a sandbox restored from a snapshot:
	// setup.sh does not run (already baked in); start.sh runs
	// (primary-fatal, secondary-warn).
	BootModeSnapshotRestore BootMode = "snapshot_restore"
)

// validBootModes is the closed set ParseBootMode accepts.
var validBootModes = map[BootMode]bool{
	BootModeBuild:           true,
	BootModeFresh:           true,
	BootModeRepoImage:       true,
	BootModeSnapshotRestore: true,
}

// InvalidBootModeError is returned by ParseBootMode when raw does not
// case-sensitively match one of the four §6.4 boot mode values -- INCLUDING
// the empty string. §6.4 gives no default boot mode, so an unset env var
// is exactly as invalid as any other unrecognized value; ParseBootMode
// never silently falls back to one mode. Follows the same named-error
// convention as platform/config.go's InvalidStageError/
// InvalidLogLevelError.
type InvalidBootModeError struct {
	Value string
}

func (e *InvalidBootModeError) Error() string {
	return fmt.Sprintf(
		"sandboxboot: invalid boot mode %q: must be one of %q, %q, %q, %q",
		e.Value, BootModeBuild, BootModeFresh, BootModeRepoImage, BootModeSnapshotRestore,
	)
}

// ParseBootMode validates raw (e.g. the NARVI_BOOT_MODE env var's literal
// value) against exactly the four §6.4 boot modes -- case-sensitive, no
// trimming or other normalizing beyond the exact-match comparison itself.
// An unset/empty or unrecognized value returns a *InvalidBootModeError,
// never a silent default.
func ParseBootMode(raw string) (BootMode, error) {
	mode := BootMode(raw)
	if !validBootModes[mode] {
		return "", &InvalidBootModeError{Value: raw}
	}
	return mode, nil
}
