# Сборка лаунчера: coi-launcher.exe
# Требуется Go 1.22+ (тот же, которым собираются сервисы). Внешних зависимостей нет.
# Файл сохранён в UTF-8 с BOM: иначе PowerShell 5.1 читает кириллицу как cp1251
# и падает с "В строке отсутствует завершающий символ".

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

Write-Host '== go build ==' -ForegroundColor Cyan
go build -trimpath -o .\coi-launcher.exe .
if ($LASTEXITCODE -ne 0) { throw 'Сборка не удалась.' }

Write-Host '== ok ==' -ForegroundColor Green
Write-Host ("   {0}\coi-launcher.exe" -f $PSScriptRoot)
Write-Host '   положите файл рядом с папками проектов и запустите START.cmd'
