package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Credential is what a successful fetch/cache-hit yields: an https basic
// auth username/password pair scoped to one host, plus its own expiry.
type Credential struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Cache is the on-disk credential cache: one JSON file per host inside
// Dir, every read/write/erase protected by an EXCLUSIVE flock
// (syscall.Flock/syscall.LOCK_EX, unlocked via syscall.LOCK_UN in a
// defer) held on that file for the duration of the operation (§5.2:
// "caches to disk with flock") -- this genuinely matters: two concurrent
// `git clone` processes hitting different repos on the SAME host could
// otherwise race on the same cache file.
//
// Dir and every file written into it carry restrictive permissions (dir
// 0700, file 0600): these are real secrets (a scm bearer/OAuth-style
// password), never world/group readable.
type Cache struct {
	Dir string
}

// cacheFileName derives a filesystem-safe filename from host: a
// hex-encoded SHA-256 of the lowercased host, NEVER the raw host string --
// a malicious/unexpected host value (containing "/", "..", null bytes, or
// other path-unsafe characters) must never be interpretable as a
// path-traversal or otherwise unsafe filename. Hashing collapses any such
// input to a fixed-shape, safe filename unconditionally.
func cacheFileName(host string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(host)))
	return hex.EncodeToString(sum[:]) + ".json"
}

// path ensures Dir exists, is OURS, and is 0700, then returns the full
// path to host's cache file inside it.
//
// The ownership and mode check is the point, and MkdirAll cannot make it:
// MkdirAll is a no-op when the directory already exists, whatever its
// owner or mode. Dir's default lives under /tmp, which is world-writable,
// and the agent runtime -- prompt-injectable, and running as a different
// uid precisely because it is not trusted (§30.5) -- can create a
// directory there. If it wins that race, root then opens credential files
// INSIDE a runtime-owned directory: the runtime cannot read a 0600 root
// file, but it owns the directory entry, so it can plant a symlink at the
// exact name and have root's own O_CREATE write the SCM token wherever it
// points.
//
// So a directory that is not ours, or not 0700, is a hard failure rather
// than something to repair: repairing it would race the same attacker,
// and a credential that cannot be cached safely must not be cached.
func (c *Cache) path(host string) (string, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return "", fmt.Errorf("credentials: create cache dir %s: %w", c.Dir, err)
	}
	if err := c.assertDirIsOurs(); err != nil {
		return "", err
	}
	return filepath.Join(c.Dir, cacheFileName(host)), nil
}

// assertDirIsOurs fails unless Dir is a real directory, owned by this
// process's uid, with no group or other permission bits.
func (c *Cache) assertDirIsOurs() error {
	info, err := os.Lstat(c.Dir)
	if err != nil {
		return fmt.Errorf("credentials: stat cache dir %s: %w", c.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("credentials: cache dir %s is not a directory", c.Dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("credentials: cannot read ownership of cache dir %s", c.Dir)
	}
	if uint64(stat.Uid) != uint64(os.Getuid()) {
		return fmt.Errorf("credentials: cache dir %s is owned by uid %d, not this process (uid %d); refusing to cache a credential in a directory another user controls", c.Dir, stat.Uid, os.Getuid())
	}
	// The WRITE bits, not every bit. Group- or other-writable is the hole:
	// it lets another uid create, replace or symlink an entry, which is
	// how a 0600 root-owned file still ends up written wherever an
	// attacker chose. Group- or other-READABLE is not the same problem --
	// the entries themselves are 0600, and the filenames are a hash of
	// the host, so a listing reveals nothing. Rejecting readability too
	// would have cost strictness nowhere and broken every caller whose
	// directory came from a 0755 parent.
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("credentials: cache dir %s has mode %#o; refusing to cache a credential in a directory group or others can write to, since another uid can then redirect this write with a symlink", c.Dir, perm)
	}
	return nil
}

// withFileLock opens (creating if absent, 0600) path, takes an exclusive
// flock on it for the duration of fn, then unlocks (deferred) --
// serializing concurrent access to this exact file across goroutines/
// processes.
func withFileLock(path string, fn func(f *os.File) error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("credentials: open cache file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("credentials: flock %s: %w", path, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn(f)
}

// Load reads host's cached Credential. Returns (Credential{}, false, nil)
// if the file is simply absent (checked BEFORE opening/creating it, so a
// genuine cache miss never has the side effect of creating an empty file
// on disk) or is present but empty (e.g. a freshly Erase'd entry).
func (c *Cache) Load(host string) (Credential, bool, error) {
	path, err := c.path(host)
	if err != nil {
		return Credential{}, false, err
	}

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return Credential{}, false, nil
	}

	var (
		cred  Credential
		found bool
	)
	lockErr := withFileLock(path, func(f *os.File) error {
		data, readErr := io.ReadAll(f)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		if len(data) == 0 {
			return nil
		}
		if unmarshalErr := json.Unmarshal(data, &cred); unmarshalErr != nil {
			return fmt.Errorf("decode %s: %w", path, unmarshalErr)
		}
		found = true
		return nil
	})
	if lockErr != nil {
		return Credential{}, false, lockErr
	}
	return cred, found, nil
}

// Store writes cred as host's cached Credential, replacing any prior
// content (truncate-then-write, all under the same held flock).
func (c *Cache) Store(host string, cred Credential) error {
	path, err := c.path(host)
	if err != nil {
		return err
	}

	return withFileLock(path, func(f *os.File) error {
		data, marshalErr := json.Marshal(cred)
		if marshalErr != nil {
			return fmt.Errorf("encode credential for %s: %w", host, marshalErr)
		}
		if truncErr := f.Truncate(0); truncErr != nil {
			return fmt.Errorf("truncate %s: %w", path, truncErr)
		}
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return fmt.Errorf("seek %s: %w", path, seekErr)
		}
		if _, writeErr := f.Write(data); writeErr != nil {
			return fmt.Errorf("write %s: %w", path, writeErr)
		}
		return nil
	})
}

