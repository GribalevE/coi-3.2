package main

import (
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Уровни сообщений диагностики.
const (
	levelOK   = "OK"
	levelFix  = "FIX"
	levelWarn = "WARN"
	levelFail = "FAIL"
)

// Имена, которые проверяются первыми при поиске бинарника в bin\.
var (
	preferDataset = []string{"ern-dataset.exe", "dataset.exe", "ernserver.exe"}
	preferApp     = []string{"coi.exe", "coi-1.exe", "server.exe"}
)

type item struct {
	level string
	text  string
}

type report struct {
	items []item
}

func (r *report) add(level, format string, args ...any) {
	r.items = append(r.items, item{level: level, text: fmt.Sprintf(format, args...)})
}

func (r *report) failed() bool {
	for _, it := range r.items {
		if it.level == levelFail {
			return true
		}
	}
	return false
}

func printReport(r *report) {
	fmt.Println("Диагностика запуска")
	fmt.Println(strings.Repeat("-", 68))
	for _, it := range r.items {
		fmt.Printf("[%-4s] %s\n", it.level, it.text)
	}
}

// prepare проверяет окружение, правит то, что можно поправить молча, и
// заполняет пути, бинарники и порты в cfg.
func prepare(cfg *config) (*report, error) {
	rep := &report{}

	if cfg.root == "" {
		cfg.root = defaultRoot()
	}
	if abs, err := filepath.Abs(cfg.root); err == nil {
		cfg.root = abs
	}
	rep.add(levelOK, "папка решения: %s", cfg.root)

	if cfg.dataset == "" {
		cfg.dataset = findProject(cfg.root, "ern-dataset", 4)
	}
	if cfg.dataset == "" {
		rep.add(levelFail, "не найдена папка сервиса данных (go.mod с module ern-dataset) — распакуйте архив целиком, чтобы папки coi-1 и ern-dataset лежали рядом с лаунчером")
	} else {
		rep.add(levelOK, "сервис данных: %s", cfg.dataset)
	}

	if cfg.app == "" {
		cfg.app = findProject(cfg.root, "coi-1", 4)
	}
	if cfg.app == "" {
		rep.add(levelFail, "не найдена папка сервиса проверки (go.mod с module coi-1) — распакуйте архив целиком, чтобы папки coi-1 и ern-dataset лежали рядом с лаунчером")
	} else {
		rep.add(levelOK, "сервис проверки: %s", cfg.app)
	}

	if rep.failed() {
		return rep, nil // без папок остальные проверки бессмысленны
	}

	checkDatasetFile(rep, cfg)
	checkOfficialsFile(rep, cfg)
	checkRemoteSupport(rep, cfg)

	cfg.dataExe = checkExe(rep, "сервис данных", cfg.dataset, cfg.dataExe, preferDataset)
	cfg.appExe = checkExe(rep, "сервис проверки", cfg.app, cfg.appExe, preferApp)

	unblockAll(rep, cfg.dataset, cfg.app)
	resolvePort(rep, "сервис данных", &cfg.dataPort)
	resolvePort(rep, "сервис проверки", &cfg.appPort)
	checkJournal(rep, cfg)

	return rep, nil
}

func checkDatasetFile(rep *report, cfg *config) {
	path := filepath.Join(cfg.dataset, "data", "dataset.json")
	st, err := os.Stat(path)
	switch {
	case err != nil:
		rep.add(levelFail, "нет файла данных %s — архив распакован не полностью, распакуйте его заново целиком", path)
	case st.Size() < 1000:
		rep.add(levelFail, "файл данных %s повреждён: %d байт — скачайте архив заново", path, st.Size())
	default:
		rep.add(levelOK, "данные: %s (%d КБ)", path, st.Size()/1024)
	}
}

func checkOfficialsFile(rep *report, cfg *config) {
	path := filepath.Join(cfg.app, "data", "dltos.json")
	st, err := os.Stat(path)
	switch {
	case err != nil:
		rep.add(levelFail, "нет реестра должностных лиц %s — архив распакован не полностью, распакуйте его заново целиком", path)
	case st.Size() < 100:
		rep.add(levelFail, "реестр должностных лиц %s повреждён: %d байт — скачайте архив заново", path, st.Size())
	default:
		rep.add(levelOK, "реестр должностных лиц: %s (%d КБ)", path, st.Size()/1024)
	}
}

// checkRemoteSupport проверяет, что сервис проверки умеет брать данные по HTTP
// (COI_DATA_URL). Если не умеет — ему нужен локальный data/dataset.json.
func checkRemoteSupport(rep *report, cfg *config) {
	if grepSources(cfg.app, "COI_DATA_URL") || grepSources(cfg.app, `"data-url"`) {
		rep.add(levelOK, "связь сервисов: проверка получает данные по сети (COI_DATA_URL)")
		return
	}
	local := filepath.Join(cfg.app, "data", "dataset.json")
	if st, err := os.Stat(local); err == nil && st.Size() > 1000 {
		rep.add(levelWarn, "сервис проверки работает на локальном %s", local)
		return
	}
	rep.add(levelFail, "сборка сервиса проверки не умеет получать данные по сети и нет %s — скачайте актуальный архив решения", local)
}

// grepSources ищет подстроку в .go-файлах проекта.
func grepSources(dir, needle string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "var":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if raw, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(raw), needle) {
			found = true
		}
		return nil
	})
	return found
}

