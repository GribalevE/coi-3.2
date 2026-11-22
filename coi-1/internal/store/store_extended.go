// internal/store/store_extended.go

package store

import (
	"coi-1/internal/erndb"
	"context"
)

// CountPersons — быстрая проверка, пуст ли реестр.
func (s *Store) CountPersons(ctx context.Context, out *int) error {
	return s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM person").Scan(out)
}

// LoadFromSQL исполняет INSERT-операторы из файла.
//
// Мы не запускаем файл целиком: там есть CREATE TABLE IF NOT EXISTS,
// которые конфликтуют с миграциями. Поэтому фильтруем только INSERT.
func (s *Store) LoadFromSQL(ctx context.Context, path string) error {
	db, err := erndb.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	bundle, err := erndb.Bundle(ctx, db)
	if err != nil {
		return err
	}
	return s.Seed(ctx, bundle)
}
