package providercredential

// Provider is one of the 3 provider names §25.3 scopes this Step to --
// matches the Postgres provider_credential_provider ENUM
// (migrations/000056_provider_credentials.up.sql) verbatim.
type Provider string

// The 3 recognized Provider values -- see AllProviders below for the same
// set as a ranged-over slice.
const (
	ProviderGoogle    Provider = "google"
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

// AllProviders is every recognized Provider, in this file's own
// declaration order -- exported so a caller (e.g. the CP-side delivery
// endpoint resolving all 3 at once, or a test ranging exhaustively) never
// needs to hand-maintain a second list.
var AllProviders = []Provider{ProviderGoogle, ProviderAnthropic, ProviderOpenAI}

// IsValidProvider reports whether p is one of the 3 recognized Provider
// values.
func IsValidProvider(p Provider) bool {
	_, ok := envVarNames[p]
	return ok
}

// envVarNames is §25.3's own verbatim provider->env-var mapping: "provider
// API keys only, mapped provider->env-var name (GOOGLE_API_KEY/
// GOOGLE_GENERATIVE_AI_API_KEY/GEMINI_API_KEY for google, ANTHROPIC_API_KEY,
// OPENAI_API_KEY)".
//
// google deliberately maps to all THREE names, not just one. §25.2's own
// research (verified directly against the pinned OpenCode 1.17.15
// binary's live GET /provider catalog) records the google provider's own
// catalog entry as accepting all three interchangeably ("env:
// GOOGLE_API_KEY, GOOGLE_GENERATIVE_AI_API_KEY, GEMINI_API_KEY") but does
// NOT document which one OpenCode consults first when more than one is
// set, or that setting more than one is unsafe -- there is no documented
// precedence to defer to, and the 3 values this Step ever sets them to
// are always identical (the SAME resolved credential, never 3 different
// ones), so there is no possible conflict from setting all three: at
// worst, 2 of the 3 are redundant for whichever one OpenCode actually
// reads; at no cost is authentication ever LESS likely to succeed than
// picking just one and guessing wrong. This is the conservative choice
// given an underspecified precedence, not a guess disguised as one.
var envVarNames = map[Provider][]string{
	ProviderGoogle:    {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	ProviderAnthropic: {"ANTHROPIC_API_KEY"},
	ProviderOpenAI:    {"OPENAI_API_KEY"},
}

// EnvVarNames returns the OS environment variable name(s) provider's own
// resolved credential value must be written to before spawning `opencode
// serve` (internal/sandboxagent/opencodeproc.Spawn's own cmd.Env) -- nil
// for an unrecognized Provider (see IsValidProvider). Every name in the
// returned slice gets the SAME value; see this var's own doc comment
// above for why google returns 3 names rather than 1.
func EnvVarNames(p Provider) []string {
	names, ok := envVarNames[p]
	if !ok {
		return nil
	}
	// Defensive copy -- callers (e.g. spawn-time env-building code) may
	// append to what they get back; envVarNames' own backing arrays must
	// never be mutated out from under every other caller.
	out := make([]string, len(names))
	copy(out, names)
	return out
}
