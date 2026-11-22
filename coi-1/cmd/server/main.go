// cmd/server/main.go

// Command server запускает REST-сервис выявления конфликта интересов
// при личном таможенном досмотре. Работает нативно на Windows, Linux,
// macOS без CGO и без внешних компиляторов.
//
// Источники данных:
//
//	data/dataset.json — лица (без деления на физических и должностных лиц)
//	data/dltos.json   — реестр должностных лиц, правится через /admin
//
// Реестр идентификации и мок СМЭВ читают один и тот же набор лиц, поэтому
// дублирования загрузки нет.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"coi-1/internal/admin"
	"coi-1/internal/api"
	"coi-1/internal/conflict"
	"coi-1/internal/data"
	"coi-1/internal/erndb"
	"coi-1/internal/smev"
	"coi-1/internal/store"
	"coi-1/web"
)

type config struct {
	Addr        string
	DBPath      string
	DataPath    string
	DLTOSPath   string
	SQLPath     string
	Token       string
	SMEVLatency time.Duration
	RuleVersion string
	AdminUser   string
	AdminPass   string
	DataURL     string
}

func loadConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.Addr, "addr", envOr("COI_ADDR", ":8080"), "HTTP bind address")
	flag.StringVar(&cfg.DBPath, "db", envOr("COI_DB", "var/coi.db"), "SQLite path")
	flag.StringVar(&cfg.DataPath, "data", envOr("COI_DATA", "data/dataset.json"),
		"persons dataset prepared by cmd/seed")
	flag.StringVar(&cfg.DataURL, "data-url", envOr("COI_DATA_URL", ""),
		"сервис основного датасета, например http://localhost:8091; приоритетнее -data")

	flag.StringVar(&cfg.DLTOSPath, "dltos", envOr("COI_DLTOS", "data/dltos.json"),
		"officials dataset prepared by cmd/seed")
	flag.StringVar(&cfg.SQLPath, "sql", envOr("COI_SQL", ""),
		"legacy: ERN SQL dump (конвертируется на старте, вместо -data/-dltos)")
	flag.StringVar(&cfg.Token, "token", envOr("AIS_TOKEN", ""), "Bearer token")
	flag.DurationVar(&cfg.SMEVLatency, "smev-latency",
		envDuration("COI_SMEV_LATENCY", 0), "Mock SMEV latency")
	flag.StringVar(&cfg.RuleVersion, "rules",
		envOr("COI_RULES", "rules_v1.0.0"), "Rule set version")
	flag.StringVar(&cfg.AdminUser, "admin-user",
		envOr("COI_ADMIN_USER", "unecon"), "admin panel login (demo default)")
	flag.StringVar(&cfg.AdminPass, "admin-pass",
		envOr("COI_ADMIN_PASSWORD", "unecon"), "admin panel password (demo default)")
	flag.Parse()

	if cfg.Token == "" {
		log.Fatal("AIS_TOKEN is required")
	}
	if cfg.SQLPath != "" {
		log.Printf("WARNING: -sql is legacy, prefer data/dataset.json + data/dltos.json")
		cfg.DataPath = ""
	}
	return cfg
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q: %v (using %s)", name, raw, err, fallback)
		return fallback
	}
	return d
}

