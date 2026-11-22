// internal/audit/audit.go

// Package audit exposes read-only access to append-only audit events.
package audit

import (
	"context"

	"coi-1/internal/store"
)

// Repository is the smallest audit-reading contract required by HTTP.
type Repository interface {
	Audit(context.Context, string) ([]store.AuditRow, error)
}
