// Команда ernserver — сервис основного датасета ЕРН.
//
// Владеет файлом dataset.json (лица, паспорта, родство, записи ЗАГС, разводы,
// дееспособность, занятость, налоги, права) и отдаёт его по HTTP. Сервис
// проверки coi-1 забирает датасет при старте, поэтому данные обновляются
// заменой файла, без пересборки потребителя.
//
// Реестр должностных лиц здесь не хранится: он правится через /admin в coi-1
// и остаётся локальным файлом того сервиса.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"ern-dataset/internal/dataset"
	"ern-dataset/internal/httpapi"
)

func main() {
	addr := flag.String("addr", envOr("ERN_ADDR", ":8091"), "адрес HTTP-сервера")
	dir := flag.String("dir", envOr("ERN_DIR", "data"), "каталог с dataset.json")
	version := flag.String("version", envOr("ERN_VERSION", "v1.0.0"),
		"версия датасета: пишется в журнал, /health и заголовок ответа")
	flag.Parse()

	path := filepath.Join(*dir, "dataset.json")
	store, err := dataset.New(path)
	if err != nil {
		log.Fatalf("датасет: %v", err)
	}

	handler, err := httpapi.New(httpapi.Config{
		Store:   store,
		Version: *version,
		Now:     time.Now,
	})
	if err != nil {
		log.Fatalf("http: %v", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	go func() {
		log.Printf("сервис данных ЕРН %s: %s, файл %s", *version, *addr, path)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-stop
	log.Println("остановка...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
