# scripts/demo.ps1 — сквозной сценарий: check → decision → audit.
# Требует запущенного сервиса в другом окне.
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not $env:AIS_TOKEN) { $env:AIS_TOKEN = "dev-token" }
if (-not $env:COI_ADDR) { $env:COI_ADDR = ":8080" }

$port = $env:COI_ADDR.TrimStart(":")
$base = "http://localhost:$port"

function Invoke-Curl {
    param([string[]]$CurlArgs)
    & curl.exe @CurlArgs
    if ($LASTEXITCODE -ne 0) { throw "curl exit $LASTEXITCODE" }
}

Write-Host "== healthz ==" -ForegroundColor Cyan
Invoke-Curl @("-sS", "$base/healthz")
Write-Host ""

Write-Host "== check DL-014 / P-185 ==" -ForegroundColor Cyan
$checkBody = @'
{
  "dlto_id": "DL-014",
  "person": {
    "passport": "6697 478160",
    "full_name": "ЗАЙЦЕВ ЕГОР СЕРГЕЕВИЧ",
    "address": "г. Нижний Тагил, ул. ГАГАРИНА, д. 100, кв. 61"
  }
}
'@
Invoke-Curl @(
    "-sS", "-X", "POST", "$base/v1/ldt/orders/LTD-DEMO-001/check",
    "-H", "Authorization: Bearer $env:AIS_TOKEN",
    "-H", "Content-Type: application/json",
    "--data", $checkBody
)
Write-Host ""

Write-Host "== decision: reassigned ==" -ForegroundColor Cyan
$decisionBody = @'
{
  "decision": "reassigned",
  "reviewer_id": "U-DEMO-001",
  "comment": "После рассмотрения сигнала о родстве и совпадении адреса назначен другой исполнитель.",
  "new_dlto_id": "DL-017"
}
'@
Invoke-Curl @(
    "-sS", "-X", "PATCH", "$base/v1/ldt/orders/LTD-DEMO-001/decision",
    "-H", "Authorization: Bearer $env:AIS_TOKEN",
    "-H", "Content-Type: application/json",
    "--data", $decisionBody
)
Write-Host ""

Write-Host "== audit ==" -ForegroundColor Cyan
Invoke-Curl @(
    "-sS", "$base/v1/ldt/orders/LTD-DEMO-001/audit",
    "-H", "Authorization: Bearer $env:AIS_TOKEN"
)
Write-Host ""