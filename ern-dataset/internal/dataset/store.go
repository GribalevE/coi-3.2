// Package dataset даёт доступ к файлу основного датасета ЕРН: чтение,
// контрольная сумма и разбор счётчиков. Сервис только читает файл, запись
// в него не предусмотрена.
package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Counts — сколько записей в основных секциях датасета. Нужны для /health и
// /api/v1/meta: по ним видно, тот ли файл забрал проверяющий сервис.
type Counts struct {
	Persons int `json:"persons"`
	Kinship int `json:"kinship"`
	Civil   int `json:"civil_registry"`
	Divorce int `json:"divorce"`
	Tax     int `json:"tax_registration"`
}

// Info — сведения о файле датасета на момент чтения.
type Info struct {
	File     string    `json:"file"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	SHA256   string    `json:"sha256"`
	Counts   Counts    `json:"counts"`
}

// Store читает файл датасета. Кэша нет намеренно: файл обновляется заменой,
// а чтение 150 КБ дешевле, чем сопровождение инвалидации кэша.
type Store struct {
	path string
}

// New проверяет, что файл существует и разбирается, и возвращает хранилище.
// Ошибка на старте лучше, чем 200 OK с пустым датасетом.
func New(path string) (*Store, error) {
	raw, _, err := read(path)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Store{path: path}, nil
}

// Path возвращает путь к файлу датасета.
func (s *Store) Path() string { return s.path }

// Read возвращает содержимое файла и его метаданные.
func (s *Store) Read() ([]byte, Info, error) {
	return read(s.path)
}

// Parse проверяет структуру датасета и считает записи. Полная схема
// проверяется на стороне потребителя (coi-1), здесь — только то, без чего
// сервис бессмысленен: наличие секции persons.
func Parse(raw []byte) (Counts, error) {
	var doc struct {
		Persons []json.RawMessage `json:"persons"`
		Kinship []json.RawMessage `json:"kinship"`
		Civil   []json.RawMessage `json:"civil_registry"`
		Divorce []json.RawMessage `json:"divorce"`
		Tax     []json.RawMessage `json:"tax_registration"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Counts{}, fmt.Errorf("разбор JSON: %w", err)
	}
	if len(doc.Persons) == 0 {
		return Counts{}, errors.New("в датасете нет записей в секции persons")
	}
	return Counts{
		Persons: len(doc.Persons),
		Kinship: len(doc.Kinship),
		Civil:   len(doc.Civil),
		Divorce: len(doc.Divorce),
		Tax:     len(doc.Tax),
	}, nil
}

func read(path string) ([]byte, Info, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, Info{}, fmt.Errorf("файл датасета: %w", err)
	}
	if stat.IsDir() {
		return nil, Info{}, fmt.Errorf("%s — это каталог, нужен файл dataset.json", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, Info{}, fmt.Errorf("чтение датасета: %w", err)
	}
	if len(raw) == 0 {
		return nil, Info{}, fmt.Errorf("файл %s пуст", path)
	}

	sum := sha256.Sum256(raw)
	return raw, Info{
		File:     filepath.Base(path),
		Size:     stat.Size(),
		Modified: stat.ModTime().UTC(),
		SHA256:   hex.EncodeToString(sum[:]),
	}, nil
}
