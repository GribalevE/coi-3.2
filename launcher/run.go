package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"
)

type childProc struct {
	name string
	cmd  *exec.Cmd
	done chan error
}

func (c *childProc) stop() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
}

// run поднимает сервис данных, ждёт его готовности, затем сервис проверки.
// Оба файла уже собраны — здесь только запуск.
func run(cfg *config) error {
	fmt.Printf("\nЗапускаю сервис данных (порт %d) ...\n", cfg.dataPort)
	db, err := startProcess("db", cfg.dataset, cfg.dataExe,
		[]string{"-addr", fmt.Sprintf(":%d", cfg.dataPort), "-dir", "data"}, nil)
	if err != nil {
		return fmt.Errorf("сервис данных: %w", err)
	}
	defer db.stop()

	dataURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.dataPort)
	if err := waitHTTP(dataURL+"/health", 30*time.Second); err != nil {
		return fmt.Errorf("сервис данных не отвечает: %w", err)
	}
	fmt.Println("сервис данных готов")

	fmt.Printf("Запускаю сервис проверки (порт %d) ...\n", cfg.appPort)
	app, err := startProcess("app", cfg.app, cfg.appExe,
		[]string{"-addr", fmt.Sprintf(":%d", cfg.appPort), "-token", cfg.token},
		[]string{"COI_DATA_URL=" + dataURL})
	if err != nil {
		return fmt.Errorf("сервис проверки: %w", err)
	}
	defer app.stop()

	appURL := fmt.Sprintf("http://localhost:%d", cfg.appPort)
	if err := waitHTTP(appURL+"/", 30*time.Second); err != nil {
		return fmt.Errorf("сервис проверки не отвечает: %w", err)
	}
	fmt.Println("сервис проверки готов")

	if !cfg.noBrowser {
		openBrowser(appURL)
	}

	fmt.Printf("\nГотово. Приложение открыто: %s\n", appURL)
	fmt.Printf("Если браузер не открылся — введите этот адрес вручную.\n")
	fmt.Printf("Админка: %s/admin/  (логин unecon, пароль unecon)\n", appURL)
	fmt.Println("Остановка — Ctrl+C в этом окне.")

	return waitStop(db, app)
}

func startProcess(name, dir, exe string, args, env []string) (*childProc, error) {
	if exe == "" {
		return nil, fmt.Errorf("файл сервиса не найден")
	}
	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("нет файла %s", exe)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	out := &prefixWriter{prefix: "[" + name + "] ", atLineStart: true, w: os.Stdout}
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc := &childProc{name: name, cmd: cmd, done: make(chan error, 1)}
	go func() {
		proc.done <- cmd.Wait()
	}()
	return proc, nil
}

func waitStop(procs ...*childProc) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	exited := make(chan error, len(procs))
	for _, p := range procs {
		go func(p *childProc) {
			exited <- <-p.done
		}(p)
	}

	select {
	case <-interrupt:
		fmt.Println("\nОстанавливаю сервисы ...")
		for _, p := range procs {
			p.stop()
		}
		return nil
	case err := <-exited:
		for _, p := range procs {
			p.stop()
		}
		if err != nil {
			return fmt.Errorf("сервис неожиданно завершился: %w", err)
		}
		return fmt.Errorf("сервис неожиданно завершился")
	}
}

func waitHTTP(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("ответ %s", resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("таймаут")
	}
	return fmt.Errorf("%s за %s (%v)", url, timeout, lastErr)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("не удалось открыть браузер: %v\n", err)
	}
}

// prefixWriter помечает строки дочернего процесса, чтобы вывод двух сервисов
// не смешивался в один поток.
type prefixWriter struct {
	mu          sync.Mutex
	prefix      string
	w           io.Writer
	atLineStart bool
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var sb strings.Builder
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" {
			continue
		}
		if p.atLineStart {
			sb.WriteString(p.prefix)
			p.atLineStart = false
		}
		sb.WriteString(line)
		if strings.HasSuffix(line, "\n") {
			p.atLineStart = true
		}
	}
	if _, err := io.WriteString(p.w, sb.String()); err != nil {
		return len(b), err
	}
	return len(b), nil
}
