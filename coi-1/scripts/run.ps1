# scripts/run.ps1 - запуск сервиса проверки из собранного бинарника.
#
# Файл сохранён в UTF-8 с BOM. Без BOM PowerShell 5.1 читает кириллицу как
# cp1251 и падает с "Непредвиденная лексема" / "Отсутствует закрывающий знак".
#
# Источник сведений о лицах: сервис ern-dataset (COI_DATA_URL) либо локальный
# файл data/dataset.json. Сервис приоритетнее файла. Реестр должностных лиц
# всегда локальный - его правит раздел /admin.
#
#   .\scripts\run.ps1
#   .\scripts\run.ps1 -DataUrl http://127.0.0.1:8091
#   .\scripts\run.ps1 -Reset          чистая база проверок

param(
    [string]$Addr = ':8080',
    [string]$DataUrl = '',
    [switch]$Reset
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Join-Path $PSScriptRoot '..')

if ($DataUrl) { $env:COI_DATA_URL = $DataUrl }

if (-not $env:AIS_TOKEN) { $env:AIS_TOKEN = 'dev-token' }
if (-not $env:COI_DLTOS) { $env:COI_DLTOS = 'data/dltos.json' }
if (-not $env:COI_DB) { $env:COI_DB = 'var/coi.db' }
if (-not $env:COI_ADMIN_USER) { $env:COI_ADMIN_USER = 'admin' }
if (-not $env:COI_ADMIN_PASSWORD) { $env:COI_ADMIN_PASSWORD = '123' }

# --- бинарник ----------------------------------------------------------------
if (-not (Test-Path -LiteralPath 'bin/coi.exe')) {
    Write-Host 'ERROR: не найден bin/coi.exe' -ForegroundColor Red
    Write-Host 'Соберите проект: .\scripts\build.ps1' -ForegroundColor Yellow
    exit 1
}

# --- источник сведений о лицах ----------------------------------------------
if (-not $env:COI_DATA) { $env:COI_DATA = 'data/dataset.json' }

# Файла лиц может не быть: тогда сведения берутся из сервиса данных.
# Если сервис уже запущен на порту по умолчанию, подхватываем его сами.
if (-not $env:COI_DATA_URL -and -not (Test-Path -LiteralPath $env:COI_DATA)) {
    try {
        $probe = Invoke-WebRequest -Uri 'http://127.0.0.1:8091/health' -UseBasicParsing -TimeoutSec 2
        if ($probe.StatusCode -lt 400) {
            $env:COI_DATA_URL = 'http://127.0.0.1:8091'
            Write-Host 'файла лиц нет, но сервис данных отвечает на 127.0.0.1:8091 - беру данные оттуда' -ForegroundColor Cyan
        }
    } catch { }
}

$files = @()
if ($env:COI_DATA_URL) {
    $dataLabel = $env:COI_DATA_URL.TrimEnd('/')
} else {
    $dataLabel = $env:COI_DATA
    $files += $env:COI_DATA
}
$files += $env:COI_DLTOS

$missing = @($files | Where-Object { $_ -and -not (Test-Path -LiteralPath $_) })
if ($missing.Count -gt 0) {
    Write-Host ('ERROR: не найден файл: ' + ($missing -join ', ')) -ForegroundColor Red
    Write-Host 'Вариант 1. Соберите датасет лиц: .\scripts\prepare-dataset.ps1' -ForegroundColor Yellow
    Write-Host 'Вариант 2. Запустите сервис данных в отдельном окне:' -ForegroundColor Yellow
    Write-Host '          cd ..\ern-dataset' -ForegroundColor Yellow
    Write-Host '          .\bin\ern-dataset.exe -addr :8091 -dir data' -ForegroundColor Yellow
    Write-Host 'Вариант 3. Укажите адрес сервиса одной строкой:' -ForegroundColor Yellow
    Write-Host '          .\scripts\run.ps1 -DataUrl http://127.0.0.1:8091' -ForegroundColor Yellow
    exit 1
}

# --- доступность сервиса данных ---------------------------------------------
if ($env:COI_DATA_URL) {
    try {
        $r = Invoke-WebRequest -Uri ($dataLabel + '/health') -UseBasicParsing -TimeoutSec 3
        if ($r.StatusCode -ge 400) { throw ('ответ ' + $r.StatusCode) }
    } catch {
        Write-Host ('ERROR: сервис данных недоступен: ' + $dataLabel) -ForegroundColor Red
        Write-Host 'Запустите сервис данных (ern-dataset) или уберите COI_DATA_URL,' -ForegroundColor Yellow
        Write-Host 'тогда сведения о лицах возьмутся из data/dataset.json.' -ForegroundColor Yellow
        exit 1
    }
}

# --- журнал проверок ---------------------------------------------------------
New-Item -ItemType Directory -Force -Path 'var' | Out-Null
if ($Reset -and (Test-Path -LiteralPath $env:COI_DB)) {
    Remove-Item -LiteralPath $env:COI_DB -Force
    Write-Host ('удалён старый журнал: ' + $env:COI_DB) -ForegroundColor Cyan
}

Write-Host '== coi.exe ==' -ForegroundColor Cyan
Write-Host ('   лица:    ' + $dataLabel)
Write-Host ('   ДЛТО:    ' + $env:COI_DLTOS)
Write-Host ('   журнал:  ' + $env:COI_DB)
Write-Host ('   адрес:   http://localhost' + $Addr)
Write-Host ('   /admin:  вход ' + $env:COI_ADMIN_USER + ' / ' + $env:COI_ADMIN_PASSWORD) -ForegroundColor Yellow

& .\bin\coi.exe -addr $Addr
exit $LASTEXITCODE