// findExe ищет исполняемый файл сервиса: сначала заданный путь, затем
// предпочтительные имена в bin\, затем единственный .exe в bin\ или в корне.
func findExe(projectDir, override string, prefer []string) (string, []string) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", nil
		}
		return override, []string{override}
	}

	var all []string
	byName := map[string]string{}
	for _, dir := range []string{filepath.Join(projectDir, "bin"), projectDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if !strings.HasSuffix(name, ".exe") || name == "coi-launcher.exe" {
				continue
			}
			full := filepath.Join(dir, e.Name())
			all = append(all, full)
			if _, seen := byName[name]; !seen {
				byName[name] = full
			}
		}
	}

	for _, p := range prefer {
		if full, ok := byName[strings.ToLower(p)]; ok {
			return full, all
		}
	}
	if len(all) == 1 {
		return all[0], all
	}
	return "", all
}

// checkExe возвращает путь к бинарнику и печатает результат проверки.
func checkExe(rep *report, role, projectDir, override string, prefer []string) string {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			rep.add(levelFail, "указанный файл (%s) не найден: %s", role, override)
			return ""
		}
	}
	exe, all := findExe(projectDir, override, prefer)
	switch {
	case exe == "" && len(all) == 0:
		rep.add(levelFail, "нет собранного файла сервиса (%s) в %s — в архиве отсутствует bin\\*.exe, скачайте архив заново", role, projectDir)
		return ""
	case exe == "":
		rep.add(levelFail, "в %s несколько .exe (%s) — укажите нужный через -data-exe или -app-exe",
			projectDir, strings.Join(shortNames(all), ", "))
		return ""
	case !isPE(exe):
		rep.add(levelFail, "%s повреждён при скачивании (это не исполняемый файл) — скачайте архив заново", exe)
		return exe
	}
	st, err := os.Stat(exe)
	if err != nil {
		rep.add(levelFail, "не читается %s: %v", exe, err)
		return exe
	}
	rep.add(levelOK, "файл сервиса (%s): %s (%.1f МБ)", role, filepath.Base(exe), float64(st.Size())/1024/1024)
	return exe
}

func shortNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

// isPE проверяет сигнатуру исполняемого файла Windows: пара байтов "MZ".
func isPE(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [2]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false
	}
	return b[0] == 'M' && b[1] == 'Z'
}

// unblockAll снимает метку Zone.Identifier — из-за неё Windows блокирует
// запуск скачанных .exe и .ps1.
func unblockAll(rep *report, dirs ...string) {
	if runtime.GOOS != "windows" {
		return
	}
	count := 0
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "var":
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ps1", ".exe":
			default:
				return nil
			}
			if rmErr := os.Remove(path + ":Zone.Identifier"); rmErr == nil {
				count++
			}
			return nil
		})
	}
	if count > 0 {
		rep.add(levelFix, "снята метка «скачано из интернета» с %d файлов", count)
		return
	}
	rep.add(levelOK, "блокировки скачанных файлов нет")
}

func resolvePort(rep *report, name string, port *int) {
	if portFree(*port) {
		rep.add(levelOK, "порт %d свободен (%s)", *port, name)
		return
	}
	next := nextFree(*port + 1)
	if next == 0 {
		rep.add(levelFail, "порт %d занят и свободных рядом нет (%s) — закройте лишние программы", *port, name)
		return
	}
	rep.add(levelFix, "порт %d занят (%s) — использую %d", *port, name, next)
	*port = next
}

func checkJournal(rep *report, cfg *config) {
	path := filepath.Join(cfg.app, "var", "coi.db")
	if _, err := os.Stat(path); err != nil {
		rep.add(levelOK, "журнал проверок будет создан заново")
		return
	}
	if cfg.reset {
		if err := os.Remove(path); err != nil {
			rep.add(levelWarn, "не удалось удалить %s: %v", path, err)
			return
		}
		rep.add(levelFix, "удалён старый журнал %s", path)
		return
	}
	rep.add(levelWarn, "журнал проверок уже есть: показ пойдёт по старым данным, для чистого запуска используйте -reset")
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func nextFree(from int) int {
	for p := from; p < from+50; p++ {
		if portFree(p) {
			return p
		}
	}
	return 0
}

// findProject ищет папку, в go.mod которой указан нужный модуль.
func findProject(root, module string, depth int) string {
	if hasModule(root, module) {
		return root
	}
	if depth == 0 {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch e.Name() {
		case ".git", "bin", "var", "node_modules", "data", "docs":
			continue
		}
		dir := filepath.Join(root, e.Name())
		if hasModule(dir, module) {
			return dir
		}
		if found := findProject(dir, module, depth-1); found != "" {
			return found
		}
	}
	return ""
}

func hasModule(dir, module string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "module ")) == module
	}
	return false
}
