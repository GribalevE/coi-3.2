// internal/smev/from_sql.go

package smev

import (
	"context"
	"fmt"

	"coi-1/internal/erndb"
)

func MockFromSQL(path string) (*MockClient, error) {
	db, err := erndb.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	bundle, err := erndb.Bundle(context.Background(), db)
	if err != nil {
		return nil, fmt.Errorf("load seed %s: %w", path, err)
	}
	return NewMockClient(bundle, 0, []string{
		"parental_rights",
		"legal_capacity",
		"employment_service",
		"post_district",
	})
}
