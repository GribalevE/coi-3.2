# launcher.ps1 - запуск решения без сборки лаунчера (PowerShell, Go не нужен).
#
# Бинарники сервисов собираются заранее: scripts\build.ps1 в каждом репозитории.
# Этот скрипт их находит, проверяет и запускает в правильном порядке.
#
#   .\launcher.ps1              диагностика и запуск
#   .\launcher.ps1 -Check       только диагностика
#   .\launcher.ps1 -Reset       с чистым журналом проверок

param(
    [string]$Root = (Get-Location).Path,
    [string]$DatasetDir = '',
    [string]$AppDir = '',
    [string]$DataExe = '',
    [string]$AppExe = '',
    [int]$DataPort = 8091,
    [int]$AppPort = 8080,
    [string]$Token = 'dev-token',
    [switch]$Reset,
    [switch]$Check,
    [switch]$NoBrowser
)

$ErrorActionPreference = 'Stop'

function Line($level, $text, $color) {
    Write-Host ("[{0,-4}] {1}" -f $level, $text) -ForegroundColor $color
}

function Find-Project($root, $module) {
    $mods = Get-ChildItem -Path $root -Filter go.mod -Recurse -Depth 4 -ErrorAction SilentlyContinue
    foreach ($m in $mods) {
        # go.mod может начинаться с комментария, поэтому ищем строку module по всему файлу
        $lines = Get-Content -Path $m.FullName -ErrorAction SilentlyContinue
        foreach ($line in $lines) {
            if ($line -match ('^module\s+' + [regex]::Escape($module) + '\s*$')) {
                return $m.DirectoryName
            }
        }
    }
    return ''
}

# Ищет исполняемый файл сервиса: явный путь → предпочтительные имена в bin\ →
# единственный .exe в bin\ или в корне проекта.
function Find-Exe($projectDir, $override, $prefer) {
    if ($override) {
        if (Test-Path -LiteralPath $override) {
            return @{ Path = (Resolve-Path -LiteralPath $override).Path; All = @($override) }
        }
        return @{ Path = ''; All = @() }
    }

    $all = @()
    foreach ($d in @((Join-Path $projectDir 'bin'), $projectDir)) {
        if (-not (Test-Path -LiteralPath $d)) { continue }
        $found = Get-ChildItem -Path $d -Filter *.exe -File -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -ne 'coi-launcher.exe' } |
            ForEach-Object { $_.FullName }
        if ($found) { $all += @($found) }
    }

    foreach ($name in $prefer) {
        $hit = $all | Where-Object { [System.IO.Path]::GetFileName($_) -ieq $name } | Select-Object -First 1
        if ($hit) { return @{ Path = $hit; All = $all } }
    }
    if ($all.Count -eq 1) { return @{ Path = $all[0]; All = $all } }
    return @{ Path = ''; All = $all }
}

# Проверяет сигнатуру исполняемого файла Windows: пара байтов "MZ".
function Test-PE($path) {
    try {
        $fs = [System.IO.File]::OpenRead($path)
        try {
            $b = New-Object byte[] 2
            $n = $fs.Read($b, 0, 2)
            return ($n -eq 2 -and $b[0] -eq 0x4D -and $b[1] -eq 0x5A)
        } finally { $fs.Dispose() }
    } catch {
        return $false
    }
}

function Test-PortFree($port) {
    try {
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $port)
        $listener.Start()
        $listener.Stop()
        return $true
    } catch {
        return $false
    }
}

function Wait-Http($url, $timeoutSec) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 2
            if ($r.StatusCode -lt 500) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 300
    }
    return $false
}

$failed = $false

Write-Host 'Диагностика запуска'
Write-Host ('-' * 68)

# --- каталоги проектов -------------------------------------------------------
if (-not $DatasetDir) { $DatasetDir = Find-Project $Root 'ern-dataset' }
if (-not $DatasetDir) {
    Line 'FAIL' "не найден сервис данных (go.mod с module ern-dataset) в $Root - укажите -DatasetDir" Red
    $failed = $true
} else {
    Line 'OK' "сервис данных: $DatasetDir" Green
}

if (-not $AppDir) { $AppDir = Find-Project $Root 'coi-1' }
if (-not $AppDir) {
    Line 'FAIL' "не найден сервис проверки (go.mod с module coi-1) в $Root - укажите -AppDir" Red
    $failed = $true
} else {
    Line 'OK' "сервис проверки: $AppDir" Green
}

