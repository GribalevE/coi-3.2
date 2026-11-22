// internal/smev/smev.go

// Package smev provides a production-shaped synthetic exchange adapter.
//
// Адаптер читает нормализованный датасет лиц (data/dataset.json) и, если нужно,
// реестр должностных лиц (data/dltos.json). Датасет не приходит из БД
// приложения: и реестр идентификации, и обмен работают с файлами.
package smev

import (
	"context"
	"errors"
	"fmt"
	"time"

	"coi-1/internal/data"
)

type PersonDTO = data.Person
type KinshipDTO = data.Kinship
type DivorceDTO = data.Divorce
type CapacityDTO = data.Capacity
type ParentalDTO = data.Parental
type EmploymentDTO = data.Employment
type TaxDTO = data.Tax

var (
	ErrTimeout  = errors.New("smev: timeout")
	ErrNotFound = errors.New("smev: not found")
	ErrUpstream = errors.New("smev: upstream error")
)

// SourceInfo описывает фактическую доступность источника сведений в датасете.
//
// Это единственное место, где известно, что реально есть в данных. Отчёт о
// покрытии в API строится отсюда, а не из захардкоженного списка.
type SourceInfo struct {
	Name      string
	Available bool
	Detail    string
}

// Client describes the exchange operations used by one check.
//
// Важно: интерфейс не различает «источника нет» и «источник пуст».
// Это осознанно: с точки зрения обмена оба случая — успешный ответ
// с пустым результатом. Факт недоступности источника фиксируется
// отдельно — через CoverageReport, который строит API-слой.
type Client interface {
	FetchPerson(context.Context, string) (PersonDTO, error)
	FetchKinship(context.Context, string) ([]KinshipDTO, error)
	FetchDivorces(context.Context, string) ([]DivorceDTO, error)
	FetchCapacity(context.Context, string) (CapacityDTO, error)
	FetchParental(context.Context, string) ([]ParentalDTO, error)
	FetchEmployment(context.Context, string) (EmploymentDTO, error)
	FetchTax(context.Context, string) (TaxDTO, error)
}

// MockClient reads an immutable normalized bundle.
type MockClient struct {
	bundle    data.Bundle
	officials func() []data.Person
	Latency   time.Duration
	Failure   error
}

// NewMockClient validates and copies a bundle.
//
// Параметр unavailable оставлен для совместимости с вызывающим кодом и не
// используется: доступность источников определяется по содержимому датасета
// (см. Sources).
func NewMockClient(b data.Bundle, latency time.Duration, _ []string) (*MockClient, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &MockClient{bundle: b, Latency: latency}, nil
}

// NewFromBundle — адаптер над датасетом лиц и реестром должностных лиц.
//
// Реестр передаётся функцией, а не срезом: должностное лицо могут добавить
// через /admin уже после старта сервиса, и обмен должен видеть актуальный
// состав. Функция обязана быть безопасной при параллельном вызове.
func NewFromBundle(
	b data.Bundle, officials func() []data.Person, latency time.Duration,
) (*MockClient, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &MockClient{
		bundle:    b,
		officials: officials,
		Latency:   latency,
	}, nil
}

// officialList возвращает текущий состав реестра должностных лиц.
func (m *MockClient) officialList() []data.Person {
	if m.officials == nil {
		return nil
	}
	return m.officials()
}

// Sources сообщает, какие источники сведений реально присутствуют в датасете.
func (m *MockClient) Sources() []SourceInfo {
	return []SourceInfo{
		{Name: "person", Available: len(m.bundle.Persons) > 0, Detail: countDetail(len(m.bundle.Persons), "записей о лицах")},
		{Name: "kinship", Available: len(m.bundle.Kinship) > 0, Detail: countDetail(len(m.bundle.Kinship), "записей о родстве")},
		{Name: "civil_registry", Available: len(m.bundle.Civil) > 0, Detail: countDetail(len(m.bundle.Civil), "записей ЗАГС")},
		{Name: "divorce", Available: len(m.bundle.Divorces) > 0, Detail: countDetail(len(m.bundle.Divorces), "записей о расторжении брака")},
		{Name: "emancipation", Available: len(m.bundle.Emancipations) > 0, Detail: countDetail(len(m.bundle.Emancipations), "записей об эмансипации")},
		{Name: "tax_registration", Available: len(m.bundle.Tax) > 0, Detail: countDetail(len(m.bundle.Tax), "записей налогового учёта")},
		{Name: "parental_rights", Available: len(m.bundle.Parental) > 0, Detail: countDetail(len(m.bundle.Parental), "записей о родительских правах")},
		{Name: "legal_capacity", Available: len(m.bundle.Capacity) > 0, Detail: countDetail(len(m.bundle.Capacity), "записей о дееспособности")},
		{Name: "employment_service", Available: len(m.bundle.Employment) > 0, Detail: countDetail(len(m.bundle.Employment), "записей службы занятости")},
		{Name: "post_district", Available: false, Detail: "внешний справочник «пост — район» не предоставлен"},
	}
}

