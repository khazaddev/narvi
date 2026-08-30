// Package shadowsentinel holds the values a suppressed write returns
// instead of a real result, so that every layer which can suppress
// returns the SAME ones.
//
// It exists because two layers can suppress independently and did not
// agree. §30.2 puts a transport gate underneath the typed port decorator
// as a fallback net, and each resolves the egress flag for itself. When
// the decorator resolved live and the transport resolved shadow -- a
// transient repo_settings read failure is enough, since that read fails
// closed -- the decorator called the live client, whose transport
// answered with a synthesized 200 carrying no fields at all. The caller
// parsed that into PRRef{Number: 0, URL: ""} and an empty commit SHA:
// zero values that no synthetic-value check recognises, because the
// checks look for these sentinels. Downstream lanes then ran against a
// pull request that does not exist.
//
// Keeping the values here, in a package both the adapter and the app
// layer may import, is what makes "suppressed" look the same whichever
// layer decided it. A caller checking for a synthetic result must never
// have to know which one did.
package shadowsentinel

// PRNumber is deliberately negative. Real GitHub pull request numbers are
// positive and monotonic, so this cannot collide with a real PR and
// cannot be mistaken for one anywhere it is printed, compared, or stored
// -- while still being an int, so nothing downstream has to special-case
// the type.
const PRNumber = -1

// CommitSHA is the right length and alphabet for a git object id, so
// anything that validates the shape still works, and spells what it is so
// nobody reads it as a real commit. It is not a valid hex SHA: the letters
// past 'f' make it impossible for it to name an object that could exist.
const CommitSHA = "shadowsuppressednotarealcommitsha0000000"

// URLScheme prefixes every synthetic URL. Not https, so a synthetic link
// cannot be followed by accident and cannot match a real GitHub URL in a
// string comparison.
const URLScheme = "shadow-suppressed://"
