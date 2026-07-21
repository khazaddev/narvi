// Package contractdrift implements the pure decision functions Step 27
// ("mocking + contract drift", §14.3) needs to detect "backend changed,
// contract didn't":
//
//   - Fingerprint (fingerprint.go): a deterministic hash of a repo's
//     contracts directory listing at one ref -- mirrors internal/domain/
//     imagebuild.Fingerprint's own sorted-map-keys, NUL-separated idiom
//     exactly (see that file's own doc comment), just against a different
//     input shape (a path -> git-blob-or-tree-sha map instead of a
//     repo-name -> commit-sha map).
//   - HasDrifted (drift.go): the truth table deciding whether a repo's own
//     current (RepoSHA, ContractsFingerprint) pair, compared against the
//     last recorded Snapshot for that same repo, signals drift (§14.3: "If
//     a real backend endpoint changes without the contract being
//     updated").
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness. This package does not know how a Fingerprint's input map was
// obtained (a GitHub Contents API call, in practice -- see internal/
// adapters/outbound/githubapi's own ResolveContractsFingerprint), and does
// not know how a Snapshot was persisted or looked up (internal/adapters/
// outbound/postgres's own ContractDriftStore) -- it only decides, given
// already-resolved values, what they mean.
package contractdrift