// Erase purges host's cache entry (truncates its file to empty, under the
// same held flock) -- git calls erase when an offered credential FAILED
// auth, so a subsequent Load for the same host must report a miss, never
// the bad value again.
func (c *Cache) Erase(host string) error {
	path, err := c.path(host)
	if err != nil {
		return err
	}

	return withFileLock(path, func(f *os.File) error {
		if truncErr := f.Truncate(0); truncErr != nil {
			return fmt.Errorf("truncate %s: %w", path, truncErr)
		}
		return nil
	})
}

// PurgeAll removes every cached credential this Cache holds, by deleting
// its own Dir wholesale (not one file at a time -- Erase's own
// per-host-file granularity has no use here, since the whole point is
// leaving nothing behind at all) -- §30.4(2)'s own defense-in-depth for
// the image-build path: gitclone.CleanForImageBuild calls this
// immediately before a BootModeBuild snapshot, on top of (never instead
// of) forcing the read-only mint in the first place, so a bug in that
// primary fix would still leave no token on disk for the provider image
// to capture. Also called unconditionally at the START of every boot
// (cmd/sandbox-agent/main.go's own runBootSequence), regardless of mode --
// §30.4(3)'s own "a boot-time cache purge in all modes is also required,"
// though NOT load-bearing on its own there -- the load-bearing control is
// the purge at snapshot-MINT time, which cmd/sandbox-agent's own
// HandleSnapshot now performs before every mint request. See that call
// site for the residual window it does and does not close.
//
// A missing Dir is not an error (os.RemoveAll's own documented behavior)
// -- a sandbox that never minted any credential at all has nothing to
// purge, which is the common case for a fresh BootModeFresh/BootModeBuild
// boot's very first credential-helper invocation.
func (c *Cache) PurgeAll() error {
	// The CONTENTS, never the directory itself.
	//
	// Removing the directory looks equivalent and opens a real window:
	// the default Dir is under /tmp, which is world-writable, and the
	// agent runtime runs as a different uid on purpose. Between this
	// delete and the next Store's MkdirAll, the runtime can create
	// /tmp/narvi-credentials as its own -- and MkdirAll on an existing
	// directory is a no-op, whatever its owner. Root then writes
	// credential files into a directory the runtime controls, where a
	// planted symlink redirects the token somewhere the runtime can read.
	//
	// Leaving the root-owned 0700 directory in place means there is no
	// moment at which that name is unclaimed. A missing directory is
	// still not an error: a sandbox that never minted anything has
	// nothing to purge, which is the common fresh-boot case.
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("credentials: read cache dir %s: %w", c.Dir, err)
	}
	for _, entry := range entries {
		if rmErr := os.RemoveAll(filepath.Join(c.Dir, entry.Name())); rmErr != nil {
			return fmt.Errorf("credentials: purge cache entry %s: %w", entry.Name(), rmErr)
		}
	}
	return nil
}
