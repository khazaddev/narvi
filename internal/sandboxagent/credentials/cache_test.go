package credentials_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

func TestCache_LoadAbsentIsMissNotError(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}

	cred, found, err := cache.Load("example.com")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if found {
		t.Errorf("Load() found = true, want false for an absent entry; cred = %+v", cred)
	}
}

func TestCache_StoreThenLoadRoundTrip(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	want := credentials.Credential{
		Username:  "user1",
		Password:  "pass1",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}

	if err := cache.Store("example.com", want); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	got, found, err := cache.Load("example.com")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found {
		t.Fatal("Load() found = false, want true")
	}
	if got.Username != want.Username || got.Password != want.Password || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestCache_StoreOverwritesPriorEntry(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	first := credentials.Credential{Username: "old", Password: "old-pass", ExpiresAt: time.Now().Add(time.Hour)}
	second := credentials.Credential{Username: "new", Password: "new-pass", ExpiresAt: time.Now().Add(2 * time.Hour)}

	if err := cache.Store("example.com", first); err != nil {
		t.Fatalf("Store(first) error = %v", err)
	}
	if err := cache.Store("example.com", second); err != nil {
		t.Fatalf("Store(second) error = %v", err)
	}

	got, found, err := cache.Load("example.com")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found {
		t.Fatal("Load() found = false, want true")
	}
	if got.Username != "new" {
		t.Errorf("Load().Username = %q, want %q (the overwritten value)", got.Username, "new")
	}
}

func TestCache_EraseRemovesEntry(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	cred := credentials.Credential{Username: "u", Password: "p", ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.Store("example.com", cred); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if err := cache.Erase("example.com"); err != nil {
		t.Fatalf("Erase() error = %v", err)
	}

	_, found, err := cache.Load("example.com")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if found {
		t.Error("Load() found = true after Erase(), want false")
	}
}

// TestCache_EraseOfAbsentEntryIsNotAnError proves Erase on a host with no
// cached entry at all is a harmless no-op, not an error.
func TestCache_EraseOfAbsentEntryIsNotAnError(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	if err := cache.Erase("never-cached.example.com"); err != nil {
		t.Errorf("Erase() error = %v, want nil for an entry that was never cached", err)
	}
}

// TestCache_DirAndFilePermissions proves the cache directory and every
// file inside it carry exactly 0700/0600 -- these are real secrets, never
// world/group readable.
func TestCache_DirAndFilePermissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "cache")
	cache := &credentials.Cache{Dir: dir}
	cred := credentials.Credential{Username: "u", Password: "p", ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.Store("example.com", cred); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("cache dir mode = %o, want 0700", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want exactly 1 cache file", len(entries))
	}

	fileInfo, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file mode = %o, want 0600", perm)
	}
}

// TestCache_MaliciousHostNeverEscapesDir constructs host values containing
// "/", "..", and null bytes directly and confirms the resulting cache
// file always stays a flat file directly inside Dir -- cacheFileName's
// SHA-256 hashing must make a path-traversal write structurally
// impossible, not merely unlikely.
func TestCache_MaliciousHostNeverEscapesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	cache := &credentials.Cache{Dir: dir}

	maliciousHosts := []string{
		"../../../etc/passwd",
		"/etc/passwd",
		"host/with/slashes",
		"host\x00with\x00nulls",
		"..",
		".",
		"....//....//etc/passwd",
	}

	for i, host := range maliciousHosts {
		host := host
		t.Run(fmt.Sprintf("host_%d", i), func(t *testing.T) {
			cred := credentials.Credential{Username: fmt.Sprintf("user-%d", i), Password: "p", ExpiresAt: time.Now().Add(time.Hour)}
			if err := cache.Store(host, cred); err != nil {
				t.Fatalf("Store(%q) error = %v", host, err)
			}

			got, found, err := cache.Load(host)
			if err != nil {
				t.Fatalf("Load(%q) error = %v", host, err)
			}
			if !found || got.Username != cred.Username {
				t.Errorf("Load(%q) = (%+v, %v), want the stored credential back", host, got, found)
			}
		})
	}

	// Every entry created above must be a flat file directly inside dir
	// -- no subdirectories (which would indicate a "/"-containing host
	// leaked into the filename) and no path escaping dir's own boundary
	// (os.ReadDir itself only ever sees dir's own immediate contents, so
	// any entry it lists at all is already proof it landed inside dir).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("found a directory entry %q inside the cache dir -- a malicious host escaped into the filename", e.Name())
		}
		if filepath.Dir(filepath.Join(dir, e.Name())) != dir {
			t.Errorf("entry %q does not resolve directly inside %q", e.Name(), dir)
		}
	}
}

