// internal/conflict/conflict.go

// Package conflict evaluates deterministic human-review signals without I/O.
package conflict

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"coi-1/internal/data"
)

type Severity string

const (
	SeverityNone       Severity = "none"
	SeverityInfo       Severity = "informational"
	SeverityWarning    Severity = "warning"
	SeverityLow        Severity = "low"
	SeverityMedium     Severity = "medium"
	SeverityHigh       Severity = "high"
	SeverityCritical   Severity = "critical"
	SeverityContextual Severity = "contextual"
	SeverityNoData     Severity = "no_data"
)

type CriterionStatus string

const (
	Active         CriterionStatus = "Active"
	RequiresSource CriterionStatus = "RequiresSource"
	DisabledInV1   CriterionStatus = "DisabledInV1"
)

type Fact struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

type Reason struct {
	Code     string   `json:"code"`
	Title    string   `json:"title"`
	Weight   int      `json:"weight"`
	Severity Severity `json:"severity"`
	Facts    []Fact   `json:"facts"`
}

type Signal struct {
	OrderID     string    `json:"order_id"`
	DLTOID      string    `json:"dlto_id"`
	PersonID    *string   `json:"person_id"`
	RuleVersion string    `json:"rule_version"`
	Severity    Severity  `json:"severity"`
	Reasons     []Reason  `json:"reasons"`
	GeneratedAt time.Time `json:"generated_at"`
	// Coverage заполняется только через EvaluateWithCoverage.
	Coverage *CoverageReport `json:"coverage,omitempty"`
}

type Snapshot struct {
	Now            time.Time
	OrderID        string
	DLTO           data.Person
	Person         data.Person
	Kinship        []data.Kinship
	Divorces       []data.Divorce
	Capacity       data.Capacity
	Parental       []data.Parental
	Employment     data.Employment
	DLTOTax        data.Tax
	PersonTax      data.Tax
	IdentityMethod string
}

type Criterion interface {
	Code() string
	Title() string
	Weight() int
	DefaultSeverity() Severity
	Status() CriterionStatus
	Evaluate(context.Context, Snapshot) ([]Reason, error)
}

type criterion struct {
	code     string
	title    string
	weight   int
	severity Severity
	status   CriterionStatus
}

func (c criterion) Code() string              { return c.code }
func (c criterion) Title() string             { return c.title }
func (c criterion) Weight() int               { return c.weight }
func (c criterion) DefaultSeverity() Severity { return c.severity }
func (c criterion) Status() CriterionStatus   { return c.status }

func same(a, b string) bool {
	return a != "" && b != "" && data.Normalize(a) == data.Normalize(b)
}

func pair(a, b, x, y string) bool {
	return a == x && b == y || a == y && b == x
}

