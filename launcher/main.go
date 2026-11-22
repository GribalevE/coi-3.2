// Команда coi-launcher запускает готовое решение: сервис данных ЕРН и сервис
// проверки поручений. Оба сервиса и сам лаунчер собраны заранее — пользователю
// достаточно запустить файл, ничего устанавливать и собирать не нужно.
//
// Лаунчер сам находит папки сервисов рядом с собой, проверяет файлы данных и
// бинарники, лечит типовые причины сбоя (метка «скачано из интернета», занятые
// порты, старый журнал проверок), затем поднимает сервисы в правильном порядке
// и открывает браузер.
//
// Использование:
//
//	coi-launcher.exe            запуск (двойной щелчок или START.cmd)
//	coi-launcher.exe -check     только диагностика, без запуска
//	coi-launcher.exe -reset     запуск с чистым журналом проверок
//	coi-launcher.exe -h         все параметры
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	exitOK      = 0
	exitCheck   = 2 // диагностика нашла неисправимое
	exitRuntime = 3 // сервис не поднялся или упал
)

type config struct {
	root      string
	dataset   string
	app       string
	dataExe   string
	appExe    string
	dataPort  int
	appPort   int
	token     string
	reset     bool
	noBrowser bool
	checkOnly bool
}

func main() {
	setupConsole()

	fmt.Println("COI LDT — выявление конфликта интересов при личном таможенном досмотре")
	fmt.Println()

	cfg := parseFlags()

	rep, err := prepare(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка подготовки: %v\n", err)
		os.Exit(exitCheck)
	}
	printReport(rep)

	if cfg.checkOnly {
		if rep.failed() {
			os.Exit(exitCheck)
		}
		fmt.Println("\nПроверка пройдена. Запуск: coi-launcher.exe")
		return
	}
	if rep.failed() {
		fmt.Println("\nЗапуск отменён: исправьте пункты FAIL выше и запустите файл снова.")
		os.Exit(exitCheck)
	}

	if err := run(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\nОшибка запуска: %v\n", err)
		os.Exit(exitRuntime)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.root, "root", "",
		"папка с решением; по умолчанию — папка лаунчера и её родители")
	flag.StringVar(&cfg.dataset, "dataset-dir", "",
		"папка сервиса данных; по умолчанию ищется по go.mod (module ern-dataset)")
	flag.StringVar(&cfg.app, "app-dir", "",
		"папка сервиса проверки; по умолчанию ищется по go.mod (module coi-1)")
	flag.StringVar(&cfg.dataExe, "data-exe", "",
		"путь к бинарнику сервиса данных; по умолчанию bin\\*.exe")
	flag.StringVar(&cfg.appExe, "app-exe", "",
		"путь к бинарнику сервиса проверки; по умолчанию bin\\*.exe")
	flag.IntVar(&cfg.dataPort, "data-port", 8091, "порт сервиса данных")
	flag.IntVar(&cfg.appPort, "app-port", 8080, "порт сервиса проверки")
	flag.StringVar(&cfg.token, "token", "dev-token", "токен API сервиса проверки")
	flag.BoolVar(&cfg.reset, "reset", false, "удалить журнал var/coi.db перед запуском")
	flag.BoolVar(&cfg.noBrowser, "no-browser", false, "не открывать браузер")
	flag.BoolVar(&cfg.checkOnly, "check", false, "только диагностика, без запуска")
	flag.Parse()
	return cfg
}

// setupConsole переключает консоль Windows в UTF-8, иначе русский текст
// превращается в мусор при запуске двойным щелчком.
func setupConsole() {
	if runtime.GOOS != "windows" {
		return
	}
	_ = exec.Command("cmd", "/c", "chcp", "65001").Run()
}

// defaultRoot ищет папку решения: сама папка лаунчера, затем до трёх уровней
// вверх — так файл можно положить и в корень архива, и в подпапку bin.
func defaultRoot() string {
	exe, err := os.Executable()
	if err != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			return wd
		}
		return "."
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 4; i++ {
		if findProject(dir, "ern-dataset", 4) != "" && findProject(dir, "coi-1", 4) != "" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Dir(exe)
}
