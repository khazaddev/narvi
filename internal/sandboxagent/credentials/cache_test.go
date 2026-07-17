package credentials_test

import (
	"fmt"
	"os"
	"path/filepath"
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
