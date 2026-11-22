# scripts/test.ps1 — vet + race-тесты.
param([switch]$Short)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

Write-Host "== go vet ==" -ForegroundColor Cyan
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

if ($Short) {
    Write-Host "== go test ==" -ForegroundColor Cyan
    go test ./... -count=1
}
else {
    Write-Host "== go test -race ==" -ForegroundColor Cyan
    go test ./... -race -count=1
}
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "OK" -ForegroundColor Green