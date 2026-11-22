// internal/data/data.go

// Package data defines the normalized synthetic dataset.
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Person is a synthetic registry identity.
type Person struct {
	ID              string   `json:"id"`
	Role            string   `json:"role,omitempty"`
	FullName        string   `json:"full_name"`
	Post            string   `json:"post,omitempty"`
	Passport        string   `json:"passport"`
	Address         string   `json:"address"`
	City            string   `json:"city"`
	Street          string   `json:"street"`
	House           string   `json:"house"`
	BirthDate       string   `json:"birth_date"`
	SNILS           string   `json:"snils"`
	INN             string   `json:"inn"`
	Citizenships    []string `json:"citizenships"`
	PostCity        string   `json:"post_city,omitempty"`
	DistrictStreets []string `json:"district_streets,omitempty"`
	Emancipated     bool     `json:"emancipated,omitempty"`
}

// Kinship is a directed parent edge or an undirected family edge.
type Kinship struct {
	A       string `json:"a"`
	B       string `json:"b"`
	Kind    string `json:"kind"`
	CivilID string `json:"civil_id,omitempty"`
}

// Civil is a synthetic civil registry record.
type Civil struct {
	ID   string `json:"id"`
	A    string `json:"a"`
	B    string `json:"b"`
	Kind string `json:"kind"`
}

// Divorce records a dissolved marriage.
type Divorce struct {
	A string `json:"a"`
	B string `json:"b"`
}

// Guardianship records a guardian relationship.
type Guardianship struct {
	Guardian string `json:"guardian"`
	Ward     string `json:"ward"`
}

// Emancipation records emancipation.
type Emancipation struct {
	PersonID string `json:"person_id"`
}

// Parental records deprivation of parental rights.
type Parental struct {
	PersonID string `json:"person_id"`
	ChildID  string `json:"child_id"`
}

// Capacity records a person's legal capacity.
type Capacity struct {
	PersonID string `json:"person_id"`
	State    string `json:"state"`
}

// Employment records unemployment registration.
type Employment struct {
	PersonID   string `json:"person_id"`
	Unemployed bool   `json:"unemployed"`
}

// Tax records entrepreneur identifiers.
type Tax struct {
	PersonID string   `json:"person_id"`
	OGRNIP   []string `json:"ogrnip"`
}

// DriverLicense is a synthetic license record.
type DriverLicense struct {
	PersonID string `json:"person_id"`
	Number   string `json:"number"`
}

// Bundle is the normalized dataset exchanged with the mock adapter.
type Bundle struct {
	Persons        []Person        `json:"persons"`
	Kinship        []Kinship       `json:"kinship"`
	Civil          []Civil         `json:"civil_registry"`
	Divorces       []Divorce       `json:"divorce"`
	Guardianships  []Guardianship  `json:"guardianship"`
	Emancipations  []Emancipation  `json:"emancipation"`
	Parental       []Parental      `json:"parental_rights"`
	Capacity       []Capacity      `json:"legal_capacity"`
	Employment     []Employment    `json:"employment_service"`
	Tax            []Tax           `json:"tax_registration"`
	DriverLicenses []DriverLicense `json:"driver_license"`
}

// Normalize returns a conservative textual comparison key.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(strings.TrimSpace(s))), " ")
}

