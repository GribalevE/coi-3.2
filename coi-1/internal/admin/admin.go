// internal/admin/admin.go

// Package admin — управление реестром должностных лиц через веб-страницу.
//
// Авторизация: логин и пароль из конфигурации (по умолчанию admin / 123 —
// только для демонстрации, см. README). Сессия — подписанная cookie.
//
// Любое изменение пишется в два места: файл датасета должностных лиц (источник
// истины для следующего запуска) и журнал SQLite (нужен для ссылочной
// целостности поручений).
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"coi-1/internal/data"
	"coi-1/internal/store"
)

const (
	sessionCookie = "coi_admin"
	sessionTTL    = 30 * time.Minute
)

type Config struct {
	Store *store.Store
	Set   *data.DLTOSet
	Path  string
	User  string
	Pass  string
}

type Handler struct {
	cfg    Config
	mu     sync.Mutex
	secret []byte
	now    func() time.Time
}

// New создаёт обработчик. Реестр передаётся указателем: изменения держим в
// памяти и сохраняем на диск.
func New(cfg Config) (*Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("admin: store is required")
	}
	if cfg.Set == nil {
		return nil, errors.New("admin: officials set is required")
	}
	if cfg.Path == "" {
		return nil, errors.New("admin: dataset path is required")
	}
	if cfg.User == "" || cfg.Pass == "" {
		return nil, errors.New("admin: credentials are required")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("admin: secret: %w", err)
	}
	return &Handler{cfg: cfg, secret: secret, now: time.Now}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/admin", "/admin/":
		if !h.authorized(r) {
			h.renderLogin(w, http.StatusOK, "")
			return
		}
		h.renderPanel(w, http.StatusOK, "", "")

	case "/admin/login":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
		h.login(w, r)

	case "/admin/logout":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
		h.logout(w, r)

	case "/admin/dltos":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
		if !h.authorized(r) {
			h.renderLogin(w, http.StatusUnauthorized, "Требуется вход.")
			return
		}
		h.save(w, r)

	case "/admin/dltos/delete":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
		if !h.authorized(r) {
			h.renderLogin(w, http.StatusUnauthorized, "Требуется вход.")
			return
		}
		h.remove(w, r)

	default:
		http.NotFound(w, r)
	}
}

/* ---------- авторизация ---------- */

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, http.StatusBadRequest, "Некорректная форма.")
		return
	}
	user := strings.TrimSpace(r.PostFormValue("user"))
	pass := r.PostFormValue("pass")

	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(h.cfg.User)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(h.cfg.Pass)) == 1
	if !userOK || !passOK {
		log.Printf("admin: failed login attempt")
		h.renderLogin(w, http.StatusUnauthorized, "Неверный идентификатор или пароль.")
		return
	}

	exp := h.now().Add(sessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    h.sign(exp),
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) authorized(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return h.verify(c.Value)
}

// Officials возвращает текущий состав реестра. Метод безопасен при
// параллельных вызовах и используется адаптером СМЭВ: должностное лицо,
// добавленное через /admin, должно находиться обменом без перезапуска.
func (h *Handler) Officials() []data.Person {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]data.Person(nil), h.cfg.Set.Persons...)
}

func (h *Handler) sign(exp int64) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%d", exp)
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) verify(value string) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || h.now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%d", exp)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expected))
}

/* ---------- изменение реестра ---------- */

