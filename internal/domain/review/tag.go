package review

// Tag is one member of BlastRadius's fixed, closed vocabulary of
// system-surface areas a PR's diff touches. See doc.go's design call #3
// for why these eight and not some other set — this package's own
// invention, grounded in §21.2's named sensitive-path examples (auth,
// migrations, contracts) plus five siblings of comparably broad blast
// radius. Validating a caller-supplied tag list against this vocabulary
// (e.g. rejecting an unrecognized tag string from a model tool call) is
// the job of whichever Step accepts that external input (§8.2),
// not this package — nothing here consumes BlastRadius computationally,
// so no validity-checking function is exported (doc.go: "keep everything
// else unexported").
type Tag string

// The eight Tag values. Extending this list is a deliberate, reviewed
// change to this one place — never something a consumer infers ad hoc.
const (
	// TagAuth is authentication/authorization code — named explicitly by
	// §21.2 as a default sensitive path.
	TagAuth Tag = "auth"
	// TagMigrations is a database schema migration — named explicitly by
	// §21.2 as a default sensitive path.
	TagMigrations Tag = "migrations"
	// TagContracts is a change under /contracts, the wire-schema source
	// of truth (§6) — named explicitly by §21.2 as a default sensitive
	// path.
	TagContracts Tag = "contracts"
	// TagSecrets is secrets/credential handling — the natural sibling of
	// TagAuth; a defect here has comparably broad blast radius.
	TagSecrets Tag = "secrets"
	// TagInfra is deployment/infrastructure/CI configuration (images,
	// cluster manifests, workflow definitions).
	TagInfra Tag = "infra"
	// TagPublicAPI is externally-facing API surface (REST/WS endpoints
	// third parties or the frontend depend on).
	TagPublicAPI Tag = "public_api"
	// TagDataLayer is persistence/store code, distinct from TagMigrations
	// (schema changes) — query logic, data-access invariants.
	TagDataLayer Tag = "data_layer"
	// TagDependencies is a third-party dependency version change — a
	// well-known distinct supply-chain risk category.
	TagDependencies Tag = "dependencies"
)
