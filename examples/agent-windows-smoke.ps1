# End-to-end check of cps-agent as a real Windows service, against a virtual printer.
#
#   vprint (driver share on :9200) <- cps-agent service <- cps server <- job
#
# Needs an elevated prompt (GitHub's windows runners are). Expects cps.exe, vprint.exe and
# cps-agent.exe in $Bin. Run from the repo root:
#   pwsh examples/agent-windows-smoke.ps1 -Bin C:\tmp\bin
param([Parameter(Mandatory)] [string] $Bin)

$ErrorActionPreference = 'Stop'
$api = 'http://localhost:18080/api/v1'
$ops = @{ Authorization = 'Bearer dev-key-ops-0123456789abcdef' }
$wms = @{ Authorization = 'Bearer dev-key-wms-0123456789abcdef' }
$cfg = Join-Path $env:ProgramData 'cps-agent\agent.json'
$work = Join-Path $env:RUNNER_TEMP 'cps-smoke'
New-Item -ItemType Directory -Force $work | Out-Null

function Wait-Until([scriptblock] $cond, [string] $what, [int] $seconds = 60) {
  for ($i = 0; $i -lt $seconds; $i++) {
    try { if (& $cond) { return } } catch {}
    Start-Sleep 1
  }
  throw "timed out waiting for: $what"
}

$server = Get-Content server/config.example.json | ConvertFrom-Json
$server.addr = ':18080'
$server.printers = @()
$server.status_poll_interval = '1s'
$server | ConvertTo-Json -Depth 10 | Set-Content "$work\cps.json"

Start-Process "$Bin\vprint.exe" -ArgumentList '-id','usb1','-listen',':9200','-admin',':19290' `
  -RedirectStandardOutput "$work\vprint.log" -RedirectStandardError "$work\vprint.err" -NoNewWindow
Start-Process "$Bin\cps.exe" -ArgumentList '-config',"$work\cps.json" `
  -RedirectStandardOutput "$work\cps.log" -RedirectStandardError "$work\cps.err" -NoNewWindow
Wait-Until { (Invoke-WebRequest http://localhost:18080/healthz -UseBasicParsing).StatusCode -eq 200 } 'server up'

try {
  $enroll = Invoke-RestMethod -Method Post "$api/admin/agents/enroll" -Headers $ops `
    -ContentType 'application/json' -Body '{"agent_id":"ci-win","site":"wh1","ttl":"1h"}'
  $env:CPS_AGENT_SERVER = 'http://localhost:18080'
  $env:CPS_AGENT_HOST = '127.0.0.1'
  & "$Bin\cps-agent.exe" -version
  & "$Bin\cps-agent.exe" -enroll $enroll.token -id ci-win -site wh1 -config $cfg -listen-base 19390 `
    -install
  if ($LASTEXITCODE -ne 0) { throw "enroll/install failed: $LASTEXITCODE" }
  Write-Host 'config ACL:'; icacls $cfg

  Wait-Until { (Get-Service cps-agent).Status -eq 'Running' } 'service running'
  $printer = $null
  Wait-Until {
    $p = (Invoke-RestMethod "$api/agents" -Headers $ops).items | Where-Object id -eq 'ci-win'
    $script:printer = $p.printers | Select-Object -First 1
    [bool] $script:printer
  } 'agent registered a printer'
  Write-Host "agent printer: $printer"

  Wait-Until {
    $st = Invoke-RestMethod "$api/printers/status" -Headers $ops
    ($st | ConvertTo-Json -Depth 10) -match '"usable":\s*true'
  } 'printer usable via heartbeat'

  $body = @{ printer_id = $printer; external_id = 'win-smoke-1'; zpl = '^XA^FO50,50^A0N,40,40^FDwindows^FS^XZ' } | ConvertTo-Json
  $job = Invoke-RestMethod -Method Post "$api/jobs" -Headers $wms -ContentType 'application/json' -Body $body
  Wait-Until { (Invoke-RestMethod "$api/jobs/$($job.job_id)" -Headers $wms).state -in 'printed','failed','sent_unknown' } 'job finished'
  $state = (Invoke-RestMethod "$api/jobs/$($job.job_id)" -Headers $wms).state
  if ($state -ne 'printed') { throw "job ended $state, want printed" }
  Write-Host "job $($job.job_id): $state"
}
finally {
  $log = Join-Path (Split-Path $cfg) 'agent.log'
  if (Test-Path $log) { Write-Host '--- agent.log'; Get-Content $log }
  & "$Bin\cps-agent.exe" -uninstall
}
if ($LASTEXITCODE -ne 0) { throw 'uninstall failed' }
if (Get-Service cps-agent -ErrorAction SilentlyContinue) { throw 'service still present after -uninstall' }
Write-Host 'windows agent smoke: OK'