func main() {
	cfg := loadConfig()

	// Windows не знает про \: пути создаются через os.MkdirAll,
	// а не через shell-команды.
	if dir := parentDir(cfg.DBPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	bundle, officials, err := loadDatasets(cfg)
	if err != nil {
		log.Fatalf("load datasets: %v", err)
	}
	log.Printf("persons: %d, kinship: %d, civil: %d, divorce: %d, tax: %d",
		len(bundle.Persons), len(bundle.Kinship), len(bundle.Civil),
		len(bundle.Divorces), len(bundle.Tax))
	log.Printf("officials: %d", len(officials.Persons))

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seedIfEmpty(ctx, st, bundle); err != nil {
		log.Fatalf("seed: %v", err)
	}
	if err := syncOfficials(ctx, st, officials); err != nil {
		log.Fatalf("sync officials: %v", err)
	}

	// Реестр должностных лиц передаётся админке указателем: правки через
	// страницу меняют и файл, и журнал.
	officialsSet := officials
	adminHandler, err := admin.New(admin.Config{
		Store: st,
		Set:   &officialsSet,
		Path:  cfg.DLTOSPath,
		User:  cfg.AdminUser,
		Pass:  cfg.AdminPass,
	})
	if err != nil {
		log.Fatalf("build admin: %v", err)
	}
	if cfg.AdminPass == "123" {
		log.Printf("WARNING: пароль администратора по умолчанию (admin/123) — " +
			"допустимо только для демонстрации, задайте COI_ADMIN_PASSWORD")
	}

	// Адаптер СМЭВ получает реестр функцией: должностное лицо, добавленное
	// через /admin, должно находиться обменом без перезапуска сервиса.
	mockSMEV, err := smev.NewFromBundle(bundle, adminHandler.Officials, cfg.SMEVLatency)
	if err != nil {
		log.Fatalf("build smev: %v", err)
	}

	engine, err := conflict.NewEngine(cfg.RuleVersion)
	if err != nil {
		log.Fatalf("build engine: %v", err)
	}

	// web.FS — это embed.FS, он уже реализует fs.FS,
	// поэтому fs.Sub больше не нужен: файлы лежат в корне FS.
	handler := api.New(api.Config{
		Store:       st,
		SMEV:        mockSMEV,
		Engine:      engine,
		Token:       cfg.Token,
		Now:         time.Now,
		SMEVTimeout: 5 * time.Second,
		WebFS:       web.FS,
		Admin:       adminHandler,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	// os.Interrupt работает на обеих ОС: SIGINT на Unix, Ctrl+C на Windows.
	signal.Notify(stop, os.Interrupt)

	go func() {
		log.Printf("listening on %s (rules=%s)", cfg.Addr, cfg.RuleVersion)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-stop
	log.Println("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// loadDatasets читает оба файла датасета. В режиме -sql дамп конвертируется в
// памяти: лица берутся целиком, должностные лица выделяются по роли.
// loadDatasets читает основной датасет — из отдельного сервиса данных или из
// локального файла — и реестр должностных лиц, который всегда локальный:
// он правится через /admin и принадлежит этому сервису.
func loadDatasets(cfg config) (data.Bundle, data.DLTOSet, error) {
	var (
		bundle data.Bundle
		err    error
	)

	switch {
	case cfg.DataURL != "":
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		remote := erndb.NewRemote(cfg.DataURL, 20*time.Second)
		bundle, err = remote.Fetch(ctx, 3)
		if err != nil {
			return data.Bundle{}, data.DLTOSet{}, fmt.Errorf(
				"сервис данных %s: %w (запустите проект ern-dataset: .\\scripts\\run.ps1)",
				cfg.DataURL, err)
		}
		log.Printf("dataset: %s", cfg.DataURL)
	case cfg.DataPath != "":
		bundle, err = data.Load(cfg.DataPath)
		if err != nil {
			return data.Bundle{}, data.DLTOSet{}, fmt.Errorf(
				"%s: %w (соберите датасет: go run ./cmd/seed -in data/ern_full.sql "+
					"либо задайте -data-url)", cfg.DataPath, err)
		}
		log.Printf("dataset: %s", cfg.DataPath)
	case cfg.SQLPath != "":
		return loadLegacy(cfg)
	default:
		return data.Bundle{}, data.DLTOSet{}, errors.New(
			"не задан ни -data-url, ни -data, ни -sql")
	}

	officials, err := data.LoadDLTOSet(cfg.DLTOSPath)
	if err != nil {
		return data.Bundle{}, data.DLTOSet{}, fmt.Errorf("%s: %w", cfg.DLTOSPath, err)
	}
	return bundle, officials, nil
}

// loadLegacy — прежний путь: SQL-дамп ЕРН разбирается в памяти.
func loadLegacy(cfg config) (data.Bundle, data.DLTOSet, error) {
	db, err := erndb.Open(cfg.SQLPath)
	if err != nil {
		return data.Bundle{}, data.DLTOSet{}, err
	}
	defer db.Close()

	legacyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	bundle, err := erndb.Bundle(legacyCtx, db)
	if err != nil {
		return data.Bundle{}, data.DLTOSet{}, err
	}

	// Роль из дампа переносим в реестр должностных лиц и очищаем в датасете.
	officials := data.DLTOSet{Persons: make([]data.Person, 0, 32)}
	for i := range bundle.Persons {
		if bundle.Persons[i].Role == "DLTO" {
			p := bundle.Persons[i]
			p.Role = ""
			officials.Persons = append(officials.Persons, p)
		}
		bundle.Persons[i].Role = ""
	}
	return bundle, officials, nil
}

// parentDir возвращает директорию файла или "" для относительного имени
// без директории. Нужен для os.MkdirAll перед открытием SQLite.
func parentDir(p string) string {
	i := len(p) - 1
	for i >= 0 && p[i] != '/' && p[i] != '\\' {
		i--
	}
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// seedIfEmpty загружает лица в журнал. Роль не берётся из датасета: все лица
// заводятся как PHYS, а должностные лица затем перезаписываются syncOfficials.
func seedIfEmpty(ctx context.Context, st *store.Store, bundle data.Bundle) error {
	people, err := st.People(ctx)
	if err != nil {
		return err
	}
	if len(people) > 0 {
		log.Printf("registry already has %d persons; skipping seed", len(people))
		return nil
	}
	for i := range bundle.Persons {
		bundle.Persons[i].Role = "PHYS"
	}
	if err := st.Seed(ctx, bundle); err != nil {
		return err
	}
	log.Printf("seeded %d persons from dataset", len(bundle.Persons))
	return nil
}

// syncOfficials приводит журнал в соответствие с реестром должностных лиц.
func syncOfficials(ctx context.Context, st *store.Store, set data.DLTOSet) error {
	for _, p := range set.ToPersons() {
		if err := st.UpsertOfficial(ctx, p); err != nil {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
	}
	return nil
}
