// Package httpapi — HTTP-интерфейс сервиса датасета: /health,
// /api/v1/meta и /api/v1/bundle. Только чтение, без авторизации:
// сервис живёт во внутреннем контуре.
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"ern-dataset/internal/dataset"
)

// Config — зависимости сервера.
type Config struct {
	Store   *dataset.Store
	Version string
	Now     func() time.Time
}

type server struct {
	cfg Config
}

// New собирает маршруты и оборачивает их журналом запросов.
func New(cfg Config) (http.Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("httpapi: не передан Store")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/v1/meta", s.meta)
	mux.HandleFunc("GET /api/v1/bundle", s.bundle)
	mux.HandleFunc("/", s.notFound)

	return withLogging(mux), nil
}

// health — проверка живости вместе с состоянием датасета: если файл пропал
// или перестал разбираться, сервис честно отвечает 503.
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	raw, info, err := s.cfg.Store.Read()
	if err != nil {
		log.Printf("health: %v", err)
		writeError(w, http.StatusServiceUnavailable, "датасет недоступен")
		return
	}
	counts, err := dataset.Parse(raw)
	if err != nil {
		log.Printf("health: %v", err)
		writeError(w, http.StatusServiceUnavailable, "датасет не разбирается: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
		"file":    info.File,
		"size":    info.Size,
		"sha256":  info.SHA256,
		"counts":  counts,
		"checked": s.cfg.Now().UTC(),
	})
}

// meta — сведения о файле: размер, время изменения, контрольная сумма и
// количество записей. По SHA-256 видно, тот ли датасет забрал coi-1.
func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	raw, info, err := s.cfg.Store.Read()
	if err != nil {
		log.Printf("meta: %v", err)
		writeError(w, http.StatusInternalServerError, "не удалось прочитать датасет")
		return
	}
	counts, err := dataset.Parse(raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "датасет не разбирается")
		return
	}
	info.Counts = counts
	writeJSON(w, http.StatusOK, info)
}

// bundle — сам датасет. ETag считается по контрольной сумме файла, поэтому
// после замены файла меняется автоматически; If-None-Match даёт 304.
func (s *server) bundle(w http.ResponseWriter, r *http.Request) {
	raw, info, err := s.cfg.Store.Read()
	if err != nil {
		log.Printf("bundle: %v", err)
		writeError(w, http.StatusInternalServerError, "не удалось прочитать датасет")
		return
	}

	etag := `"` + short(info.SHA256) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Dataset-SHA256", info.SHA256)
	h.Set("X-Dataset-Version", s.cfg.Version)
	if _, err := w.Write(raw); err != nil {
		log.Printf("bundle: запись ответа: %v", err)
	}
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "неизвестный маршрут: "+r.URL.Path)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func short(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16]
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status,
			time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