func (h *Handler) save(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderPanel(w, http.StatusBadRequest, "Некорректная форма.", "")
		return
	}

	id := strings.TrimSpace(r.PostFormValue("id"))
	fio := strings.Join(strings.Fields(r.PostFormValue("full_name")), " ")
	passport := strings.TrimSpace(r.PostFormValue("passport"))
	post := strings.TrimSpace(r.PostFormValue("post"))
	address := strings.TrimSpace(r.PostFormValue("address"))

	if id == "" {
		h.renderPanel(w, http.StatusBadRequest, "Укажите идентификатор должностного лица.", "")
		return
	}
	if fio == "" {
		h.renderPanel(w, http.StatusBadRequest, "Укажите фамилию, имя и отчество.", "")
		return
	}
	if len(data.Digits(passport)) != 10 {
		h.renderPanel(w, http.StatusBadRequest,
			"Паспорт должен содержать 10 цифр: серия 4 и номер 6.", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	errMsg := h.applySave(ctx, data.Person{
		ID:       id,
		FullName: fio,
		Passport: passport,
		Post:     post,
		Address:  address,
	})
	if errMsg != "" {
		h.renderPanel(w, http.StatusConflict, errMsg, "")
		return
	}
	h.renderPanel(w, http.StatusOK, "", fmt.Sprintf("Запись %s сохранена.", id))
}

// applySave выполняет изменение под блокировкой и возвращает текст ошибки.
func (h *Handler) applySave(ctx context.Context, p data.Person) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	previous, existed := h.cfg.Set.Get(p.ID)
	candidate := p
	candidate.Role = "DLTO"
	if !existed {
		// Новому должностному лицу нужен признак гражданства РФ: без него
		// идентификация вернёт «лицо не является гражданином РФ».
		candidate.Citizenships = []string{"RF"}
	}

	h.cfg.Set.Upsert(candidate)
	if err := data.SaveDLTOSet(h.cfg.Path, *h.cfg.Set); err != nil {
		h.restore(previous, existed, candidate.ID)
		return "Не удалось сохранить файл датасета: " + err.Error()
	}

	if err := h.cfg.Store.UpsertOfficial(ctx, candidate); err != nil {
		h.restore(previous, existed, candidate.ID)
		if saveErr := data.SaveDLTOSet(h.cfg.Path, *h.cfg.Set); saveErr != nil {
			log.Printf("admin: rollback of %s failed: %v", h.cfg.Path, saveErr)
		}
		return "Не удалось записать должностное лицо в журнал: " + err.Error()
	}
	return ""
}

