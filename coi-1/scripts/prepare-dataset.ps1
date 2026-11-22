# scripts/prepare-dataset.ps1 — конвертация SQL-дампа ЕРН в два файла датасета.
#   data/dataset.json — лица (без деления на физических и должностных лиц)
#   data/dltos.json   — реестр должностных лиц (правится через /admin)
# Использование: .\scripts\prepare-dataset.ps1 [-In data\ern_full.sql]

param(
    [string]$In = "data/ern_full.sql",
    [string]$Persons = "data/dataset.json",
    [string]$DLTOS = "data/dltos.json"
)

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: go не найден в PATH." -ForegroundColor Red
    exit 1
}

if (-not (Test-Path $In)) {
    Write-Host "ERROR: не найден файл дампа: $In" -ForegroundColor Red
    exit 1
}

Write-Host "== go run ./cmd/seed ==" -ForegroundColor Cyan
Write-Host "   источник: $In"
Write-Host "   лица:     $Persons"
Write-Host "   ДЛТО:     $DLTOS"

go run ./cmd/seed -in $In -persons $Persons -dltos $DLTOS
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

foreach ($f in @($Persons, $DLTOS)) {
    if (Test-Path $f) {
        $size = (Get-Item $f).Length
        Write-Host ("OK: {0} ({1:N0} байт)" -f $f, $size) -ForegroundColor Green
    }
}

Write-Host "Проверьте строки WARNING выше: отсутствие записи ЗАГС у демо-пары " -ForegroundColor Yellow
Write-Host "означает, что K-01 (родство) не сработает — останутся адрес и фамилия." -ForegroundColor Yellow
