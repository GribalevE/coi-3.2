// internal/api/web.go

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"coi-1/internal/conflict"
	"coi-1/internal/identity"
)

type webCheckRequest struct {
	DLTO   identity.Reference `json:"dlto"`
	Person identity.Reference `json:"person"`
	// DLTOSource — идентификатор должностного лица из состава смены.
	// Если задан, сведения о должностном лице берутся из реестра,
	// а паспорт в браузер не передаётся.
	DLTOSource string `json:"dlto_source,omitempty"`
}

type personView struct {
	ID       string `json:"id"`
	FullName string `json:"full_name"`
	Role     string `json:"role,omitempty"`
	Address  string `json:"address,omitempty"`
	Post     string `json:"post,omitempty"`
}

type webCheckResponse struct {
	OK            bool                     `json:"ok"`
	Error         string                   `json:"error,omitempty"`
	OrderID       string                   `json:"order_id,omitempty"`
	Justification string                   `json:"justification,omitempty"`
	Conclusion    string                   `json:"conclusion,omitempty"`
	Signal        *conflict.Signal         `json:"signal,omitempty"`
	Coverage      *conflict.CoverageReport `json:"coverage,omitempty"`
	DLTO          *personView              `json:"dlto,omitempty"`
	Person        *personView              `json:"person,omitempty"`
	RuleVersion   string                   `json:"rule_version,omitempty"`
	CheckedAt     string                   `json:"checked_at,omitempty"`
}

// webCheck обслуживает демонстрационный интерфейс.
//
// Покрытие источников учитывается (EvaluateWithCoverage), ошибки обмена не
// подавляются, у проверки есть собственный таймаут, паспортные данные в ответ
// не попадают.
func (h *handler) webCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req webCheckRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.webFail(w, http.StatusBadRequest,
			"Некорректный запрос: ожидается JSON с полями dlto и person.")
		return
	}

	matcher := identity.Matcher{Registry: h.cfg.Store}

	// Должностное лицо могло быть выбрано из состава смены: тогда его
	// идентичность восстанавливается по идентификатору, а не по форме.
	dltoRef := req.DLTO
	if req.DLTOSource != "" {
		ref, ok := h.officialRef(ctx, req.DLTOSource)
		if !ok {
			h.webFail(w, http.StatusBadRequest,
				"Должностное лицо не найдено в реестре должностных лиц таможенного органа.")
			return
		}
		dltoRef = ref
	}

	dltoRes, err := matcher.Match(ctx, dltoRef)
	if err != nil {
		h.webFail(w, http.StatusInternalServerError, "Поиск в реестре ЕРН не выполнен.")
		return
	}
	if dltoRes.Person == nil {
		h.webFail(w, http.StatusBadRequest,
			"Должностное лицо не идентифицировано: "+humanReason(dltoRes.Reason)+".")
		return
	}
	if dltoRes.Person.Role != "DLTO" {
		h.webFail(w, http.StatusBadRequest,
			"Указанное лицо не найдено в реестре должностных лиц таможенного органа.")
		return
	}

	personRes, err := matcher.Match(ctx, req.Person)
	if err != nil {
		h.webFail(w, http.StatusInternalServerError, "Поиск в реестре ЕРН не выполнен.")
		return
	}
	if personRes.Person == nil {
		h.webFail(w, http.StatusBadRequest,
			"Проверяемое лицо не идентифицировано: "+humanReason(personRes.Reason)+".")
		return
	}
	if personRes.Person.Role == "DLTO" {
		h.webFail(w, http.StatusBadRequest,
			"Проверяемое лицо является должностным лицом таможенного органа: "+
				"проверка такой пары не выполняется.")
		return
	}

	now := h.cfg.Now().UTC()
	orderID := "WEB-" + now.Format("20060102-150405.000000000")

	// person_ref — идентификатор лица, а не паспортные данные.
	token, err := h.cfg.Store.StartCheck(ctx, orderID, dltoRes.Person.ID,
		personRes.Person.ID, h.cfg.Engine.Version(), now)
	if err != nil {
		h.webFail(w, http.StatusInternalServerError,
			"Не удалось зарегистрировать проверку в журнале.")
		return
	}

	start := time.Now()
	snapshot, err := h.webSnapshot(ctx, orderID, dltoRes.Person.ID,
		personRes.Person.ID, personRes.Method, now)
	if err != nil {
		h.webFailAfter(w, ctx, orderID, token, "smev_error", start,
			http.StatusBadGateway, "Обмен с СМЭВ не завершён: "+err.Error()+".")
		return
	}

	coverage := h.buildCoverage()
	aware := conflict.CoverageAwareSnapshot{Snapshot: snapshot, Coverage: coverage}
	sig, err := h.cfg.Engine.EvaluateWithCoverage(ctx, aware)
	if err != nil {
		h.webFailAfter(w, ctx, orderID, token, "engine_error", start,
			http.StatusInternalServerError, "Проверка не завершена.")
		return
	}

	latency := time.Since(start).Milliseconds()
	if _, err := h.cfg.Store.CompleteCheck(ctx, token, sig, latency, "ok"); err != nil {
		h.webFail(w, http.StatusInternalServerError,
			"Не удалось записать результат проверки в журнал.")
		return
	}

	justification, conclusion := justify(orderID, sig, snapshot.DLTO, snapshot.Person)

	writeJSON(w, http.StatusOK, webCheckResponse{
		OK:            true,
		OrderID:       orderID,
		Justification: justification,
		Conclusion:    conclusion,
		Signal:        &sig,
		Coverage:      sig.Coverage,
		DLTO: &personView{
			ID:       snapshot.DLTO.ID,
			FullName: snapshot.DLTO.FullName,
			Role:     snapshot.DLTO.Role,
			Post:     snapshot.DLTO.Post,
		},
		Person: &personView{
			ID:       snapshot.Person.ID,
			FullName: snapshot.Person.FullName,
			Role:     snapshot.Person.Role,
			Address:  snapshot.Person.Address,
		},
		RuleVersion: sig.RuleVersion,
		CheckedAt:   now.Format("02.01.2006 15:04:05") + " UTC",
	})
}