func countDetail(n int, what string) string {
	if n == 0 {
		return "источник не представлен в датасете"
	}
	return fmt.Sprintf("%d %s", n, what)
}

func (m *MockClient) before(ctx context.Context) error {
	if m.Latency > 0 {
		timer := time.NewTimer(m.Latency)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ErrTimeout
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return ErrTimeout
	}
	return m.Failure
}

func (m *MockClient) exists(id string) bool {
	for _, p := range m.bundle.Persons {
		if p.ID == id {
			return true
		}
	}
	for _, p := range m.officialList() {
		if p.ID == id {
			return true
		}
	}
	return false
}

func (m *MockClient) FetchPerson(ctx context.Context, id string) (PersonDTO, error) {
	if err := m.before(ctx); err != nil {
		return PersonDTO{}, err
	}
	for _, p := range m.bundle.Persons {
		if p.ID != id {
			continue
		}
		return m.enrich(p, id), nil
	}
	for _, p := range m.officialList() {
		if p.ID != id {
			continue
		}
		return m.enrich(p, id), nil
	}
	return PersonDTO{}, ErrNotFound
}

// enrich добавляет производные признаки (эмансипация) и копирует срезы,
// чтобы получатель не мог изменить датасет.
func (m *MockClient) enrich(p data.Person, id string) PersonDTO {
	p.Citizenships = append([]string(nil), p.Citizenships...)
	p.DistrictStreets = append([]string(nil), p.DistrictStreets...)
	for _, e := range m.bundle.Emancipations {
		if e.PersonID == id {
			p.Emancipated = true
		}
	}
	return p
}

func (m *MockClient) FetchKinship(ctx context.Context, id string) ([]KinshipDTO, error) {
	if err := m.before(ctx); err != nil {
		return nil, err
	}
	if !m.exists(id) {
		return nil, ErrNotFound
	}
	neighbors := map[string]bool{id: true}
	for _, k := range m.bundle.Kinship {
		if k.A == id {
			neighbors[k.B] = true
		}
		if k.B == id {
			neighbors[k.A] = true
		}
	}
	out := make([]KinshipDTO, 0)
	for _, k := range m.bundle.Kinship {
		if neighbors[k.A] || neighbors[k.B] {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *MockClient) FetchDivorces(ctx context.Context, id string) ([]DivorceDTO, error) {
	if err := m.before(ctx); err != nil {
		return nil, err
	}
	if !m.exists(id) {
		return nil, ErrNotFound
	}
	out := make([]DivorceDTO, 0)
	for _, d := range m.bundle.Divorces {
		if d.A == id || d.B == id {
			out = append(out, d)
		}
	}
	return out, nil
}

// FetchCapacity возвращает пустое состояние, если записи нет.
//
// Раньше здесь возвращалось State: "full". Это неверно: отсутствие листа о
// дееспособности не подтверждает полную дееспособность. Пустое состояние
// означает «сведений нет», и правило K-17 по нему не срабатывает.
func (m *MockClient) FetchCapacity(ctx context.Context, id string) (CapacityDTO, error) {
	if err := m.before(ctx); err != nil {
		return CapacityDTO{}, err
	}
	if !m.exists(id) {
		return CapacityDTO{}, ErrNotFound
	}
	for _, c := range m.bundle.Capacity {
		if c.PersonID == id {
			return c, nil
		}
	}
	return CapacityDTO{PersonID: id}, nil
}

func (m *MockClient) FetchParental(ctx context.Context, id string) ([]ParentalDTO, error) {
	if err := m.before(ctx); err != nil {
		return nil, err
	}
	if !m.exists(id) {
		return nil, ErrNotFound
	}
	out := make([]ParentalDTO, 0)
	for _, p := range m.bundle.Parental {
		if p.PersonID == id {
			out = append(out, p)
		}
	}
	return out, nil
}

// FetchEmployment возвращает «сведений нет», если записи нет.
func (m *MockClient) FetchEmployment(ctx context.Context, id string) (EmploymentDTO, error) {
	if err := m.before(ctx); err != nil {
		return EmploymentDTO{}, err
	}
	if !m.exists(id) {
		return EmploymentDTO{}, ErrNotFound
	}
	for _, e := range m.bundle.Employment {
		if e.PersonID == id {
			return e, nil
		}
	}
	return EmploymentDTO{PersonID: id}, nil
}

func (m *MockClient) FetchTax(ctx context.Context, id string) (TaxDTO, error) {
	if err := m.before(ctx); err != nil {
		return TaxDTO{}, err
	}
	if !m.exists(id) {
		return TaxDTO{}, ErrNotFound
	}
	for _, t := range m.bundle.Tax {
		if t.PersonID == id {
			t.OGRNIP = append([]string(nil), t.OGRNIP...)
			return t, nil
		}
	}
	return TaxDTO{PersonID: id, OGRNIP: []string{}}, nil
}