// restore возвращает запись реестра в прежнее состояние.
// Вызывается только под h.mu.
func (h *Handler) restore(previous data.Person, existed bool, id string) {
	if existed {
		h.cfg.Set.Upsert(previous)
		return
	}
	h.cfg.Set.Delete(id)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderPanel(w, http.StatusBadRequest, "Некорректная форма.", "")
		return
	}
	id := strings.TrimSpace(r.PostFormValue("id"))
	if id == "" {
		h.renderPanel(w, http.StatusBadRequest, "Не указан идентификатор.", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	errMsg := h.applyRemove(ctx, id)
	if errMsg != "" {
		h.renderPanel(w, http.StatusConflict, errMsg, "")
		return
	}
	h.renderPanel(w, http.StatusOK, "", fmt.Sprintf("Запись %s удалена.", id))
}

func (h *Handler) applyRemove(ctx context.Context, id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.cfg.Store.DeleteOfficial(ctx, id); err != nil {
		if errors.Is(err, store.ErrOfficialInUse) {
			return "Запись используется в поручениях и не может быть удалена. " +
				"Изменение сведений доступно, удаление — нет."
		}
		return "Не удалось удалить запись из журнала: " + err.Error()
	}

	previous, existed := h.cfg.Set.Get(id)
	h.cfg.Set.Delete(id)
	if err := data.SaveDLTOSet(h.cfg.Path, *h.cfg.Set); err != nil {
		if existed {
			h.cfg.Set.Upsert(previous)
		}
		return "Запись удалена из журнала, но файл датасета не обновлён: " + err.Error()
	}
	return ""
}

/* ---------- отрисовка ---------- */

func (h *Handler) renderLogin(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := loginTemplate.Execute(w, struct{ Error string }{Error: msg}); err != nil {
		log.Printf("admin: login template: %v", err)
	}
}

func (h *Handler) renderPanel(w http.ResponseWriter, status int, errMsg, notice string) {
	h.mu.Lock()
	officials := append([]data.Person(nil), h.cfg.Set.Persons...)
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	data := struct {
		OfficialCount int
		Officials     []data.Person
		Error         string
		Notice        string
	}{
		OfficialCount: len(officials),
		Officials:     officials,
		Error:         errMsg,
		Notice:        notice,
	}
	if err := panelTemplate.Execute(w, data); err != nil {
		log.Printf("admin: panel template: %v", err)
	}
}

/* ---------- шаблоны ---------- */

const pageHead = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<link rel="stylesheet" href="/style.css">
</head>
<body class="admin-body">
<header class="site-header">
  <div class="container header-row">
    <div class="brand">
      <img class="brand-logo" src="https://customs.gov.ru/assets/frontend/images/small-logo.svg"
           alt="Федеральная таможенная служба" height="46">
      <div class="brand-text">
        <p class="brand-title">Федеральная таможенная служба</p>
        <p class="brand-subtitle">Управление реестром должностных лиц</p>
      </div>
    </div>
    %s
  </div>
</header>
<nav class="site-nav"><div class="container nav-row">
  <a class="nav-link" href="/">Проверка поручения</a>
  <a class="nav-link is-active" href="/admin/">Управление реестром</a>
</div></nav>`

var loginTemplate = template.Must(template.New("login").Parse(fmt.Sprintf(pageHead,
	"Вход — управление реестром должностных лиц",
	`<p class="header-note">Демонстрационный прототип</p>`) + `
<main class="container page">
  <div class="page-head">
    <h1>Вход в раздел управления</h1>
    <p class="page-lead">Раздел предназначен для ведения реестра должностных лиц
      таможенных органов.</p>
  </div>

  <section class="card card-narrow">
    <h2 class="card-title">Авторизация</h2>
    {{if .Error}}<div class="state state-error">
      <p class="state-title">Доступ не предоставлен</p>
      <p class="state-text">{{.Error}}</p></div>{{end}}

    <form method="post" action="/admin/login" class="form-stack">
      <div class="field">
        <label for="user">Идентификатор</label>
        <input id="user" name="user" autocomplete="username" required>
      </div>
      <div class="field">
        <label for="pass">Пароль</label>
        <input id="pass" name="pass" type="password" autocomplete="current-password" required>
      </div>
      <div class="actions">
        <button class="btn btn-primary" type="submit">Войти</button>
        <a class="btn btn-secondary" href="/">На главную</a>
      </div>
    </form>

    <p class="form-note">раздел роедназначен для ведения реестра должномтных лиц таможенных органов</p>
  </section>
</main>
</body>
</html>`))

var panelTemplate = template.Must(template.New("panel").Parse(fmt.Sprintf(pageHead,
	"Реестр должностных лиц — управление",
	`<form method="post" action="/admin/logout">
      <button class="btn btn-link" type="submit">Выйти</button>
    </form>`) + `
<main class="container page">
  <div class="page-head">
    <h1>Реестр должностных лиц</h1>
    <p class="page-lead">Записей в реестре: <b>{{.OfficialCount}}</b>. Изменения сразу
      сохраняются в файл датасета должностных лиц и в журнал проверок.</p>
  </div>

  {{if .Error}}<div class="state state-error">
    <p class="state-title">Изменение не применено</p>
    <p class="state-text">{{.Error}}</p></div>{{end}}
  {{if .Notice}}<div class="state state-ok">
    <p class="state-title">Готово</p>
    <p class="state-text">{{.Notice}}</p></div>{{end}}

  <section class="card">
    <h2 class="card-title">Добавить должностное лицо</h2>
    <form method="post" action="/admin/dltos" class="grid-form">
      <div class="field">
        <label for="new-id">Идентификатор</label>
        <input id="new-id" name="id" placeholder="DL-000" required>
      </div>
      <div class="field">
        <label for="new-fio">Фамилия, имя, отчество</label>
        <input id="new-fio" name="full_name" placeholder="ФИО" required>
      </div>
      <div class="field">
        <label for="new-passport">Паспорт</label>
        <input id="new-passport" name="passport" placeholder="0000 000000" required>
      </div>
      <div class="field">
        <label for="new-post">Таможенный пост</label>
        <input id="new-post" name="post" placeholder="Таможенный орган">
      </div>
      <div class="field field-wide">
        <label for="new-address">Адрес регистрации</label>
        <input id="new-address" name="address" placeholder="город, улица, дом, квартира">
      </div>
      <div class="actions">
        <button class="btn btn-primary" type="submit">Добавить в реестр</button>
      </div>
    </form>
  </section>

  <section class="card">
    <h2 class="card-title">Записи реестра</h2>
    {{if .Officials}}
      <div class="admin-rows">
      {{range .Officials}}
        <form method="post" action="/admin/dltos" class="admin-row">
          <div class="field">
            <label>Идентификатор</label>
            <input name="id" value="{{.ID}}" readonly>
          </div>
          <div class="field">
            <label>Фамилия, имя, отчество</label>
            <input name="full_name" value="{{.FullName}}" required>
          </div>
          <div class="field">
            <label>Паспорт</label>
            <input name="passport" value="{{.Passport}}" required>
          </div>
          <div class="field">
            <label>Таможенный пост</label>
            <input name="post" value="{{.Post}}">
          </div>
          <div class="admin-row-actions">
            <button class="btn btn-small btn-primary" type="submit">Сохранить</button>
            <button class="btn btn-small btn-danger" type="submit"
              formaction="/admin/dltos/delete"
              onclick="return confirm('Удалить запись {{.ID}}?');">Удалить</button>
          </div>
        </form>
      {{end}}
      </div>
    {{else}}
      <p class="state-text">Реестр пуст. Добавьте первую запись.</p>
    {{end}}
  </section>

  <p class="form-note">Удаление невозможно, если должностное лицо уже указано
    в поручениях: журнал проверок не должен терять ссылки. Изменение сведений
    при этом остаётся доступным.</p>
</main>
</body>
</html>`))
