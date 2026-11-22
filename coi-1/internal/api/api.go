// internal/api/api.go

// Package api exposes authenticated REST handlers for AIS integration.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"coi-1/internal/audit"
	"coi-1/internal/conflict"
	"coi-1/internal/data"
	"coi-1/internal/identity"
	"coi-1/internal/smev"
	"coi-1/internal/store"
)

type Config struct {
	Store       *store.Store
	Audit       audit.Repository
	SMEV        smev.Client
	Engine      *conflict.Engine
	Token       string
	Now         func() time.Time
	SMEVTimeout time.Duration
	WebFS       fs.FS        // embedded web/ directory; if nil, web UI is disabled
	Admin       http.Handler // раздел управления реестром должностных ли
}

type handler struct {
	cfg Config
}

type checkRequest struct {
	DLTOID string             `json:"dlto_id"`
	Person identity.Reference `json:"person"`
}

func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SMEVTimeout <= 0 {
		cfg.SMEVTimeout = 2500 * time.Millisecond
	}
	if cfg.Audit == nil {
		cfg.Audit = cfg.Store
	}
	h := &handler{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"rule_version": cfg.Engine.Version()})
	})
	mux.HandleFunc("POST /v1/ldt/orders/{order_id}/check", h.check)
	mux.HandleFunc("PATCH /v1/ldt/orders/{order_id}/decision", h.decision)
	mux.HandleFunc("GET /v1/ldt/orders/{order_id}/audit", h.audit)

	// --- web UI ---
	mux.HandleFunc("POST /api/check", h.webCheck)
	mux.HandleFunc("GET /api/shift", h.shift)
	// --- раздел управления реестром должностных лиц ---
	if cfg.Admin != nil {
		// Методы указаны явно: без них "/admin/" конфликтует с "GET /" —
		// ни один из шаблонов не является более специфичным.
		mux.Handle("GET /admin", cfg.Admin)
		mux.Handle("POST /admin", cfg.Admin)
		mux.Handle("GET /admin/", cfg.Admin)
		mux.Handle("POST /admin/", cfg.Admin)
	}

	if cfg.WebFS != nil {
		mux.Handle("GET /", http.FileServer(http.FS(cfg.WebFS)))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			expected := "Bearer " + cfg.Token
			if cfg.Token == "" ||
				subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "Требуется авторизация AIS")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "serialization error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(raw, '\n')); err != nil {
		log.Printf("response write failed")
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "content_type", "Требуется application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "body_too_large", "Слишком большой запрос")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid_json", "Некорректный JSON")
		}
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid_json", "Ожидается один JSON-объект")
		return false
	}
	return true
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := h.cfg.Store.Ready(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "Хранилище не готово")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	orderID := r.PathValue("order_id")
	if !validID(orderID) {
		writeErr(w, http.StatusBadRequest, "invalid_order_id", "Некорректный order_id")
		return
	}
	var req checkRequest
	if !decode(w, r, &req) {
		return
	}
	if !validID(req.DLTOID) ||
		(req.Person.Passport == "" && req.Person.SNILS == "" && req.Person.INN == "" &&
			(req.Person.FullName == "" || req.Person.Address == "")) {
		writeErr(w, http.StatusBadRequest, "validation", "Не заданы исполнитель или идентификаторы")
		return
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.Token))
	mac.Write([]byte(req.Person.Passport))
	log.Printf("check order_id=%q passport_hash=%s",
		orderID, hex.EncodeToString(mac.Sum(nil)))

	raw, err := json.Marshal(req.Person)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "serialization", "Ошибка сериализации")
		return
	}
	now := h.cfg.Now().UTC()
	start := time.Now()
	token, err := h.cfg.Store.StartCheck(ctx, orderID, req.DLTOID,
		string(raw), h.cfg.Engine.Version(), now)
	if err != nil {
		h.storeError(w, err)
		return
	}
	finished := false
	failureStatus := "upstream_error"
	defer func() {
		if finished {
			return
		}
		cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cleanupCancel()
		if err := h.cfg.Store.FailCheck(cleanup, orderID, token, failureStatus,
			h.cfg.Engine.Version(), h.cfg.Now(), time.Since(start).Milliseconds()); err != nil {
			log.Printf("check failure audit failed order_id=%q", orderID)
		}
	}()

	result, err := (identity.Matcher{Registry: h.cfg.Store}).Match(ctx, req.Person)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity_error", "Ошибка реестра идентификации")
		return
	}
	var signal conflict.Signal
	smevStatus := "ok"
	if result.Person == nil {
		signal = h.cfg.Engine.NoData(orderID, req.DLTOID, result.Reason, now)
		smevStatus = "no_data"
	} else {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, h.cfg.SMEVTimeout)
		snapshot, fetchErr := h.fetch(fetchCtx, orderID, req.DLTOID,
			result.Person.ID, result.Method, now)
		fetchCancel()
		if fetchErr != nil {
			failureStatus = "upstream_error"
			if errors.Is(fetchErr, smev.ErrTimeout) ||
				errors.Is(fetchErr, context.DeadlineExceeded) ||
				errors.Is(fetchErr, context.Canceled) {
				failureStatus = "timeout"
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]string{
					"code": "smev_error", "message": "Обмен SMEV не завершён",
					"status": failureStatus,
				},
			})
			return
		}
		coverage := h.buildCoverage()
		aware := conflict.CoverageAwareSnapshot{
			Snapshot: snapshot,
			Coverage: coverage,
		}
		signal, err = h.cfg.Engine.EvaluateWithCoverage(ctx, aware)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "evaluation_error", "Проверка не завершена")
			return
		}
	}
	auditID, err := h.cfg.Store.CompleteCheck(ctx, token, signal,
		time.Since(start).Milliseconds(), smevStatus)
	if err != nil {
		h.storeError(w, err)
		return
	}
	finished = true
	writeJSON(w, http.StatusOK, map[string]any{
		"signal": signal, "audit_id": auditID, "rule_version": h.cfg.Engine.Version(),
	})
}

