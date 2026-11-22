// internal/erndb/bundle.go

// Package erndb: сохранение нормализованного датасета в отдельный файл.
//
// Датасет живёт отдельно от БД приложения: SQL-дамп ЕРН конвертируется один раз
// (cmd/seed) в data/dataset.json, а дальше и реестр (store), и мок СМЭВ читают
// один и тот же файл. Повторное исполнение SQL-дампа при каждом старте больше
// не нужно.
package erndb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"coi-1/internal/data"
)

// SaveBundle записывает нормализованный датасет в один JSON-файл.
// Запись атомарная: сначала во временный файл рядом, затем rename.
func SaveBundle(path string, b data.Bundle) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("validate dataset: %w", err)
	}
	b.Canonicalize()

	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dataset: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".dataset-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write dataset: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("sync dataset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close dataset: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("rename dataset: %w", err)
	}
	return nil
}
