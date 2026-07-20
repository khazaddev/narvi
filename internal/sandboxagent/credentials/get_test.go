package credentials_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

const testExpiryBuffer = 5 * time.Minute

// testGen is the fixed sandbox gen every Get/RunGet call in this file
// threads through -- its exact value is never asserted on by any test here
// (that's cpclient_test.go's own job, against a real httptest.Server); it
// only needs to be A value, so Get/RunGet compile and run with the new
// gen parameter.
const testGen = 7

// fakeFetcher is a CredentialFetcher test double: it records every Fetch
// call, returns fetchResult/fetchErr, and (via failOnFetch) can fail the
// test outright if Fetch is ever invoked -- used to prove a cache hit
// makes ZERO calls to Fetch.
type fakeFetcher struct {
	t           *testing.T
	failOnFetch bool
	calls       int
	fetchResult credentials.Credential
	fetchErr    error
}

func (f *fakeFetcher) Fetch(_ context.Context, sessionID, sandboxToken string, gen int, host string) (credentials.Credential, error) {
	f.calls++
	if f.failOnFetch {
		f.t.Fatalf("Fetch(sessionID=%q, sandboxToken=%q, gen=%d, host=%q) called, want it never called", sessionID, sandboxToken, gen, host)
	}
	return f.fetchResult, f.fetchErr
}

func descriptorReader(protocol, host string) *strings.Reader {
	return strings.NewReader(fmt.Sprintf("protocol=%s\nhost=%s\n\n", protocol, host))
}

