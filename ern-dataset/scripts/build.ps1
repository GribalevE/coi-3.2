# Сборка сервиса данных ЕРН.
# Результат: bin\ern-dataset.exe

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

Write-Host '== go mod tidy ==' -ForegroundColor Cyan
go mod tidy
if ($LASTEXITCODE -ne 0) { throw 'go mod tidy завершился с ошибкой.' }

New-Item -ItemType Directory -Force .\bin | Out-Null

Write-Host '== go build ==' -ForegroundColor Cyan
go build -o .\bin\ern-dataset.exe .\cmd\ernserver
if ($LASTEXITCODE -ne 0) { throw 'Сборка не удалась.' }

Write-Host '== ok ==' -ForegroundColor Green
Write-Host '   bin\ern-dataset.exe -addr :8091 -dir data'
