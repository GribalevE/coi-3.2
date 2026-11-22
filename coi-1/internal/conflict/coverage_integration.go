// internal/conflict/coverage_integration.go

package conflict

import "context"

// CoverageAwareSnapshot расширяет Snapshot отчётом покрытия.
// Встраивание (embedding) сохраняет совместимость с уже написанными
// вызовами Engine.Evaluate — они продолжат работать через базовый Snapshot.
type CoverageAwareSnapshot struct {
	Snapshot
	Coverage CoverageReport
}

// EvaluateWithCoverage запускает те же критерии, что Evaluate, но пропускает
// те, для которых покрытие источников неполно.
//
// Ключевое правило: если правило требует источник, которого нет в книге,
// мы НЕ возвращаем «не сработало». Мы просто не включаем его в результат
// и фиксируем это в CoverageReport.
func (e *Engine) EvaluateWithCoverage(
	ctx context.Context,
	s CoverageAwareSnapshot,
) (Signal, error) {
	signal, err := e.Evaluate(ctx, s.Snapshot)
	if err != nil {
		return Signal{}, err
	}
	filtered := make([]Reason, 0, len(signal.Reasons))
	for _, r := range signal.Reasons {
		if s.Coverage.CanEvaluate(r.Code) {
			filtered = append(filtered, r)
		}
	}
	signal.Reasons = filtered
	signal.Severity = recomputeSeverity(signal.Reasons)
	cov := s.Coverage
	signal.Coverage = &cov
	return signal, nil
}

// recomputeSeverity — детерминированный пересчёт верхнего уровня
// по списку причин.
func recomputeSeverity(reasons []Reason) Severity {
	rank := map[Severity]int{
		SeverityNone: 0, SeverityInfo: 1, SeverityWarning: 2,
		SeverityLow: 3, SeverityMedium: 4, SeverityHigh: 5, SeverityCritical: 6,
	}
	best := SeverityNone
	for _, r := range reasons {
		level := r.Severity
		if level == SeverityContextual {
			level = SeverityInfo
		}
		if rank[level] > rank[best] {
			best = level
		}
	}
	return best
}