func (h *handler) fetch(
	ctx context.Context, orderID, dlto, person, method string, now time.Time,
) (conflict.Snapshot, error) {
	type result struct {
		what string
		err  error
	}
	results := make(chan result, 9)

	var dltoDTO, personDTO smev.PersonDTO
	var kinship []smev.KinshipDTO
	var divorces []smev.DivorceDTO
	var capacity smev.CapacityDTO
	var parental []smev.ParentalDTO
	var employment smev.EmploymentDTO
	var dltoTax, personTax smev.TaxDTO

	go func() { p, err := h.cfg.SMEV.FetchPerson(ctx, dlto); dltoDTO = p; results <- result{"dlto", err} }()
	go func() { p, err := h.cfg.SMEV.FetchPerson(ctx, person); personDTO = p; results <- result{"person", err} }()
	go func() { k, err := h.cfg.SMEV.FetchKinship(ctx, person); kinship = k; results <- result{"kinship", err} }()
	go func() {
		d, err := h.cfg.SMEV.FetchDivorces(ctx, person)
		divorces = d
		results <- result{"divorces", err}
	}()
	go func() {
		c, err := h.cfg.SMEV.FetchCapacity(ctx, person)
		capacity = c
		results <- result{"capacity", err}
	}()
	go func() {
		p, err := h.cfg.SMEV.FetchParental(ctx, person)
		parental = p
		results <- result{"parental", err}
	}()
	go func() {
		e, err := h.cfg.SMEV.FetchEmployment(ctx, person)
		employment = e
		results <- result{"employment", err}
	}()
	go func() { t, err := h.cfg.SMEV.FetchTax(ctx, dlto); dltoTax = t; results <- result{"dlto_tax", err} }()
	go func() {
		t, err := h.cfg.SMEV.FetchTax(ctx, person)
		personTax = t
		results <- result{"person_tax", err}
	}()

	const expected = 9
	for i := 0; i < expected; i++ {
		select {
		case <-ctx.Done():
			return conflict.Snapshot{}, ctx.Err()
		case r := <-results:
			if r.err != nil {
				return conflict.Snapshot{}, fmt.Errorf("smev %s: %w", r.what, r.err)
			}
		}
	}
	return conflict.Snapshot{
		Now:            now,
		OrderID:        orderID,
		DLTO:           personFromDTO(dltoDTO),
		Person:         personFromDTO(personDTO),
		Kinship:        toKinship(kinship),
		Divorces:       toDivorces(divorces),
		Capacity:       capacity,
		Parental:       parental,
		Employment:     employment,
		DLTOTax:        dltoTax,
		PersonTax:      personTax,
		IdentityMethod: method,
	}, nil
}

