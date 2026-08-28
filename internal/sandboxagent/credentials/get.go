package credentials

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Get implements the "get" op end to end (§5.2's protocol, doc.go):
//
//  1. Parses stdin via ParseDescriptor.
//  2. REFUSES anything but Protocol == "https" (§5.2: "scoped https+host
//     only") by returning (Credential{}, false, nil) -- no error, just
//     "nothing to offer", matching git's own credential-helper contract.
//  3. Checks the on-disk cache for a FRESH hit (not yet within
//     expiryBuffer of its own ExpiresAt, §5.2's "5-min expiry buffer") --
//     on a hit, returns the cached value WITHOUT ever calling client.Fetch.
//  4. On a cache miss OR a stale hit, calls client.Fetch to mint a fresh
//     credential, writes it back to the cache, and returns it.
//  5. On a Fetch failure, returns (Credential{}, false, err) -- NEVER
//     falls back to the stale/expired cached value, even though it was
//     technically already read in step 3. This is enforced BY
//     CONSTRUCTION, not by discipline: freshCacheHit (below) is the only
//     place that ever reads a cached Credential, and its own stale-or-miss
//     branch discards the value before returning -- there is no variable
//     in this function's scope that could hold a stale Credential past
//     that point for a later branch to accidentally return.
//
// forceReadOnly (§30.4(2)) is passed straight through to client.Fetch on
// a miss/stale hit -- true for a BootModeBuild boot (cmd/sandbox-agent/
// main.go's own runCredentialHelper), forcing the read-only substitution
// regardless of the repo's own egress mode: "a build only needs read".
func Get(
	ctx context.Context,
	r io.Reader,
	cache *Cache,
	client CredentialFetcher,
	sessionID, sandboxToken string,
	gen int,
	expiryBuffer time.Duration,
	forceReadOnly bool,
) (Credential, bool, error) {
	desc, err := ParseDescriptor(r)
	if err != nil {
		return Credential{}, false, err
	}

	if desc.Protocol != "https" {
		return Credential{}, false, nil
	}

	if cred, hit := freshCacheHit(cache, desc.Host, expiryBuffer); hit {
		return cred, true, nil
	}

	// Reaching here means either a genuine cache miss or a stale entry --
	// freshCacheHit's own local `cred` variable (whatever it read from
	// disk) is now out of scope forever. The ONLY Credential this
	// function can return from this point on is `fetched`, produced
	// fresh below.
	fetched, err := client.Fetch(ctx, sessionID, sandboxToken, gen, desc.Host, forceReadOnly)
	if err != nil {
		return Credential{}, false, fmt.Errorf("credentials: fetch credential for %s: %w", desc.Host, err)
	}

	if err := cache.Store(desc.Host, fetched); err != nil {
		return Credential{}, false, fmt.Errorf("credentials: store credential for %s: %w", desc.Host, err)
	}

	return fetched, true, nil
}

// freshCacheHit reports whether host has a cached credential that is not
// yet within expiryBuffer of its own ExpiresAt (§5.2's 5-min buffer). A
// cache read failure is treated the same as a miss (the safe direction:
// Get proceeds to Fetch a fresh credential rather than ever risking use of
// an unreadable/corrupt cache entry). Deliberately its own function, not
// inlined into Get, so the possibly-stale Credential cache.Load returns
// can never be reached from Get's Fetch-failure path -- see Get's own doc
// comment.
func freshCacheHit(cache *Cache, host string, expiryBuffer time.Duration) (Credential, bool) {
	cred, found, err := cache.Load(host)
	if err != nil || !found {
		return Credential{}, false
	}
	if time.Now().Add(expiryBuffer).After(cred.ExpiresAt) {
		return Credential{}, false // stale -- treated as a miss, never returned
	}
	return cred, true
}

// RunGet is the "get" subcommand entry point: calls Get, then writes
// `username=...\npassword=...\n` to stdout on a hit, or nothing at all
// when Get reports (_, false, nil) -- git's own "this helper has nothing
// to add" contract. A real error from Get itself propagates before
// anything is written. Once Get succeeds, though, the two stdout writes
// below are sequential: if the username line succeeds but the password
// line then fails (e.g. a broken pipe), RunGet still returns that error,
// but the username line has already reached stdout by that point -- a
// real, if low-impact, partial-output possibility (git treats the
// helper's nonzero exit as a hard failure regardless of what was already
// printed, and a username alone isn't the secret half of the pair).
func RunGet(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	cache *Cache,
	client CredentialFetcher,
	sessionID, sandboxToken string,
	gen int,
	expiryBuffer time.Duration,
	forceReadOnly bool,
) error {
	cred, ok, err := Get(ctx, stdin, cache, client, sessionID, sandboxToken, gen, expiryBuffer, forceReadOnly)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if _, err := fmt.Fprintf(stdout, "username=%s\n", cred.Username); err != nil {
		return fmt.Errorf("credentials: write username: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "password=%s\n", cred.Password); err != nil {
		return fmt.Errorf("credentials: write password: %w", err)
	}
	return nil
}

// RunStore is the "store" subcommand entry point: drains stdin and
// returns -- this package's own cache (already populated by Get/RunGet's
// own Fetch+Store path) is authoritative, so there is nothing extra to
// persist from git's own "store" invocation.
func RunStore(stdin io.Reader) error {
	if _, err := io.Copy(io.Discard, stdin); err != nil {
		return fmt.Errorf("credentials: drain stdin: %w", err)
	}
	return nil
}

// RunErase is the "erase" subcommand entry point: parses the Descriptor
// from stdin and purges that host's cache entry -- git calls erase when
// an offered credential FAILED auth, so the next get must not offer the
// same bad value again.
func RunErase(stdin io.Reader, cache *Cache) error {
	desc, err := ParseDescriptor(stdin)
	if err != nil {
		return err
	}
	if desc.Host == "" {
		return nil
	}
	return cache.Erase(desc.Host)
}
