package httpapi

import (
	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// sessionToDTO converts a stored sqlcgen.Session row into its REST wire
// shape (restdtos.Session, contracts/rest/v1/dtos.schema.json). Explicit
// conversions are needed for the enum fields -- sqlcgen.SessionStatus/
// SessionSpawnSource and their restdtos counterparts are two DISTINCT
// named string types generated from two independent schemas (a Postgres
// enum vs a JSON Schema enum) that happen to share the same underlying
// type and allowed values, per this schema's own "Enums here MUST match
// the Postgres enums ... exactly" requirement -- and for failureReason/
// createdBy's own nullability representations (pgtype's own
// nullable-value convention vs restdtos' plain-pointer/wrapper-struct
// convention).
func sessionToDTO(s sqlcgen.Session) restdtos.Session {
	var createdBy *string
	if s.CreatedBy.Valid {
		str := s.CreatedBy.String()
		createdBy = &str
	}

	var failureReason *restdtos.SessionFailureReason
	if s.FailureReason != nil {
		failureReason = &restdtos.SessionFailureReason{Value: string(*s.FailureReason)}
	}

	return restdtos.Session{
		Id:            s.ID.String(),
		Title:         s.Title,
		Status:        restdtos.SessionStatus(s.Status),
		FailureReason: failureReason,
		Archived:      s.Archived,
		SpawnSource:   restdtos.SessionSpawnSource(s.SpawnSource),
		CreatedBy:     createdBy,
		CreatedAt:     s.CreatedAt.Time,
		UpdatedAt:     s.UpdatedAt.Time,
	}
}
