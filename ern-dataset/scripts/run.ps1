# Сборка (если нужно) и запуск сервиса данных ЕРН.

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not (Test-Path .\data\dataset.json)) {
    throw 'Нет data\dataset.json - положите файл основного датасета в папку data.'
}

$addr = if ($env:ERN_ADDR) { $env:ERN_ADDR } else { ':8091' }
$version = if ($env:ERN_VERSION) { $env:ERN_VERSION } else { 'v1.0.0' }

if (-not (Test-Path .\bin\ern-dataset.exe)) {
    Write-Host '== go build ==' -ForegroundColor Cyan
    New-Item -ItemType Directory -Force .\bin | Out-Null
    go build -o .\bin\ern-dataset.exe .\cmd\ernserver
    if ($LASTEXITCODE -ne 0) { throw 'Сборка не удалась.' }
}

Write-Host '== ern-dataset ==' -ForegroundColor Cyan
Write-Host '   датасет: data\dataset.json'
Write-Host "   адрес:   http://localhost$addr"
Write-Host ''

.\bin\ern-dataset.exe -addr $addr -dir data -version $version
