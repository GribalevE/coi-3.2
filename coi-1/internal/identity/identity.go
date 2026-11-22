// internal/identity/identity.go

// Package identity matches submitted identities without fuzzy inference.
package identity

import (
	"context"

	"coi-1/internal/data"
)

// Reference is the identity supplied by AIS.
type Reference struct {
	Passport    string `json:"passport"`
	FullName    string `json:"full_name"`
	Address     string `json:"address"`
	SNILS       string `json:"snils,omitempty"`
	INN         string `json:"inn,omitempty"`
	Citizenship string `json:"citizenship,omitempty"`
}

// Registry supplies a consistent local identity projection.
type Registry interface {
	People(context.Context) ([]data.Person, error)
}

// Result describes either one identity or a manual-review reason.
type Result struct {
	Person *data.Person
	Method string
	Reason string
}

// Matcher resolves references against a registry.
type Matcher struct {
	Registry Registry
}

// Match applies passport, name/address and alternative-ID matching.
func (m Matcher) Match(ctx context.Context, ref Reference) (Result, error) {
	if ref.Citizenship != "" && data.Normalize(ref.Citizenship) != "RF" {
		return Result{Reason: "citizenship_not_rf"}, nil
	}
	people, err := m.Registry.People(ctx)
	if err != nil {
		return Result{}, err
	}
	selectOne := func(matches []data.Person, method string) Result {
		if len(matches) != 1 {
			return Result{Reason: "identity_ambiguous"}
		}
		p := matches[0]
		rf := false
		for _, c := range p.Citizenships {
			rf = rf || c == "RF"
		}
		if !rf {
			return Result{Reason: "citizenship_not_rf"}
		}
		return Result{Person: &p, Method: method}
	}
	var matches []data.Person
	if key := data.Digits(ref.Passport); key != "" {
		for _, p := range people {
			if data.Digits(p.Passport) == key {
				matches = append(matches, p)
			}
		}
		if len(matches) != 0 {
			if len(matches) != 1 {
				return Result{Reason: "identity_ambiguous"}, nil
			}
			p := matches[0]
			if ref.FullName != "" &&
				data.Normalize(ref.FullName) != data.Normalize(p.FullName) {
				return Result{Reason: "identity_conflict"}, nil
			}
			if ref.Address != "" &&
				data.Normalize(ref.Address) != data.Normalize(p.Address) {
				return Result{Reason: "identity_conflict"}, nil
			}
			return selectOne(matches, "passport"), nil
		}
	}
	if ref.FullName != "" && ref.Address != "" {
		for _, p := range people {
			if data.Normalize(p.FullName) == data.Normalize(ref.FullName) &&
				data.Normalize(p.Address) == data.Normalize(ref.Address) {
				matches = append(matches, p)
			}
		}
		if len(matches) != 0 {
			return selectOne(matches, "name_address"), nil
		}
	}
	if ref.SNILS != "" || ref.INN != "" {
		for _, p := range people {
			if ref.SNILS != "" && data.Digits(ref.SNILS) != data.Digits(p.SNILS) {
				continue
			}
			if ref.INN != "" && data.Digits(ref.INN) != data.Digits(p.INN) {
				continue
			}
			matches = append(matches, p)
		}
		if len(matches) != 0 {
			return selectOne(matches, "alternative_id"), nil
		}
	}
	return Result{Reason: "person_not_found"}, nil
}
