// internal/api/shift.go

package api

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"time"

	"coi-1/internal/data"
	"coi-1/internal/identity"
)

// shiftSize — сколько должностных лиц заступает на смену.
const shiftSize = 4

// shiftMember — участник смены в ответе интерфейсу.
//
// Паспорт отдаётся только для четырёх участников смены и только здесь:
// оператор должен видеть, чьи данные подставляются в поручение. В ответ
// /api/check и в журнал паспорт по-прежнему не попадает.
type shiftMember struct {
	ID       string `json:"id"`
	FullName string `json:"full_name"`
	Post     string `json:"post,omitempty"`
	Passport string `json:"passport,omitempty"`
}

type shiftResponse struct {
	OK        bool          `json:"ok"`
	Error     string        `json:"error,omitempty"`
	Members   []shiftMember `json:"members,omitempty"`
	StartedAt string        `json:"started_at,omitempty"`
}

// shift формирует состав смены: четыре случайных должностных лица из реестра.
//
// Выбор равновероятный и без повторов — криптографический источник
// случайности, частичная тасовка Фишера — Йетса.
func (h *handler) shift(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	officials, err := h.cfg.Store.Officials(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError,
			shiftResponse{Error: "Реестр должностных лиц недоступен."})
		return
	}
	if len(officials) < shiftSize {
		writeJSON(w, http.StatusConflict, shiftResponse{
			Error: "В реестре меньше четырёх должностных лиц — смена не может быть сформирована.",
		})
		return
	}

	members := make([]shiftMember, 0, shiftSize)
	for _, p := range pickRandom(officials, shiftSize) {
		members = append(members, shiftMember{
			ID:       p.ID,
			FullName: p.FullName,
			Post:     p.Post,
			Passport: p.Passport,
		})
	}

	writeJSON(w, http.StatusOK, shiftResponse{
		OK:        true,
		Members:   members,
		StartedAt: h.cfg.Now().Format("02.01.2006 15:04"),
	})
}

// officialRef восстанавливает идентичность должностного лица по идентификатору
// из состава смены. Паспорт остаётся на сервере: он нужен только матчеру.
func (h *handler) officialRef(ctx context.Context, id string) (identity.Reference, bool) {
	officials, err := h.cfg.Store.Officials(ctx)
	if err != nil {
		return identity.Reference{}, false
	}
	for _, p := range officials {
		if p.ID != id {
			continue
		}
		return identity.Reference{
			Passport: p.Passport,
			FullName: p.FullName,
			Address:  p.Address,
		}, true
	}
	return identity.Reference{}, false
}

// pickRandom возвращает n разных элементов в случайном порядке.
func pickRandom(list []data.Person, n int) []data.Person {
	if n > len(list) {
		n = len(list)
	}
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < n; i++ {
		j := i + randIndex(len(idx)-i)
		idx[i], idx[j] = idx[j], idx[i]
	}

	out := make([]data.Person, 0, n)
	for _, k := range idx[:n] {
		out = append(out, list[k])
	}
	return out
}

// randIndex возвращает целое в диапазоне [0, max).
func randIndex(max int) int {
	if max <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// Отказ источника случайности не должен ломать смену: смещение
		// по времени остаётся случайным для демонстрационного набора.
		return int(time.Now().UnixNano() % int64(max))
	}
	return int(v.Int64())
}
