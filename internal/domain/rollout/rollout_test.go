package rollout

import "testing"

// TestDecide_ModeOpenAdmitsUnconditionally pins §32's own "byte-for-byte
// no-op" property at the pure-decision layer: ModeOpen admits regardless
// of what repos contains -- including a repo explicitly marked
// unenrolled, and a nil slice (both callers' own "never even read
// repo_settings in this mode" shortcut).
func TestDecide_ModeOpenAdmitsUnconditionally(t *testing.T) {
	tests := []struct {
		name  string
		repos []RepoAdmission
	}{
		{name: "nil repos", repos: nil},
		{name: "empty repos", repos: []RepoAdmission{}},
		{name: "one enrolled repo", repos: []RepoAdmission{{FullName: "acme/widgets", Enrolled: true}}},
		{name: "one UNENROLLED repo -- still admitted in open mode", repos: []RepoAdmission{{FullName: "acme/widgets", Enrolled: false}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(ModeOpen, tt.repos)
			if !got.Admitted {
				t.Fatalf("Decide(ModeOpen, %+v) = %+v, want Admitted=true", tt.repos, got)
			}
			if got.Reason != ReasonNone {
				t.Fatalf("Decide(ModeOpen, %+v).Reason = %q, want %q", tt.repos, got.Reason, ReasonNone)
			}
		})
	}
}

// TestDecide_UnrecognizedModeAdmitsUnconditionally pins that Decide's own
// gate is "mode != ModeCohort", not "mode == ModeOpen" -- any value other
// than the one recognized enrollment-enforcing mode admits, mirroring
// platform.Load's own fail-fast validation being the ONLY place an
// invalid mode value can ever originate (Decide itself never rejects a
// mode value; that is Load's job, upstream).
func TestDecide_UnrecognizedModeAdmitsUnconditionally(t *testing.T) {
	got := Decide(Mode("bogus"), []RepoAdmission{{FullName: "acme/widgets", Enrolled: false}})
	if !got.Admitted {
		t.Fatalf("Decide(bogus mode, unenrolled repo) = %+v, want Admitted=true", got)
	}
}

// TestDecide_ModeCohortRequiresEveryRepoEnrolled is the mutation-test
// anchor for §32's "multi-repo sessions: all named repos must be
// enrolled" requirement: a single-repo session and a multi-repo session
// both refuse the instant ANY named repo is not enrolled, and both admit
// only when EVERY named repo is.
func TestDecide_ModeCohortRequiresEveryRepoEnrolled(t *testing.T) {
	tests := []struct {
		name         string
		repos        []RepoAdmission
		wantAdmitted bool
		wantReason   RefusalReason
		wantRepo     string
	}{
		{
			name:         "single enrolled repo admitted",
			repos:        []RepoAdmission{{FullName: "acme/widgets", Enrolled: true}},
			wantAdmitted: true,
		},
		{
			name:         "single unenrolled repo refused",
			repos:        []RepoAdmission{{FullName: "acme/widgets", Enrolled: false}},
			wantAdmitted: false,
			wantReason:   ReasonRepoNotEnrolled,
			wantRepo:     "acme/widgets",
		},
		{
			name: "multi-repo, all enrolled, admitted",
			repos: []RepoAdmission{
				{FullName: "acme/widgets", Enrolled: true},
				{FullName: "acme/gadgets", Enrolled: true},
			},
			wantAdmitted: true,
		},
		{
			name: "multi-repo, second repo unenrolled, refused naming that repo",
			repos: []RepoAdmission{
				{FullName: "acme/widgets", Enrolled: true},
				{FullName: "acme/gadgets", Enrolled: false},
			},
			wantAdmitted: false,
			wantReason:   ReasonRepoNotEnrolled,
			wantRepo:     "acme/gadgets",
		},
		{
			name: "multi-repo, first repo unenrolled, refused naming FIRST repo (stop-at-first-failure)",
			repos: []RepoAdmission{
				{FullName: "acme/widgets", Enrolled: false},
				{FullName: "acme/gadgets", Enrolled: false},
			},
			wantAdmitted: false,
			wantReason:   ReasonRepoNotEnrolled,
			wantRepo:     "acme/widgets",
		},
		{
			name:         "zero repos vacuously admitted",
			repos:        nil,
			wantAdmitted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(ModeCohort, tt.repos)
			if got.Admitted != tt.wantAdmitted {
				t.Fatalf("Decide(ModeCohort, %+v).Admitted = %v, want %v", tt.repos, got.Admitted, tt.wantAdmitted)
			}
			if !tt.wantAdmitted {
				if got.Reason != tt.wantReason {
					t.Fatalf("Decide(ModeCohort, %+v).Reason = %q, want %q", tt.repos, got.Reason, tt.wantReason)
				}
				if got.RepoFullName != tt.wantRepo {
					t.Fatalf("Decide(ModeCohort, %+v).RepoFullName = %q, want %q", tt.repos, got.RepoFullName, tt.wantRepo)
				}
			}
		})
	}
}