func surname(s string) string {
	fields := strings.Fields(data.Normalize(s))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func initials(s string) string {
	fields := strings.Fields(data.Normalize(s))
	if len(fields) < 2 {
		return ""
	}
	var out strings.Builder
	for _, f := range fields {
		r, _ := utf8.DecodeRuneInString(f)
		out.WriteRune(r)
	}
	return out.String()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func neighbors(edges []data.Kinship, id string) map[string]bool {
	out := make(map[string]bool)
	for _, k := range edges {
		if k.A == id {
			out[k.B] = true
		}
		if k.B == id {
			out[k.A] = true
		}
	}
	return out
}

func (c criterion) Evaluate(ctx context.Context, s Snapshot) ([]Reason, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.status != Active {
		return []Reason{}, nil
	}
	facts := make([]Fact, 0)
	add := func(key, value, source string) {
		facts = append(facts, Fact{Key: key, Value: value, Source: source})
	}
	a, b := s.DLTO, s.Person
	matched := false
	switch c.code {
	case "K-01":
		evidence := make(map[string]bool)
		for _, k := range s.Kinship {
			if pair(k.A, k.B, a.ID, b.ID) && k.CivilID != "" {
				evidence[k.CivilID] = true
			}
		}
		for _, id := range sortedKeys(evidence) {
			add("civil_record_id", id, "civil_registry")
			matched = true
		}
	case "K-02":
		matched = same(a.Address, b.Address)
		if matched {
			add("address", b.Address, "person")
		}
	case "K-03":
		for _, d := range s.Divorces {
			if pair(d.A, d.B, a.ID, b.ID) {
				matched = true
			}
		}
		if matched {
			add("pair", a.ID+" ↔ "+b.ID, "divorce")
		}
	case "K-04":
		children := make(map[string]bool)
		for _, k := range s.Kinship {
			if k.Kind == "parent" && k.A == a.ID {
				children[k.B] = true
			}
		}
		common := make(map[string]bool)
		for _, k := range s.Kinship {
			if k.Kind == "parent" && k.A == b.ID && children[k.B] {
				common[k.B] = true
			}
		}
		for _, id := range sortedKeys(common) {
			add("common_child_id", id, "kinship")
			matched = true
		}
		if matched {
			add("inference", "two_parent_edges", "engine")
		}
	case "K-05":
		left, right := neighbors(s.Kinship, a.ID), neighbors(s.Kinship, b.ID)
		if !left[b.ID] {
			common := make(map[string]bool)
			for id := range left {
				if right[id] && id != a.ID && id != b.ID {
					common[id] = true
				}
			}
			for _, id := range sortedKeys(common) {
				add("intermediate_person_id", id, "kinship")
				matched = true
			}
		}
		if matched {
			add("path_length", "2", "engine")
		}
	case "K-07":
		matched = b.Emancipated && neighbors(s.Kinship, a.ID)[b.ID]
		if matched {
			add("emancipated_person_id", b.ID, "emancipation")
		}
	case "K-08":
		matched = same(a.FullName, b.FullName)
	case "K-09":
		matched = same(a.FullName, b.FullName) && same(a.BirthDate, b.BirthDate)
	case "K-10":
		matched = same(surname(a.FullName), surname(b.FullName))
	case "K-11":
		matched = same(initials(a.FullName), initials(b.FullName)) && same(a.City, b.City)
	case "K-12":
		matched = same(a.City, b.City) && same(a.Street, b.Street) &&
			!same(a.Address, b.Address)
		if matched {
			add("same_house", fmt.Sprint(same(a.House, b.House)), "person")
			add("street", b.Street, "person")
		}
	case "K-13":
		matched = same(a.PostCity, b.City)
		if matched {
			threshold := false
			for _, street := range a.DistrictStreets {
				threshold = threshold || same(street, b.Street)
			}
			add("district_threshold_met", fmt.Sprint(threshold), "post_configuration")
			add("classification", "informational_only", "engine")
		}
	case "K-16":
		for _, p := range s.Parental {
			if p.PersonID == b.ID {
				matched = true
			}
		}
		if matched {
			add("person_id", b.ID, "parental_rights")
		}
	case "K-17":
		matched = s.Capacity.PersonID == b.ID &&
			(s.Capacity.State == "limited" || s.Capacity.State == "incapable")
		if matched {
			add("capacity", s.Capacity.State, "legal_capacity")
		}
	case "K-18":
		matched = s.Employment.PersonID == b.ID && s.Employment.Unemployed
		if matched {
			add("unemployed", "true", "employment_service")
		}
	case "K-19":
		shared := make(map[string]bool)
		for _, x := range s.DLTOTax.OGRNIP {
			for _, y := range s.PersonTax.OGRNIP {
				if x != "" && x == y {
					shared[x] = true
				}
			}
		}
		for _, id := range sortedKeys(shared) {
			add("shared_ogrnip", id, "tax_registration")
			matched = true
		}
		if matched {
			add("classification", "registry_anomaly_not_joint_enterprise", "engine")
		}
	case "K-20":
		countries := make(map[string]bool)
		for _, country := range b.Citizenships {
			if country != "" {
				countries[country] = true
			}
		}
		matched = len(countries) > 1
		if matched {
			add("citizenship_count", fmt.Sprint(len(countries)), "person")
		}
	}
	if !matched {
		return []Reason{}, nil
	}
	if c.code >= "K-08" && c.code <= "K-11" {
		add("classification", "identity_error_warning_not_coi-1", "engine")
	}
	return []Reason{{
		Code: c.code, Title: c.title, Weight: c.weight,
		Severity: c.severity, Facts: facts,
	}}, nil
}

// Registry returns the fixed execution order K-01 through K-20.
func Registry() []Criterion {
	return []Criterion{
		criterion{"K-01", "Родство DLTO ↔ лицо, подтверждённое ЗАГС", 100, SeverityCritical, Active},
		criterion{"K-02", "Совместное проживание (один адрес)", 90, SeverityCritical, Active},
		criterion{"K-03", "Развод между DLTO и лицом", 80, SeverityHigh, Active},
		criterion{"K-04", "Общий ребёнок", 70, SeverityHigh, Active},
		criterion{"K-05", "Косвенное родство через общего родственника", 50, SeverityMedium, Active},
		criterion{"K-06", "Опека/попечительство DLTO ↔ лицо", 85, SeverityCritical, RequiresSource},
		criterion{"K-07", "Эмансипация связанного лица", 40, SeverityMedium, Active},
		criterion{"K-08", "Полное совпадение ФИО", 30, SeverityWarning, Active},
		criterion{"K-09", "Совпадение ФИО + дата рождения", 45, SeverityWarning, Active},
		criterion{"K-10", "Совпадение фамилии", 10, SeverityInfo, Active},
		criterion{"K-11", "Совпадение инициалов + город", 10, SeverityInfo, Active},
		criterion{"K-12", "Один дом / улица (соседство)", 25, SeverityLow, Active},
		criterion{"K-13", "Земляки", 10, SeverityInfo, Active},
		criterion{"K-14", "Один административный район", 0, SeverityNone, RequiresSource},
		criterion{"K-15", "Совместное проживание в прошлом", 0, SeverityNone, RequiresSource},
		criterion{"K-16", "Лишение родительских прав у лица", 15, SeverityContextual, Active},
		criterion{"K-17", "Недееспособность/ограничение у лица", 15, SeverityContextual, Active},
		criterion{"K-18", "Статус безработного у лица", 10, SeverityContextual, Active},
		criterion{"K-19", "Общий ОГРНИП (аномалия реестра)", 55, SeverityMedium, Active},
		criterion{"K-20", "Двойное гражданство проверяемого лица", 5, SeverityInfo, Active},
	}
}

type Engine struct {
	version  string
	registry []Criterion
}

func NewEngine(version string) (*Engine, error) {
	if version != "rules_v1.0.0" {
		return nil, errors.New("unsupported rule version")
	}
	return &Engine{version: version, registry: Registry()}, nil
}

func (e *Engine) Version() string { return e.version }

func rank(s Severity) int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityLow:
		return 3
	case SeverityMedium:
		return 4
	case SeverityHigh:
		return 5
	case SeverityCritical:
		return 6
	default:
		return 0
	}
}