if ($failed) {
    Write-Host "`nЗапуск отменён: укажите каталог параметром -Root." -ForegroundColor Red
    exit 2
}

# --- файлы данных ------------------------------------------------------------
$datasetFile = Join-Path $DatasetDir 'data\dataset.json'
if (-not (Test-Path -LiteralPath $datasetFile)) {
    Line 'FAIL' "нет $datasetFile - распакуйте каталог data целиком" Red
    $failed = $true
} else {
    Line 'OK' ("датасет: {0} ({1} КБ)" -f $datasetFile, [int]((Get-Item $datasetFile).Length / 1KB)) Green
}

$dltosFile = Join-Path $AppDir 'data\dltos.json'
if (-not (Test-Path -LiteralPath $dltosFile)) {
    Line 'FAIL' "нет $dltosFile - реестр должностных лиц обязателен, из сервиса данных он не приходит" Red
    $failed = $true
} else {
    Line 'OK' ("реестр: {0} ({1} КБ)" -f $dltosFile, [int]((Get-Item $dltosFile).Length / 1KB)) Green
}

# --- поддержка COI_DATA_URL --------------------------------------------------
$hits = Get-ChildItem -Path $AppDir -Recurse -Filter *.go -File -ErrorAction SilentlyContinue |
    Select-String -Pattern 'COI_DATA_URL' -List -ErrorAction SilentlyContinue
if ($hits) {
    Line 'OK' 'сервис проверки умеет получать данные по HTTP (COI_DATA_URL)' Green
} else {
    $localDataset = Join-Path $AppDir 'data\dataset.json'
    if (Test-Path -LiteralPath $localDataset) {
        Line 'WARN' "сервис проверки не читает COI_DATA_URL - работает на локальном $localDataset" Yellow
    } else {
        Line 'FAIL' "coi.exe не умеет получать данные по HTTP и нет $localDataset - примените патч patch3" Red
        $failed = $true
    }
}

# --- бинарники ---------------------------------------------------------------
$hint = 'cd <проект>; .\scripts\build.ps1'

foreach ($pair in @(@('сервис данных', $DataExe), @('сервис проверки', $AppExe))) {
    $role, $path = $pair
    if ($path -and -not (Test-Path -LiteralPath $path)) {
        Line 'FAIL' "указанный бинарник ($role) не найден: $path" Red
        $failed = $true
    }
}

$res = Find-Exe $DatasetDir $DataExe @('ern-dataset.exe', 'dataset.exe', 'ernserver.exe')
if ($res.Path) {
    if (Test-PE $res.Path) {
        Line 'OK' ("бинарник сервиса данных: {0} ({1:N1} МБ)" -f $res.Path, ((Get-Item $res.Path).Length / 1MB)) Green
        $DataExe = $res.Path
    } else {
        Line 'FAIL' "$($res.Path) не является исполняемым файлом (нет сигнатуры MZ) - пересоберите: $hint" Red
        $failed = $true
    }
} elseif ($res.All.Count -gt 0) {
    Line 'FAIL' ("в каталоге несколько .exe ({0}) - укажите нужный через -DataExe" -f (($res.All | ForEach-Object { Split-Path $_ -Leaf }) -join ', ')) Red
    $failed = $true
} else {
    Line 'FAIL' "нет ни одного .exe сервиса данных в $DatasetDir (bin\) - соберите: $hint" Red
    $failed = $true
}

$res = Find-Exe $AppDir $AppExe @('coi.exe', 'coi-1.exe', 'server.exe')
if ($res.Path) {
    if (Test-PE $res.Path) {
        Line 'OK' ("бинарник сервиса проверки: {0} ({1:N1} МБ)" -f $res.Path, ((Get-Item $res.Path).Length / 1MB)) Green
        $AppExe = $res.Path
    } else {
        Line 'FAIL' "$($res.Path) не является исполняемым файлом (нет сигнатуры MZ) - пересоберите: $hint" Red
        $failed = $true
    }
} elseif ($res.All.Count -gt 0) {
    Line 'FAIL' ("в каталоге несколько .exe ({0}) - укажите нужный через -AppExe" -f (($res.All | ForEach-Object { Split-Path $_ -Leaf }) -join ', ')) Red
    $failed = $true
} else {
    Line 'FAIL' "нет ни одного .exe сервиса проверки в $AppDir (bin\) - соберите: $hint" Red
    $failed = $true
}

