package plan

import (
	"reflect"
	"testing"
)

func TestNextVersion(t *testing.T) {
	tests := []struct {
		name     string
		existing []Summary
		want     int
	}{
		{
			name:     "first plan ever",
			existing: nil,
			want:     1,
		},
		{
			name:     "normal v1 -> v2",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusAwaitingApproval}},
			want:     2,
		},
		{
			name:     "existing decided row still counts toward version numbering",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusApproved}},
			want:     2,
		},
		{
			name: "highest version wins regardless of order",
			existing: []Summary{
				{ID: "p2", Version: 2, Status: StatusSuperseded},
				{ID: "p1", Version: 1, Status: StatusSuperseded},
				{ID: "p3", Version: 3, Status: StatusAwaitingApproval},
			},
			want: 4,
		},
		{
			name:     "rejected row still counts toward version numbering",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusRejected}},
			want:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextVersion(tt.existing); got != tt.want {
				t.Errorf("NextVersion(%+v) = %d, want %d", tt.existing, got, tt.want)
			}
		})
	}
}

func TestShouldSupersede(t *testing.T) {
	tests := []struct {
		name     string
		existing []Summary
		want     []ID
	}{
		{
			name:     "first plan ever: nothing to supersede",
			existing: nil,
			want:     nil,
		},
		{
			name:     "normal v1 -> v2: supersede the sole awaiting_approval row",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusAwaitingApproval}},
			want:     []ID{"p1"},
		},
		{
			name:     "already-approved row is never touched",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusApproved}},
			want:     nil,
		},
		{
			name:     "already-rejected row is never touched",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusRejected}},
			want:     nil,
		},
		{
			name:     "already-superseded row is never touched again",
			existing: []Summary{{ID: "p1", Version: 1, Status: StatusSuperseded}},
			want:     nil,
		},
		{
			name: "mixed history: only the awaiting_approval row is named",
			existing: []Summary{
				{ID: "p1", Version: 1, Status: StatusSuperseded},
				{ID: "p2", Version: 2, Status: StatusRejected},
				{ID: "p3", Version: 3, Status: StatusAwaitingApproval},
			},
			want: []ID{"p3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldSupersede(tt.existing)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ShouldSupersede(%+v) = %v, want %v", tt.existing, got, tt.want)
			}
		})
	}
}
