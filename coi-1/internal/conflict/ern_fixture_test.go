// internal/conflict/ern_fixture_test.go

package conflict

import (
	"context"
	"reflect"
	"testing"
	"time"

	"coi-1/internal/data"
)

func ernDemoSnapshot() Snapshot {
	return Snapshot{
		Now:     time.Date(2026, time.November, 12, 13, 0, 0, 0, time.UTC),
		OrderID: "LTD-DEMO-001",
		DLTO: data.Person{
			ID:           "DL-014",
			Role:         "DLTO",
			FullName:     "ЗАЙЦЕВ АЛЕКСЕЙ ПАВЛОВИЧ",
			Passport:     "6605 856401",
			Address:      "г. Нижний Тагил, ул. ГАГАРИНА, д. 100, кв. 61",
			City:         "Нижний Тагил",
			Street:       "ул. ГАГАРИНА",
			House:        "100",
			BirthDate:    "1979-01-15",
			Citizenships: []string{"RF"},
		},
		Person: data.Person{
			ID:           "P-185",
			Role:         "PHYS",
			FullName:     "ЗАЙЦЕВ ЕГОР СЕРГЕЕВИЧ",
			Passport:     "6697 478160",
			Address:      "г. Нижний Тагил, ул. ГАГАРИНА, д. 100, кв. 61",
			City:         "Нижний Тагил",
			Street:       "ул. ГАГАРИНА",
			House:        "100",
			BirthDate:    "1964-12-05",
			Citizenships: []string{"RF"},
		},
		Kinship: []data.Kinship{
			{A: "DL-014", B: "P-185", Kind: "sibling", CivilID: "ZAGS-004"},
		},
		IdentityMethod: "passport",
	}
}

func TestERN_DemoPair(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine("rules_v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := engine.Evaluate(context.Background(), ernDemoSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if signal.Severity != SeverityCritical {
		t.Fatalf("severity = %q, want %q", signal.Severity, SeverityCritical)
	}
	if signal.PersonID == nil || *signal.PersonID != "P-185" {
		t.Fatalf("unexpected person ID: %v", signal.PersonID)
	}
	got := make([]string, 0, len(signal.Reasons))
	for _, reason := range signal.Reasons {
		got = append(got, reason.Code)
	}
	want := []string{"K-01", "K-02", "K-10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reason codes = %v, want %v", got, want)
	}
}

func TestERN_ReverseKinshipDoesNotDuplicateReason(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine("rules_v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ernDemoSnapshot()
	snapshot.Kinship = append(snapshot.Kinship, data.Kinship{
		A: "P-185", B: "DL-014", Kind: "sibling", CivilID: "ZAGS-004-R",
	})
	signal, err := engine.Evaluate(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, reason := range signal.Reasons {
		if reason.Code == "K-01" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("K-01 reason count = %d, want 1", count)
	}
}

func TestERN_DemoPairDeterminism(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine("rules_v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ernDemoSnapshot()
	first, err := engine.Evaluate(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Evaluate(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same snapshot produced different signals")
	}
}

func TestCoverage_MissingSocialSources(t *testing.T) {
	t.Parallel()
	sources := []SourceCoverage{
		{Source: "person", State: SourceAvailable},
		{Source: "kinship", State: SourceAvailable},
		{Source: "civil_registry", State: SourceAvailable},
		{Source: "divorce", State: SourceAvailable},
		{Source: "emancipation", State: SourceAvailable},
		{Source: "tax_registration", State: SourceAvailable},
		{Source: "post_district", State: SourceUnavailable},
		{Source: "parental_rights", State: SourceUnavailable},
		{Source: "legal_capacity", State: SourceUnavailable},
		{Source: "employment_service", State: SourceUnavailable},
	}
	report, err := BuildCoverage(sources)
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete {
		t.Fatal("incomplete dataset reported as complete")
	}
	tests := []struct {
		code string
		want bool
	}{
		{"K-01", true},
		{"K-02", true},
		{"K-13", false},
		{"K-16", false},
		{"K-17", false},
		{"K-18", false},
		{"K-06", false},
		{"UNKNOWN", false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			t.Parallel()
			if got := report.CanEvaluate(tt.code); got != tt.want {
				t.Fatalf("CanEvaluate(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestCoverage_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sources []SourceCoverage
	}{
		{"empty source", []SourceCoverage{{State: SourceAvailable}}},
		{"duplicate source", []SourceCoverage{
			{Source: "person", State: SourceAvailable},
			{Source: "person", State: SourceUnavailable},
		}},
		{"invalid state", []SourceCoverage{
			{Source: "person", State: SourceState("unknown")},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildCoverage(tt.sources); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