func (h *handler) buildCoverage() conflict.CoverageReport {
	sources := []conflict.SourceCoverage{
		{Source: "person", State: conflict.SourceAvailable, Detail: "ЕРН"},
		{Source: "kinship", State: conflict.SourceAvailable, Detail: "Связи"},
		{Source: "civil_registry", State: conflict.SourceAvailable, Detail: "ЗАГС"},
		{Source: "divorce", State: conflict.SourceAvailable, Detail: "Разводы"},
		{Source: "emancipation", State: conflict.SourceAvailable, Detail: "Эмансипация"},
		{Source: "tax_registration", State: conflict.SourceAvailable, Detail: "Налоги"},
		{Source: "parental_rights", State: conflict.SourceUnavailable, Detail: "лист отсутствует"},
		{Source: "legal_capacity", State: conflict.SourceUnavailable, Detail: "лист отсутствует"},
		{Source: "employment_service", State: conflict.SourceUnavailable, Detail: "лист отсутствует"},
		{Source: "post_district", State: conflict.SourceUnavailable, Detail: "справочник не предоставлен"},
	}
	report, err := conflict.BuildCoverage(sources)
	if err != nil {
		log.Printf("coverage build failed: %v", err)
		return conflict.CoverageReport{Complete: true}
	}
	return report
}

func (h *handler) decision(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("order_id")
	if !validID(orderID) {
		writeErr(w, http.StatusBadRequest, "invalid_order_id", "Некорректный order_id")
		return
	}
	var d store.Decision
	if !decode(w, r, &d) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	auditID, err := h.cfg.Store.Decide(ctx, orderID, d, h.cfg.Now().UTC())
	if err != nil {
		h.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   d.Decision,
		"audit_id": auditID,
	})
}

func (h *handler) audit(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("order_id")
	if !validID(orderID) {
		writeErr(w, http.StatusBadRequest, "invalid_order_id", "Некорректный order_id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, err := h.cfg.Audit.Audit(ctx, orderID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "audit_error", "Ошибка чтения аудита")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows})
}

func (h *handler) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "Объект не найден")
	case errors.Is(err, store.ErrInvalidTransition):
		writeErr(w, http.StatusConflict, "invalid_transition", "Недопустимый переход состояния")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "order_conflict", "Поручение уже проверено или занято")
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
	}
}

func personFromDTO(p smev.PersonDTO) data.Person {
	return data.Person{
		ID:              p.ID,
		Role:            p.Role,
		FullName:        p.FullName,
		Passport:        p.Passport,
		Address:         p.Address,
		City:            p.City,
		Street:          p.Street,
		House:           p.House,
		BirthDate:       p.BirthDate,
		SNILS:           p.SNILS,
		INN:             p.INN,
		Citizenships:    append([]string(nil), p.Citizenships...),
		PostCity:        p.PostCity,
		DistrictStreets: append([]string(nil), p.DistrictStreets...),
		Emancipated:     p.Emancipated,
	}
}

func toKinship(in []smev.KinshipDTO) []data.Kinship {
	out := make([]data.Kinship, len(in))
	for i, k := range in {
		out[i] = data.Kinship(k)
	}
	return out
}

func toDivorces(in []smev.DivorceDTO) []data.Divorce {
	out := make([]data.Divorce, len(in))
	for i, d := range in {
		out[i] = data.Divorce(d)
	}
	return out
}
