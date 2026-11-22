# scripts/build.ps1 — сборка bin\coi.exe.
# Не требует gcc: драйвер SQLite pure-Go (modernc.org/sqlite).
# Использование: .\scripts\build.ps1 [-Clean]

param([switch]$Clean)

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if ($Clean) {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue bin
    Remove-Item -Force -ErrorAction SilentlyContinue var\coi.db, var\coi.db-shm, var\coi.db-wal
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: go не найден в PATH. Установите Go 1.23+ с https://go.dev/dl/" -ForegroundColor Red
    exit 1
}

New-Item -ItemType Directory -Force -Path bin, var | Out-Null

Write-Host "== go mod tidy ==" -ForegroundColor Cyan
go mod tidy
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "== go build ==" -ForegroundColor Cyan
go build -trimpath -o bin/coi.exe ./cmd/server
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "OK: bin\coi.exe" -ForegroundColor Green