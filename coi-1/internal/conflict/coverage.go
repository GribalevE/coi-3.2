// internal/conflict/coverage.go

package conflict

import (
	"errors"
	"sort"
)

type SourceState string

const (
	SourceAvailable   SourceState = "available"
	SourceUnavailable SourceState = "unavailable"
	SourceFailed      SourceState = "failed"
)

type SourceCoverage struct {
	Source string      `json:"source"`
	State  SourceState `json:"state"`
	Detail string      `json:"detail"`
}

type RuleGap struct {
	Code    string   `json:"code"`
	Sources []string `json:"sources"`
}

type CoverageReport struct {
	Complete bool             `json:"complete"`
	Sources  []SourceCoverage `json:"sources"`
	RuleGaps []RuleGap        `json:"rule_gaps"`
}

type sourceRequirement struct {
	code    string
	sources []string
}

func sourceRequirements() []sourceRequirement {
	return []sourceRequirement{
		{"K-01", []string{"person", "kinship", "civil_registry"}},
		{"K-02", []string{"person"}},
		{"K-03", []string{"divorce"}},
		{"K-04", []string{"kinship"}},
		{"K-05", []string{"kinship"}},
		{"K-07", []string{"kinship", "emancipation"}},
		{"K-08", []string{"person"}},
		{"K-09", []string{"person"}},
		{"K-10", []string{"person"}},
		{"K-11", []string{"person"}},
		{"K-12", []string{"person"}},
		{"K-13", []string{"person", "post_district"}},
		{"K-16", []string{"parental_rights"}},
		{"K-17", []string{"legal_capacity"}},
		{"K-18", []string{"employment_service"}},
		{"K-19", []string{"tax_registration"}},
		{"K-20", []string{"person"}},
	}
}

func BuildCoverage(sources []SourceCoverage) (CoverageReport, error) {
	report := CoverageReport{
		Complete: true,
		Sources:  append([]SourceCoverage{}, sources...),
		RuleGaps: []RuleGap{},
	}
	states := make(map[string]SourceState, len(sources))
	for _, source := range sources {
		if source.Source == "" {
			return CoverageReport{}, errors.New("coverage source must not be empty")
		}
		if _, exists := states[source.Source]; exists {
			return CoverageReport{}, errors.New("duplicate coverage source")
		}
		switch source.State {
		case SourceAvailable, SourceUnavailable, SourceFailed:
		default:
			return CoverageReport{}, errors.New("invalid coverage source state")
		}
		states[source.Source] = source.State
	}
	sort.Slice(report.Sources, func(i, j int) bool {
		return report.Sources[i].Source < report.Sources[j].Source
	})
	for _, requirement := range sourceRequirements() {
		missing := make([]string, 0)
		for _, source := range requirement.sources {
			if states[source] != SourceAvailable {
				missing = append(missing, source)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		report.Complete = false
		report.RuleGaps = append(report.RuleGaps, RuleGap{
			Code: requirement.code, Sources: missing,
		})
	}
	return report, nil
}

func (r CoverageReport) CanEvaluate(code string) bool {
	known := false
	for _, requirement := range sourceRequirements() {
		if requirement.code == code {
			known = true
			break
		}
	}
	if !known {
		return false
	}
	for _, gap := range r.RuleGaps {
		if gap.Code == code {
			return false
		}
	}
	return true
}