// internal/data/dlto.go

// Package data: отдельный датасет должностных лиц.
//
// Основной датасет (dataset.json) содержит только лиц — без деления на
// физических лиц и должностных лиц. Принадлежность к должностным лицам задаётся
// отдельным файлом dltos.json: он небольшой, его правят через /admin, и он же
// является источником роли DLTO при загрузке в журнал.
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DLTOSet — реестр должностных лиц таможенных органов.
type DLTOSet struct {
	Persons []Person `json:"persons"`
}

// LoadDLTOSet читает реестр должностных лиц.
func LoadDLTOSet(path string) (DLTOSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return DLTOSet{}, fmt.Errorf("open officials dataset: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(io.LimitReader(f, 8<<20))
	dec.DisallowUnknownFields()
	var set DLTOSet
	if err := dec.Decode(&set); err != nil {
		return DLTOSet{}, fmt.Errorf("decode officials dataset: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return DLTOSet{}, errors.New("officials dataset must contain one JSON value")
	}
	set.Canonicalize()
	if err := set.Validate(); err != nil {
		return DLTOSet{}, err
	}
	return set, nil
}

// SaveDLTOSet записывает реестр должностных лиц атомарно.
func SaveDLTOSet(path string, set DLTOSet) error {
	set.Canonicalize()
	if err := set.Validate(); err != nil {
		return fmt.Errorf("validate officials dataset: %w", err)
	}
	raw, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return fmt.Errorf("encode officials dataset: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".dltos-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write officials dataset: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("sync officials dataset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close officials dataset: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("rename officials dataset: %w", err)
	}
	return nil
}

// Validate проверяет обязательные поля реестра.
//
// Паспорт обязателен: без него должностное лицо не идентифицируется в реестре,
// и проверка по поручению не выполнится.
func (s DLTOSet) Validate() error {
	seen := make(map[string]bool, len(s.Persons))
	for _, p := range s.Persons {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			return errors.New("official without identifier")
		}
		if seen[id] {
			return fmt.Errorf("duplicate official identifier %q", id)
		}
		seen[id] = true
		if strings.TrimSpace(p.FullName) == "" {
			return fmt.Errorf("official %q has no full name", id)
		}
		if Digits(p.Passport) == "" {
			return fmt.Errorf("official %q has no passport", id)
		}
	}
	return nil
}

// Canonicalize сортирует записи и выравнивает пробелы.
func (s *DLTOSet) Canonicalize() {
	for i := range s.Persons {
		s.Persons[i].ID = strings.TrimSpace(s.Persons[i].ID)
		s.Persons[i].FullName = strings.Join(strings.Fields(s.Persons[i].FullName), " ")
		s.Persons[i].Passport = strings.TrimSpace(s.Persons[i].Passport)
		s.Persons[i].Post = strings.TrimSpace(s.Persons[i].Post)
		s.Persons[i].Role = ""
	}
	sort.Slice(s.Persons, func(i, j int) bool { return s.Persons[i].ID < s.Persons[j].ID })
}

// ToPersons возвращает записи с проставленной ролью DLTO — в таком виде они
// попадают в журнал проверок.
func (s DLTOSet) ToPersons() []Person {
	out := make([]Person, 0, len(s.Persons))
	for _, p := range s.Persons {
		p.Role = "DLTO"
		out = append(out, p)
	}
	return out
}

// Get возвращает запись по идентификатору.
func (s DLTOSet) Get(id string) (Person, bool) {
	for _, p := range s.Persons {
		if p.ID == id {
			return p, true
		}
	}
	return Person{}, false
}

// Upsert добавляет или обновляет запись.
func (s *DLTOSet) Upsert(p Person) {
	p.Role = ""
	for i := range s.Persons {
		if s.Persons[i].ID == p.ID {
			// Сохраняем заполненные поля, которых нет в форме.
			if p.Address == "" {
				p.Address = s.Persons[i].Address
			}
			if p.City == "" {
				p.City = s.Persons[i].City
			}
			if p.Street == "" {
				p.Street = s.Persons[i].Street
			}
			if p.House == "" {
				p.House = s.Persons[i].House
			}
			if p.BirthDate == "" {
				p.BirthDate = s.Persons[i].BirthDate
			}
			if p.SNILS == "" {
				p.SNILS = s.Persons[i].SNILS
			}
			if p.INN == "" {
				p.INN = s.Persons[i].INN
			}
			if len(p.Citizenships) == 0 {
				p.Citizenships = s.Persons[i].Citizenships
			}
			s.Persons[i] = p
			return
		}
	}
	s.Persons = append(s.Persons, p)
}

// Delete удаляет запись по идентификатору.
func (s *DLTOSet) Delete(id string) bool {
	for i := range s.Persons {
		if s.Persons[i].ID != id {
			continue
		}
		s.Persons = append(s.Persons[:i], s.Persons[i+1:]...)
		return true
	}
	return false
}