// webSnapshot забирает все сведения по паре. Ошибка любого источника
// возвращается наверх: неполный снимок не выдаётся за полный.
func (h *handler) webSnapshot(
	ctx context.Context, orderID, dltoID, personID, method string, now time.Time,
) (conflict.Snapshot, error) {
	dlto, err := h.cfg.SMEV.FetchPerson(ctx, dltoID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о должностном лице: %w", err)
	}
	person, err := h.cfg.SMEV.FetchPerson(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о проверяемом лице: %w", err)
	}
	kinship, err := h.cfg.SMEV.FetchKinship(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о родстве: %w", err)
	}
	divorces, err := h.cfg.SMEV.FetchDivorces(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о расторжении брака: %w", err)
	}
	capacity, err := h.cfg.SMEV.FetchCapacity(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о дееспособности: %w", err)
	}
	parental, err := h.cfg.SMEV.FetchParental(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения о родительских правах: %w", err)
	}
	employment, err := h.cfg.SMEV.FetchEmployment(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("сведения службы занятости: %w", err)
	}
	dltoTax, err := h.cfg.SMEV.FetchTax(ctx, dltoID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("налоговый учёт должностного лица: %w", err)
	}
	personTax, err := h.cfg.SMEV.FetchTax(ctx, personID)
	if err != nil {
		return conflict.Snapshot{}, fmt.Errorf("налоговый учёт проверяемого лица: %w", err)
	}

	return conflict.Snapshot{
		Now:            now,
		OrderID:        orderID,
		DLTO:           dlto,
		Person:         person,
		Kinship:        kinship,
		Divorces:       divorces,
		Capacity:       capacity,
		Parental:       parental,
		Employment:     employment,
		DLTOTax:        dltoTax,
		PersonTax:      personTax,
		IdentityMethod: method,
	}, nil
}

// webFailAfter фиксирует неуспешную проверку в журнале и отвечает клиенту.
func (h *handler) webFailAfter(
	w http.ResponseWriter, ctx context.Context, orderID, token, status string,
	start time.Time, code int, msg string,
) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := h.cfg.Store.FailCheck(auditCtx, orderID, token, status,
		h.cfg.Engine.Version(), h.cfg.Now().UTC(), time.Since(start).Milliseconds()); err != nil {
		log.Printf("web check: fail audit order_id=%q: %v", orderID, err)
	}
	h.webFail(w, code, msg)
}

func (h *handler) webFail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, webCheckResponse{OK: false, Error: msg})
}
