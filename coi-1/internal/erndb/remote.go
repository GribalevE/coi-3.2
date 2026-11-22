// remote.go — клиент отдельного сервиса основного датасета (проект ern-dataset).
package erndb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"coi-1/internal/data"
)

// Remote — клиент отдельного сервиса основного датасета (проект ern-dataset).
//
// Сервис отдаёт готовый dataset.json по /api/v1/bundle, поэтому клиент не
// разбирает его сам: он сохраняет ответ во временный файл и отдаёт data.Load,
// чтобы схема и ссылочная целостность проверялись ровно так же, как для
// локального файла.
type Remote struct {
	base   string
	client *http.Client
}

// NewRemote создаёт клиент. timeout ограничивает один запрос целиком.
func NewRemote(base string, timeout time.Duration) *Remote {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Remote{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: timeout},
	}
}

// Bundle забирает основной датасет у сервиса данных.
func (r *Remote) Bundle(ctx context.Context) (data.Bundle, error) {
	body, err := r.get(ctx, "/api/v1/bundle")
	if err != nil {
		return data.Bundle{}, err
	}
	defer body.Close()

	f, err := os.CreateTemp("", "coi-bundle-*.json")
	if err != nil {
		return data.Bundle{}, fmt.Errorf("временный файл: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)

	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return data.Bundle{}, fmt.Errorf("чтение ответа сервиса данных: %w", err)
	}
	if err := f.Close(); err != nil {
		return data.Bundle{}, fmt.Errorf("запись временного файла: %w", err)
	}

	bundle, err := data.Load(name)
	if err != nil {
		return data.Bundle{}, fmt.Errorf("датасет от сервиса %s: %w", r.base, err)
	}
	return bundle, nil
}

// Fetch повторяет запрос: сервис данных может стартовать чуть позже coi-1.
func (r *Remote) Fetch(ctx context.Context, attempts int) (data.Bundle, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		bundle, err := r.Bundle(ctx)
		if err == nil {
			return bundle, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return data.Bundle{}, ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return data.Bundle{}, lastErr
}

func (r *Remote) get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сервис данных недоступен (%s): %w", r.base, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("сервис данных вернул %s", resp.Status)
	}
	return resp.Body, nil
}