# --- блокировка скачанных файлов (Zone.Identifier) ---------------------------
$unblocked = 0
foreach ($dir in @($DatasetDir, $AppDir)) {
    Get-ChildItem -Path $dir -Recurse -Include *.ps1, *.exe -File -ErrorAction SilentlyContinue |
        ForEach-Object {
            $stream = Get-Item $_.FullName -Stream Zone.Identifier -ErrorAction SilentlyContinue
            if ($stream) {
                Unblock-File -Path $_.FullName
                $unblocked++
            }
        }
}
if ($unblocked -gt 0) {
    Line 'FIX' "снята блокировка скачанных файлов: $unblocked" Cyan
} else {
    Line 'OK' 'блокировки скачанных файлов нет' Green
}

# --- порты -------------------------------------------------------------------
if (Test-PortFree $DataPort) {
    Line 'OK' "порт $DataPort свободен (сервис данных)" Green
} else {
    $found = 0
    for ($p = $DataPort + 1; $p -lt $DataPort + 50; $p++) {
        if (Test-PortFree $p) { $found = $p; break }
    }
    if ($found -eq 0) {
        Line 'FAIL' "порт $DataPort занят, свободных рядом нет" Red
        $failed = $true
    } else {
        Line 'FIX' "порт $DataPort занят - использую $found" Cyan
        $DataPort = $found
    }
}

if (Test-PortFree $AppPort) {
    Line 'OK' "порт $AppPort свободен (сервис проверки)" Green
} else {
    $found = 0
    for ($p = $AppPort + 1; $p -lt $AppPort + 50; $p++) {
        if (Test-PortFree $p) { $found = $p; break }
    }
    if ($found -eq 0) {
        Line 'FAIL' "порт $AppPort занят, свободных рядом нет" Red
        $failed = $true
    } else {
        Line 'FIX' "порт $AppPort занят - использую $found" Cyan
        $AppPort = $found
    }
}

# --- журнал проверок ---------------------------------------------------------
$journal = Join-Path $AppDir 'var\coi.db'
if (Test-Path -LiteralPath $journal) {
    if ($Reset) {
        Remove-Item -LiteralPath $journal -Force
        Line 'FIX' "удалён старый журнал $journal" Cyan
    } else {
        Line 'WARN' "журнал $journal уже есть - данные не перезальются, запустите с -Reset" Yellow
    }
} else {
    Line 'OK' 'журнал проверок будет создан заново' Green
}

if ($failed) {
    Write-Host "`nЗапуск отменён: устраните пункты FAIL." -ForegroundColor Red
    exit 2
}

if ($Check) {
    Write-Host "`nПроверка пройдена." -ForegroundColor Green
    exit 0
}

# --- запуск ------------------------------------------------------------------
$db = $null
$app = $null
try {
    New-Item -ItemType Directory -Force -Path (Join-Path $AppDir 'var') | Out-Null

    Write-Host "`n== сервис данных ==" -ForegroundColor Cyan
    $db = Start-Process -FilePath $DataExe -ArgumentList @('-addr', ":$DataPort", '-dir', 'data') `
        -WorkingDirectory $DatasetDir -PassThru -NoNewWindow

    if (-not (Wait-Http "http://127.0.0.1:$DataPort/health" 30)) {
        throw "сервис данных не ответил на /health за 30 с"
    }
    Line 'OK' 'сервис данных готов' Green

    Write-Host '== сервис проверки ==' -ForegroundColor Cyan
    $env:COI_DATA_URL = "http://127.0.0.1:$DataPort"
    $app = Start-Process -FilePath $AppExe -ArgumentList @('-addr', ":$AppPort", '-token', $Token) `
        -WorkingDirectory $AppDir -PassThru -NoNewWindow

    if (-not (Wait-Http "http://localhost:$AppPort/" 30)) {
        throw "сервис проверки не ответил за 30 с"
    }

    $url = "http://localhost:$AppPort"
    if (-not $NoBrowser) { Start-Process $url | Out-Null }

    Write-Host ''
    Line 'OK' "проверка: $url" Green
    Line 'OK' "админка:  $url/admin/  (admin / 123)" Green
    Write-Host ''
    Read-Host 'Enter - остановить оба сервиса' | Out-Null
} finally {
    foreach ($p in @($app, $db)) {
        if ($p -and -not $p.HasExited) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        }
    }
    Remove-Item Env:\COI_DATA_URL -ErrorAction SilentlyContinue
    Write-Host 'сервисы остановлены.'
}
