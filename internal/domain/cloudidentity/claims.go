package cloudidentity

import "time"

// Claims is the pure shape of a minted cloud-identity OIDC token's JSON
// payload -- §27.3's own claim list, verbatim: `iss` = the issuer URL,
// `sub` = Sub's own stable per-Environment value, `aud` = per-binding,
// `exp` ~= 10 minutes, plus session-varying context "as additional custom
// claims for clouds whose condition languages can use them" -- SessionID/
// Gen/Repos/ProvenanceTag below, §27.3's own named examples
// ("session_id, gen, repo full names, provenance tag"). Deliberately NOT
// wire-contract-generated (contracts/rest/v1) -- this is an external,
// standard JWT claims payload arbitrary cloud STS implementations parse
// per RFC 7519/the OIDC core spec, not Narvi's own REST/WS wire contract
// between its own web UI and sandbox-agent (§6's own stated scope).
type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`

	// SessionID/Gen/Repos/ProvenanceTag ride as custom claims ONLY --
	// never folded into Subject, which stays the same fixed,
	// per-Environment value no matter which session or sandbox
	// generation mints against it (Sub's own doc comment, and this
	// package's own doc.go gap-3 discussion: this is exactly the property
	// Azure's exact-match trust config depends on).
	SessionID     string   `json:"session_id"`
	Gen           int64    `json:"gen"`
	Repos         []string `json:"repos,omitempty"`
	ProvenanceTag *string  `json:"provenance_tag,omitempty"`
}

// BuildClaimsInput bundles everything BuildClaims needs, already resolved
// by its caller (the minting handler, internal/adapters/inbound/httpapi/
// cloudidentitytoken.go) -- no I/O, no clock read, happens inside
// BuildClaims itself.
type BuildClaimsInput struct {
	Issuer        string
	EnvironmentID string
	Audience      string
	IssuedAt      time.Time
	Lifetime      time.Duration
	SessionID     string
	Gen           int64
	Repos         []string
	ProvenanceTag *string
}

// BuildClaims renders in's own fields into the exact Claims payload
// §27.3 specifies. Audience is carried through VERBATIM -- BuildClaims
// performs no audience-allowlist check of its own; confirming in.Audience
// is one some binding applicable to this session's Environment/global
// scope actually declares is the minting handler's own job, strictly
// BEFORE it ever calls this function (§27.3: "CP refuses any audience no
// binding for this session's Environment... declares -- it never mints
// arbitrary-audience tokens").
func BuildClaims(in BuildClaimsInput) Claims {
	return Claims{
		Issuer:        in.Issuer,
		Subject:       Sub(in.EnvironmentID),
		Audience:      in.Audience,
		IssuedAt:      in.IssuedAt.Unix(),
		Expiry:        in.IssuedAt.Add(in.Lifetime).Unix(),
		SessionID:     in.SessionID,
		Gen:           in.Gen,
		Repos:         in.Repos,
		ProvenanceTag: in.ProvenanceTag,
	}
}
