package workflowengine

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/workflow"
)

// TestResolveLane covers every documented branch of resolveLane's own doc
// comment: a real classified (target, mode), every fail-open case (nil,
// empty, malformed JSON), and the §25.13 "unresolved lane still honors a
// garbage-target-but-real-plan-mode signal" carve-out LaneFor itself
// already implements.
func TestResolveLane(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want workflow.Lane
	}{
		{name: "nil column (never classified yet)", raw: nil, want: workflow.LaneRequest},
		{name: "empty column", raw: []byte{}, want: workflow.LaneRequest},
		{name: "malformed json", raw: []byte("{not json"), want: workflow.LaneRequest},
		{name: "empty json object", raw: []byte("{}"), want: workflow.LaneRequest},
		{
			name: "target=review, mode=build",
			raw:  []byte(`{"surface":"github","source":"classifier","target":"review","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LaneReview,
		},
		{
			name: "target=release (§15's release-vs-feature category)",
			raw:  []byte(`{"surface":"github","source":"classifier","target":"release","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LaneReview,
		},
		{
			name: "target=feature",
			raw:  []byte(`{"surface":"github","source":"classifier","target":"feature","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LaneReview,
		},
		{
			name: "target=request, mode=plan",
			raw:  []byte(`{"surface":"web","source":"explicit","target":"request","mode":"plan","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LanePlan,
		},
		{
			name: "target=request, mode=build",
			raw:  []byte(`{"surface":"web","source":"explicit","target":"request","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LaneRequest,
		},
		{
			name: "garbage target, mode=plan still honored (fail-open per LaneFor)",
			raw:  []byte(`{"surface":"web","source":"fallback","target":"","mode":"plan","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LanePlan,
		},
		{
			name: "garbage target and mode",
			raw:  []byte(`{"surface":"web","source":"fallback","target":"bogus","mode":"bogus","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create"}`),
			want: workflow.LaneRequest,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveLane(tc.raw); got != tc.want {
				t.Errorf("resolveLane(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRepoFullNameFromSessionRepos covers repoFullNameFromSessionRepos'
// own documented behavior: exactly one repo whose url parses as an
// owner/repo clone URL resolves; everything else (zero repos, more than
// one, malformed JSON, an unparseable URL) deliberately falls back to
// "no repo" (ok=false) rather than guessing.
func TestRepoFullNameFromSessionRepos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      []byte
		wantName string
		wantOK   bool
	}{
		{name: "nil column", raw: nil, wantOK: false},
		{name: "empty array", raw: []byte(`[]`), wantOK: false},
		{name: "malformed json", raw: []byte(`{not json`), wantOK: false},
		{
			name:     "single repo with a real owner/repo url",
			raw:      []byte(`[{"name":"narvi","url":"https://github.com/khazaddev/narvi.git","branch":null}]`),
			wantName: "khazaddev/narvi",
			wantOK:   true,
		},
		{
			name:     "single repo, url with no .git suffix",
			raw:      []byte(`[{"name":"narvi","url":"https://github.com/khazaddev/narvi","branch":"main"}]`),
			wantName: "khazaddev/narvi",
			wantOK:   true,
		},
		{
			name:   "single repo, url does not parse as owner/repo",
			raw:    []byte(`[{"name":"narvi","url":"https://github.com/","branch":null}]`),
			wantOK: false,
		},
		{
			name:   "two repos -- ambiguous, deliberately not resolved",
			raw:    []byte(`[{"name":"a","url":"https://github.com/acme/a.git","branch":null},{"name":"b","url":"https://github.com/acme/b.git","branch":null}]`),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotOK := repoFullNameFromSessionRepos(tc.raw)
			if gotOK != tc.wantOK {
				t.Fatalf("repoFullNameFromSessionRepos(%s) ok = %v, want %v", tc.raw, gotOK, tc.wantOK)
			}
			if gotOK && gotName != tc.wantName {
				t.Errorf("repoFullNameFromSessionRepos(%s) name = %q, want %q", tc.raw, gotName, tc.wantName)
			}
		})
	}
}