// TestGet_NonHTTPSProtocolRefusesSilently proves §5.2's "scoped https+host
// only": a non-https protocol yields (_, false, nil) -- no error, no
// output, git's own "nothing to offer" contract -- and never touches the
// fetcher at all.
func TestGet_NonHTTPSProtocolRefusesSilently(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	fetcher := &fakeFetcher{t: t, failOnFetch: true}

	cred, ok, err := credentials.Get(
		context.Background(), descriptorReader("ssh", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if ok {
		t.Errorf("Get() ok = true, want false for a non-https protocol; cred = %+v", cred)
	}
}

// TestGet_CacheHitNeverCallsFetch proves a fresh cache hit is returned
// as-is, with ZERO calls to CredentialFetcher.Fetch.
func TestGet_CacheHitNeverCallsFetch(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	want := credentials.Credential{
		Username:  "cached-user",
		Password:  "cached-pass",
		ExpiresAt: time.Now().Add(time.Hour), // comfortably outside the 5-min buffer
	}
	if err := cache.Store("example.com", want); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	fetcher := &fakeFetcher{t: t, failOnFetch: true}

	got, ok, err := credentials.Get(
		context.Background(), descriptorReader("https", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true for a fresh cache hit")
	}
	if got.Username != want.Username || got.Password != want.Password {
		t.Errorf("Get() = %+v, want the cached %+v", got, want)
	}
	if fetcher.calls != 0 {
		t.Errorf("Fetch called %d times, want 0 for a fresh cache hit", fetcher.calls)
	}
}

// TestGet_CacheMissCallsFetchAndStores proves a genuine cache miss calls
// Fetch exactly once and writes the result back to the cache.
func TestGet_CacheMissCallsFetchAndStores(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	fresh := credentials.Credential{
		Username:  "fresh-user",
		Password:  "fresh-pass",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	fetcher := &fakeFetcher{t: t, fetchResult: fresh}

	got, ok, err := credentials.Get(
		context.Background(), descriptorReader("https", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.Username != fresh.Username {
		t.Errorf("Get() = %+v, want %+v", got, fresh)
	}
	if fetcher.calls != 1 {
		t.Errorf("Fetch called %d times, want exactly 1 for a cache miss", fetcher.calls)
	}

	cached, found, err := cache.Load("example.com")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found || cached.Username != fresh.Username {
		t.Errorf("cache.Load() after Get() = (%+v, %v), want the freshly fetched credential stored", cached, found)
	}
}

// TestGet_StaleWithinExpiryBufferCallsFetch proves a cache entry that is
// NOT literally past its own ExpiresAt yet, but IS within expiryBuffer of
// it, is still treated as stale (a miss) and triggers a Fetch -- the §5.2
// "5-min expiry buffer" behavior, distinct from bare literal expiry.
func TestGet_StaleWithinExpiryBufferCallsFetch(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	// Expires in 1 minute -- inside the 5-minute buffer, so "needs
	// refresh" per now.Add(buffer).After(expiresAt), even though
	// ExpiresAt itself is still in the future.
	almostExpired := credentials.Credential{
		Username:  "old-user",
		Password:  "old-pass",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := cache.Store("example.com", almostExpired); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	fresh := credentials.Credential{Username: "new-user", Password: "new-pass", ExpiresAt: time.Now().Add(time.Hour)}
	fetcher := &fakeFetcher{t: t, fetchResult: fresh}

	got, ok, err := credentials.Get(
		context.Background(), descriptorReader("https", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if fetcher.calls != 1 {
		t.Errorf("Fetch called %d times, want exactly 1 -- an almost-expired entry must be treated as stale", fetcher.calls)
	}
	if got.Username != "new-user" {
		t.Errorf("Get().Username = %q, want the freshly fetched %q, not the stale cached value", got.Username, "new-user")
	}
}

// TestGet_FetchFailureNeverReturnsStaleCredential is the single most
// important test in this whole Step (§5.2: "Never fall back to stale
// cache"). A stale/expired credential IS present in the cache; Fetch then
// fails; Get must return an error, and the stale credential must NEVER be
// what comes back -- not via the returned Credential, not via ok=true.
func TestGet_FetchFailureNeverReturnsStaleCredential(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	stale := credentials.Credential{
		Username:  "STALE-USER-MUST-NEVER-BE-RETURNED",
		Password:  "STALE-PASSWORD-MUST-NEVER-BE-RETURNED",
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
	}
	if err := cache.Store("example.com", stale); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	fetchErr := errors.New("cp is down")
	fetcher := &fakeFetcher{t: t, fetchErr: fetchErr}

	got, ok, err := credentials.Get(
		context.Background(), descriptorReader("https", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err == nil {
		t.Fatal("Get() error = nil, want an error when Fetch fails against a stale cache")
	}
	if ok {
		t.Error("Get() ok = true, want false on a Fetch failure")
	}
	if got.Username == stale.Username || got.Password == stale.Password {
		t.Fatalf("Get() = %+v, THE STALE CREDENTIAL LEAKED OUT despite a Fetch failure -- this must never happen", got)
	}
	if got != (credentials.Credential{}) {
		t.Errorf("Get() Credential = %+v, want the zero value on error", got)
	}
	if fetcher.calls != 1 {
		t.Errorf("Fetch called %d times, want exactly 1", fetcher.calls)
	}
}

func TestRunGet_WritesUsernamePasswordOnHit(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	fresh := credentials.Credential{Username: "u1", Password: "p1", ExpiresAt: time.Now().Add(time.Hour)}
	fetcher := &fakeFetcher{t: t, fetchResult: fresh}

	var stdout bytes.Buffer
	err := credentials.RunGet(
		context.Background(), descriptorReader("https", "example.com"), &stdout, cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("RunGet() error = %v, want nil", err)
	}

	want := "username=u1\npassword=p1\n"
	if stdout.String() != want {
		t.Errorf("RunGet() stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunGet_WritesNothingWhenNotOK(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	fetcher := &fakeFetcher{t: t, failOnFetch: true}

	var stdout bytes.Buffer
	err := credentials.RunGet(
		context.Background(), descriptorReader("ssh", "example.com"), &stdout, cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("RunGet() error = %v, want nil", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("RunGet() stdout = %q, want empty output", stdout.String())
	}
}

// TestRunErase_PurgesEntryAndForcesNextGetToFetch proves erase actually
// takes effect: after RunErase, a subsequent RunGet for the same host is a
// cache miss (calls Fetch again), not a stale hit.
func TestRunErase_PurgesEntryAndForcesNextGetToFetch(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	original := credentials.Credential{Username: "bad-user", Password: "bad-pass", ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.Store("example.com", original); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	eraseInput := strings.NewReader("protocol=https\nhost=example.com\n\n")
	if err := credentials.RunErase(eraseInput, cache); err != nil {
		t.Fatalf("RunErase() error = %v", err)
	}

	if _, found, err := cache.Load("example.com"); err != nil || found {
		t.Fatalf("cache.Load() after RunErase = (found=%v, err=%v), want (false, nil)", found, err)
	}

	fresh := credentials.Credential{Username: "good-user", Password: "good-pass", ExpiresAt: time.Now().Add(time.Hour)}
	fetcher := &fakeFetcher{t: t, fetchResult: fresh}

	got, ok, err := credentials.Get(
		context.Background(), descriptorReader("https", "example.com"), cache, fetcher,
		"sess-1", "tok", testGen, testExpiryBuffer,
	)
	if err != nil {
		t.Fatalf("Get() after erase error = %v, want nil", err)
	}
	if !ok || got.Username != "good-user" {
		t.Errorf("Get() after erase = (%+v, %v), want the freshly fetched good-user", got, ok)
	}
	if fetcher.calls != 1 {
		t.Errorf("Fetch called %d times after erase, want exactly 1 (erase must force a real miss)", fetcher.calls)
	}
}

func TestRunStore_DrainsStdinAndReturnsNil(t *testing.T) {
	t.Parallel()

	input := strings.NewReader("protocol=https\nhost=example.com\n\n")
	if err := credentials.RunStore(input); err != nil {
		t.Errorf("RunStore() error = %v, want nil", err)
	}
}

// erroringReader always fails, to exercise RunStore/RunErase/ParseDescriptor's
// own I/O-failure paths without needing a real broken pipe.
type erroringReader struct{}

func (erroringReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated read failure")
}

func TestRunStore_PropagatesReadError(t *testing.T) {
	t.Parallel()

	if err := credentials.RunStore(erroringReader{}); err == nil {
		t.Error("RunStore() error = nil, want an error when stdin itself fails to read")
	}
}

func TestRunErase_PropagatesParseError(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	if err := credentials.RunErase(erroringReader{}, cache); err == nil {
		t.Error("RunErase() error = nil, want an error when stdin itself fails to read")
	}
}

// TestRunErase_EmptyHostIsANoOp proves a Descriptor with no host at all
// (git would never actually send this, but RunErase must still behave
// safely) purges nothing and returns nil.
func TestRunErase_EmptyHostIsANoOp(t *testing.T) {
	t.Parallel()

	cache := &credentials.Cache{Dir: filepath.Join(t.TempDir(), "cache")}
	if err := credentials.RunErase(strings.NewReader("\n"), cache); err != nil {
		t.Errorf("RunErase() error = %v, want nil for a descriptor with no host", err)
	}
}