// Digits removes non-digit characters from an identifier.
func Digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// Load reads and validates a bounded JSON bundle.
func Load(path string) (Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("open dataset: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 32<<20))
	dec.DisallowUnknownFields()
	var b Bundle
	if err := dec.Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("decode dataset: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Bundle{}, errors.New("dataset must contain one JSON value")
	}
	if err := b.Validate(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// Validate checks identifiers, references and civil evidence.
func (b Bundle) Validate() error {
	people := make(map[string]bool, len(b.Persons))
	for _, p := range b.Persons {
		if p.ID == "" || people[p.ID] || p.FullName == "" {
			return errors.New("invalid or duplicate person")
		}
		// Роль в датасете лиц не обязательна: принадлежность к должностным
		// лицам задаётся отдельным реестром (data/dltos.json).
		if p.Role != "" && p.Role != "PHYS" && p.Role != "DLTO" {
			return errors.New("invalid person role")
		}

		people[p.ID] = true
	}
	has := func(ids ...string) bool {
		for _, id := range ids {
			if !people[id] {
				return false
			}
		}
		return true
	}
	civil := make(map[string]Civil, len(b.Civil))
	for _, c := range b.Civil {
		if c.ID == "" || !has(c.A, c.B) {
			return errors.New("invalid civil record")
		}
		if _, ok := civil[c.ID]; ok {
			return errors.New("duplicate civil identifier")
		}
		civil[c.ID] = c
	}
	edges := make(map[string]bool)
	for _, k := range b.Kinship {
		key := k.A + "\x00" + k.B + "\x00" + k.Kind
		if !has(k.A, k.B) || k.A == k.B || edges[key] {
			return errors.New("invalid or duplicate kinship")
		}
		if k.Kind != "parent" && k.Kind != "spouse" && k.Kind != "sibling" {
			return errors.New("unsupported kinship kind")
		}
		edges[key] = true
		if k.CivilID != "" {
			c, ok := civil[k.CivilID]
			if !ok || c.A != k.A || c.B != k.B {
				return errors.New("kinship civil evidence mismatch")
			}
		}
	}
	for _, x := range b.Divorces {
		if !has(x.A, x.B) {
			return errors.New("invalid divorce reference")
		}
	}
	for _, x := range b.Guardianships {
		if !has(x.Guardian, x.Ward) {
			return errors.New("invalid guardianship reference")
		}
	}
	for _, x := range b.Emancipations {
		if !has(x.PersonID) {
			return errors.New("invalid emancipation reference")
		}
	}
	for _, x := range b.Parental {
		if !has(x.PersonID, x.ChildID) {
			return errors.New("invalid parental reference")
		}
	}
	unique := func(ids []string) bool {
		seen := make(map[string]bool)
		for _, id := range ids {
			if !has(id) || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	var ids []string
	for _, x := range b.Capacity {
		if x.State != "full" && x.State != "limited" && x.State != "incapable" {
			return errors.New("invalid capacity state")
		}
		ids = append(ids, x.PersonID)
	}
	if !unique(ids) {
		return errors.New("invalid capacity references")
	}
	ids = nil
	for _, x := range b.Employment {
		ids = append(ids, x.PersonID)
	}
	if !unique(ids) {
		return errors.New("invalid employment references")
	}
	ids = nil
	for _, x := range b.Tax {
		ids = append(ids, x.PersonID)
	}
	if !unique(ids) {
		return errors.New("invalid tax references")
	}
	ids = nil
	for _, x := range b.DriverLicenses {
		ids = append(ids, x.PersonID)
	}
	if !unique(ids) {
		return errors.New("invalid driver license references")
	}
	return nil
}

// Canonicalize orders top-level records before serialization.
func (b *Bundle) Canonicalize() {
	sort.Slice(b.Persons, func(i, j int) bool { return b.Persons[i].ID < b.Persons[j].ID })
	sort.Slice(b.Kinship, func(i, j int) bool {
		a, c := b.Kinship[i], b.Kinship[j]
		return a.A+"\x00"+a.B+"\x00"+a.Kind < c.A+"\x00"+c.B+"\x00"+c.Kind
	})
	sort.Slice(b.Civil, func(i, j int) bool { return b.Civil[i].ID < b.Civil[j].ID })
	for i := range b.Persons {
		sort.Strings(b.Persons[i].Citizenships)
		sort.Strings(b.Persons[i].DistrictStreets)
	}
	for i := range b.Tax {
		sort.Strings(b.Tax[i].OGRNIP)
	}
}