// TestCache_FlockSerializesConcurrentAccess spins up many concurrent
// Store+Load pairs against the SAME host's cache file (via
// errgroup.Group.Go, never a bare `go` statement — §11/nakedgoroutine
// grants no test exemption) and asserts every one succeeds with a
// decodable result -- proving the exclusive flock actually serializes
// access rather than merely compiling.
func TestCache_FlockSerializesConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	const n = 25
	const host = "concurrent.example.com"

	var group errgroup.Group
	for i := 0; i < n; i++ {
		i := i
		group.Go(func() error {
			cred := credentials.Credential{
				Username:  fmt.Sprintf("user-%d", i),
				Password:  fmt.Sprintf("pass-%d", i),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			if err := cache.Store(host, cred); err != nil {
				return fmt.Errorf("Store(%d): %w", i, err)
			}
			if _, _, err := cache.Load(host); err != nil {
				return fmt.Errorf("Load(%d): %w", i, err)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("concurrent Store/Load: %v", err)
	}

	// A final Load must still decode cleanly into a fully-populated
	// Credential -- proof no torn/corrupted write ever landed on disk
	// despite N goroutines racing on the exact same cache file.
	final, found, err := cache.Load(host)
	if err != nil {
		t.Fatalf("final Load() error = %v, want nil (no corruption)", err)
	}
	if !found {
		t.Fatal("final Load() found = false, want true")
	}
	if final.Username == "" || final.Password == "" {
		t.Errorf("final Load() = %+v, looks corrupted (empty fields)", final)
	}
}

// TestCache_PurgeAllRemovesEverything proves PurgeAll leaves no cached
// credential behind at all -- multiple hosts, all gone -- and that the
// Cache is still usable afterward (a fresh Store/Load round trip
// succeeds, proving PurgeAll doesn't leave the directory in a state a
// later credential-helper invocation couldn't recover from).
func TestCache_PurgeAllEmptiesTheDirectoryWithoutUnclaimingIt(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "cache")
	cache := &credentials.Cache{Dir: dir}

	cred := credentials.Credential{Username: "u", Password: "p", ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.Store("github.com", cred); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := cache.Store("gitlab.com", cred); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if err := cache.PurgeAll(); err != nil {
		t.Fatalf("PurgeAll() error = %v, want nil", err)
	}

	// The guarantee is "no credential remains", NOT "the directory is
	// gone" -- and this assertion used to pin the second, which is the
	// behaviour that opened a real hole. The default Dir lives under
	// /tmp, which is world-writable, and the agent runtime runs as a
	// different uid on purpose. A purge that removed the directory left
	// its NAME unclaimed, so the runtime could recreate it as its own
	// before the next Store; MkdirAll on an existing directory is a
	// no-op whatever its owner, so root then wrote credential files into
	// a directory the runtime controlled, where a planted symlink
	// redirects the token somewhere readable.
	//
	// So: the directory must SURVIVE, still ours and still 0700, and be
	// empty.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("cache dir %s does not survive PurgeAll: %v -- leaving its name unclaimed under a world-writable parent lets another uid claim it", dir, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("cache dir mode = %#o after PurgeAll, want no group/other bits", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("cache dir holds %d entries after PurgeAll, want 0", len(entries))
	}

	for _, host := range []string{"github.com", "gitlab.com"} {
		if _, found, err := cache.Load(host); err != nil || found {
			t.Errorf("cache.Load(%q) after PurgeAll = (found=%v, err=%v), want (false, nil)", host, found, err)
		}
	}

	// Still usable afterward.
	if err := cache.Store("github.com", cred); err != nil {
		t.Fatalf("Store() after PurgeAll error = %v, want nil (the cache must still be usable)", err)
	}
	got, found, err := cache.Load("github.com")
	if err != nil || !found || got.Username != "u" {
		t.Errorf("Load() after PurgeAll+Store = (%+v, %v, %v), want the freshly stored credential", got, found, err)
	}
}

// TestCache_PurgeAllOfAbsentDirIsNotAnError proves purging a Cache whose
// Dir was never created (no credential ever minted yet) is a safe no-op,
// not an error -- the common case for the very first credential-helper
// invocation of a fresh boot.
func TestCache_PurgeAllOfAbsentDirIsNotAnError(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "never-created")}
	if err := cache.PurgeAll(); err != nil {
		t.Errorf("PurgeAll() error = %v, want nil for a Dir that was never created", err)
	}
}

// TestCache_RefusesADirectoryAnotherUserCouldControl is the second half
// of closing the /tmp hijack, and it covers the case PurgeAll's own fix
// does not: the directory never being ours in the first place.
//
// MkdirAll is a no-op on an existing directory, whatever its owner or
// mode, so a cache directory pre-created by someone else is adopted
// silently. Under a world-writable parent that someone is the agent
// runtime — a different uid on purpose, and prompt-injectable. It cannot
// read a 0600 root file, but owning the directory entry is enough: it
// plants a symlink at the credential's exact name and root's own O_CREATE
// writes the SCM token wherever it points.
//
// A group- or world-accessible directory is therefore a hard failure, not
// something to repair in place — repairing races the same attacker.
func TestCache_RefusesADirectoryAnotherUserCouldControl(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("pre-create a permissive cache dir: %v", err)
	}
	// os.MkdirAll applies the umask, so set the hostile mode explicitly.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cache := &credentials.Cache{Dir: dir}
	err := cache.Store("github.com", credentials.Credential{Username: "x-access-token", Password: "ghs_must_not_be_written_here"})
	if err == nil {
		t.Fatal("Store() error = nil into a world-writable cache dir; another uid owning that directory can redirect root's own write with a symlink")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("Store() error = %v, want it to name the directory's mode as the reason", err)
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the refused Store still wrote %d entries; nothing may be written before the directory is trusted", len(entries))
	}
}