func promote(s Severity) Severity {
	switch s {
	case SeverityNone, SeverityInfo:
		return SeverityWarning
	case SeverityWarning:
		return SeverityLow
	case SeverityLow:
		return SeverityMedium
	case SeverityMedium:
		return SeverityHigh
	case SeverityHigh, SeverityCritical:
		return SeverityCritical
	default:
		return s
	}
}

func (e *Engine) Evaluate(ctx context.Context, s Snapshot) (Signal, error) {
	if s.OrderID == "" || s.DLTO.ID == "" || s.Person.ID == "" || s.Now.IsZero() {
		return Signal{}, errors.New("incomplete snapshot")
	}
	id := s.Person.ID
	signal := Signal{
		OrderID: s.OrderID, DLTOID: s.DLTO.ID, PersonID: &id,
		RuleVersion: e.version, GeneratedAt: s.Now,
		Severity: SeverityNone, Reasons: []Reason{},
	}
	contextual, primary := false, false
	for _, c := range e.registry {
		reasons, err := c.Evaluate(ctx, s)
		if err != nil {
			return Signal{}, fmt.Errorf("evaluate %s: %w", c.Code(), err)
		}
		for _, r := range reasons {
			contextual = contextual || r.Severity == SeverityContextual
			primary = primary || r.Code == "K-01" || r.Code == "K-02" || r.Code == "K-03"
			level := r.Severity
			if level == SeverityContextual {
				level = SeverityInfo
			}
			if rank(level) > rank(signal.Severity) {
				signal.Severity = level
			}
			signal.Reasons = append(signal.Reasons, r)
		}
	}
	if contextual && primary {
		signal.Severity = promote(signal.Severity)
	}
	if s.IdentityMethod != "" {
		for i := range signal.Reasons {
			signal.Reasons[i].Facts = append(signal.Reasons[i].Facts, Fact{
				Key: "identity_method", Value: s.IdentityMethod, Source: "identity",
			})
		}
	}
	sort.SliceStable(signal.Reasons, func(i, j int) bool {
		a, b := signal.Reasons[i], signal.Reasons[j]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return a.Code < b.Code
	})
	return signal, nil
}

func (e *Engine) NoData(orderID, dltoID, reason string, now time.Time) Signal {
	return Signal{
		OrderID: orderID, DLTOID: dltoID, RuleVersion: e.version,
		Severity: SeverityNoData, GeneratedAt: now,
		Reasons: []Reason{{
			Code: "IDENTITY_NOT_FOUND", Title: "Проверяемое лицо не найдено в ЕРН",
			Severity: SeverityNoData,
			Facts: []Fact{
				{Key: "reason", Value: reason},
				{Key: "hint", Value: "требуется ручная проверка"},
			},
		}},
	}
}
